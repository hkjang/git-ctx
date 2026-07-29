package notification

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git-ctx/internal/store"
)

func notificationFixture(t *testing.T) *store.Store {
	t.Helper()
	db, err := store.Open(context.Background(), "sqlite", "file:"+strings.ReplaceAll(t.Name(), "/", "-")+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','u1','alice','alice@example.test')`)
	_, _ = db.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('n1','u1','api_key_expiring','k1','Key expires','Rotate the key')`)
	return db
}

func TestWebhookDeliveryIsIdempotent(t *testing.T) {
	db := notificationFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer delivery-secret" {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	cfg := Config{Enabled: true, ActiveSince: time.Now().Add(-time.Hour), WebhookURL: server.URL, WebhookAuthorization: "Bearer delivery-secret", Timeout: time.Second, MaxAttempts: 3}
	service := New(db, func(context.Context) (Config, error) { return cfg, nil })
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d", calls.Load())
	}
	var status string
	var attempts int
	if err := db.DB.QueryRow(`SELECT status,attempts FROM notification_deliveries`).Scan(&status, &attempts); err != nil || status != "delivered" || attempts != 1 {
		t.Fatalf("status=%s attempts=%d err=%v", status, attempts, err)
	}
}

func TestWebhookFailureBackoffAndDeadLetter(t *testing.T) {
	db := notificationFixture(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "response body must not be persisted", http.StatusBadGateway)
	}))
	defer server.Close()
	cfg := Config{Enabled: true, ActiveSince: time.Now().Add(-time.Hour), WebhookURL: server.URL, Timeout: time.Second, MaxAttempts: 2}
	service := New(db, func(context.Context) (Config, error) { return cfg, nil })
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	_, _ = db.DB.Exec(`UPDATE notification_deliveries SET next_attempt_at=?`, time.Now().Add(-time.Minute))
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status, lastError string
	var attempts int
	if err := db.DB.QueryRow(`SELECT status,attempts,last_error FROM notification_deliveries`).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != "dead" || attempts != 2 || strings.Contains(lastError, "response body") {
		t.Fatalf("status=%s attempts=%d lastError=%q", status, attempts, lastError)
	}
}

func TestRunOnceSkipsFutureDeliveriesBeforeApplyingBatchLimit(t *testing.T) {
	db := notificationFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	now := time.Now().UTC()
	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		notificationID := fmt.Sprintf("future-%03d", i)
		if _, err = tx.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message,created_at) VALUES(?,?,?,?,?,?,?)`,
			notificationID, "u1", "future", notificationID, "Future", "Not due yet", now.Add(-2*time.Hour)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at,created_at) VALUES(?,?,?,?,?,?)`,
			"delivery:"+notificationID, notificationID, "webhook", fmt.Sprintf("future-hash-%03d", i), now.Add(time.Hour), now.Add(-2*time.Hour)); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	dueHash := destinationHash("webhook", server.URL)
	if _, err = tx.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at,created_at) VALUES(?,?,?,?,?,?)`,
		"delivery:due", "n1", "webhook", dueHash, now.Add(-time.Minute), now.Add(-time.Hour)); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}

	cfg := Config{
		Enabled: true, ActiveSince: now.Add(time.Hour), WebhookURL: server.URL,
		Timeout: time.Second, MaxAttempts: 3,
	}
	service := New(db, func(context.Context) (Config, error) { return cfg, nil })
	if err = service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("webhook calls=%d want=1", calls.Load())
	}
	var status string
	if err = db.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE id='delivery:due'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" {
		t.Fatalf("due delivery status=%q want=delivered", status)
	}
	var touched int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE notification_id LIKE 'future-%' AND (status<>'pending' OR attempts<>0)`).Scan(&touched); err != nil {
		t.Fatal(err)
	}
	if touched != 0 {
		t.Fatalf("future deliveries touched=%d want=0", touched)
	}
}

func TestSeedEmailCreatesNewDeliveryAfterDestinationChanges(t *testing.T) {
	db := notificationFixture(t)
	service := New(db, nil)
	activeSince := time.Now().Add(-time.Hour)
	now := time.Now().UTC()
	if err := service.seedEmail(context.Background(), activeSince, now); err != nil {
		t.Fatal(err)
	}
	oldHash := destinationHash("email", "alice@example.test")
	if _, err := db.DB.Exec(`UPDATE notification_deliveries SET status='dead' WHERE notification_id='n1' AND channel='email' AND destination_hash=?`, oldHash); err != nil {
		t.Fatal(err)
	}
	if _, err := db.DB.Exec(`UPDATE users SET email='alice.new@example.test' WHERE id='u1'`); err != nil {
		t.Fatal(err)
	}
	if err := service.seedEmail(context.Background(), activeSince, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	newHash := destinationHash("email", "alice.new@example.test")
	var oldStatus, newStatus string
	if err := db.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE notification_id='n1' AND channel='email' AND destination_hash=?`, oldHash).Scan(&oldStatus); err != nil {
		t.Fatal(err)
	}
	if err := db.DB.QueryRow(`SELECT status FROM notification_deliveries WHERE notification_id='n1' AND channel='email' AND destination_hash=?`, newHash).Scan(&newStatus); err != nil {
		t.Fatal(err)
	}
	if oldStatus != "dead" || newStatus != "pending" {
		t.Fatalf("old status=%q new status=%q", oldStatus, newStatus)
	}
	var deliveries int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE notification_id='n1' AND channel='email'`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 2 {
		t.Fatalf("email deliveries=%d want=2", deliveries)
	}
}

func TestRunOnceSupersedesStaleDestinationHashes(t *testing.T) {
	db := notificationFixture(t)
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	now := time.Now().UTC()
	_, _ = db.DB.Exec(`UPDATE users SET email='alice.new@example.test' WHERE id='u1'`)
	_, _ = db.DB.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at) VALUES(?,?,?,?,?)`,
		"stale-email", "n1", "email", destinationHash("email", "alice@example.test"), now)
	_, _ = db.DB.Exec(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at) VALUES(?,?,?,?,?)`,
		"stale-webhook", "n1", "webhook", destinationHash("webhook", "https://old.example.test/hook"), now)
	cfg := Config{Enabled: true, ActiveSince: now.Add(-time.Hour), WebhookURL: server.URL, Timeout: time.Second, MaxAttempts: 3}
	service := New(db, func(context.Context) (Config, error) { return cfg, nil })
	if err := service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("new webhook calls=%d want=1", calls.Load())
	}
	for _, id := range []string{"stale-email", "stale-webhook"} {
		var status, lastError string
		var attempts int
		if err := db.DB.QueryRow(`SELECT status,attempts,last_error FROM notification_deliveries WHERE id=?`, id).Scan(&status, &attempts, &lastError); err != nil {
			t.Fatal(err)
		}
		if status != "superseded" || attempts != 0 || !strings.Contains(lastError, "destination changed") {
			t.Fatalf("%s status=%q attempts=%d error=%q", id, status, attempts, lastError)
		}
	}
	var delivered int
	if err := db.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel='webhook' AND status='delivered'`).Scan(&delivered); err != nil || delivered != 1 {
		t.Fatalf("delivered webhooks=%d err=%v", delivered, err)
	}
}

func TestDestinationHashPreservesCaseSensitiveURLComponents(t *testing.T) {
	if destinationHash("email", " Alice@Example.Test ") != destinationHash("email", "alice@example.test") {
		t.Fatal("email destination normalization changed")
	}
	if destinationHash("webhook", "HTTPS://HOOKS.EXAMPLE.TEST/Path?Token=ABC") != destinationHash("webhook", "https://hooks.example.test/Path?Token=ABC") {
		t.Fatal("URL scheme or host was not canonicalized")
	}
	if destinationHash("webhook", "https://hooks.example.test/Path?Token=ABC") == destinationHash("webhook", "https://hooks.example.test/path?Token=ABC") {
		t.Fatal("case-sensitive URL path was collapsed")
	}
	if destinationHash("messenger", "https://hooks.example.test/path?Token=ABC") == destinationHash("messenger", "https://hooks.example.test/path?Token=abc") {
		t.Fatal("case-sensitive URL query was collapsed")
	}
}

func TestValidateConfigRejectsUnsafeDestinations(t *testing.T) {
	base := Config{Timeout: time.Second, MaxAttempts: 3}
	if err := ValidateConfig(base); err != nil {
		t.Fatal(err)
	}
	base.Enabled, base.WebhookURL = true, "http://external.example/hook"
	if err := ValidateConfig(base); err == nil {
		t.Fatal("insecure external webhook accepted")
	}
	base.WebhookURL = "https://user:pass@external.example/hook"
	if err := ValidateConfig(base); err == nil {
		t.Fatal("webhook URL credentials accepted")
	}
	base = Config{Timeout: time.Second, MaxAttempts: 3, SMTPEnabled: true, SMTPHost: "smtp.example.test", SMTPPort: 25, SMTPFrom: "git-ctx@example.test", SMTPTLSMode: "none"}
	if err := ValidateConfig(base); err == nil {
		t.Fatal("plaintext remote SMTP accepted")
	}
}

func TestSeedPagesBeyondThousandNotifications(t *testing.T) {
	db := notificationFixture(t)
	tx, err := db.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 2; i <= 1205; i++ {
		id := fmt.Sprintf("n%d", i)
		if _, err = tx.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES(?,?,?,?,?,?)`, id, "u1", "bulk", id, "Bulk", "Bulk notification"); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	service := New(db, nil)
	if err = service.seed(context.Background(), Config{ActiveSince: time.Now().Add(-time.Hour), WebhookURL: "https://hooks.example.test/git-ctx"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var deliveries int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE channel='webhook'`).Scan(&deliveries); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1205 {
		t.Fatalf("deliveries=%d want=1205", deliveries)
	}
}

func TestSQLiteSeedIncludesNotificationCreatedInSettingSecond(t *testing.T) {
	db := notificationFixture(t)
	activeSince := time.Now().UTC()
	_, err := db.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('same-second','u1','setting_test','same-second','Same second','Must be delivered')`)
	if err != nil {
		t.Fatal(err)
	}
	service := New(db, nil)
	if err = service.seed(context.Background(), Config{ActiveSince: activeSince, WebhookURL: "https://hooks.example.test/git-ctx"}, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE notification_id='same-second'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("same-second delivery count=%d", count)
	}
}

func TestSMTPConnectionTestSendsMessage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	message := make(chan string, 1)
	serverError := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverError <- acceptErr
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		writer := bufio.NewWriter(connection)
		write := func(value string) bool {
			if _, writeErr := writer.WriteString(value); writeErr != nil {
				serverError <- writeErr
				return false
			}
			if flushErr := writer.Flush(); flushErr != nil {
				serverError <- flushErr
				return false
			}
			return true
		}
		if !write("220 localhost test SMTP\r\n") {
			return
		}
		for {
			line, readErr := reader.ReadString('\n')
			if readErr != nil {
				serverError <- readErr
				return
			}
			switch {
			case strings.HasPrefix(line, "EHLO"):
				if !write("250-localhost\r\n250 HELP\r\n") {
					return
				}
			case strings.HasPrefix(line, "MAIL FROM:"), strings.HasPrefix(line, "RCPT TO:"):
				if !write("250 OK\r\n") {
					return
				}
			case strings.HasPrefix(line, "DATA"):
				if !write("354 End data with <CR><LF>.<CR><LF>\r\n") {
					return
				}
				var body strings.Builder
				for {
					dataLine, dataErr := reader.ReadString('\n')
					if dataErr != nil {
						serverError <- dataErr
						return
					}
					if dataLine == ".\r\n" {
						break
					}
					body.WriteString(dataLine)
				}
				message <- body.String()
				if !write("250 queued\r\n") {
					return
				}
			case strings.HasPrefix(line, "QUIT"):
				write("221 bye\r\n")
				return
			default:
				if !write("250 OK\r\n") {
					return
				}
			}
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	cfg := Config{
		SMTPEnabled: true, SMTPHost: "127.0.0.1", SMTPPort: port,
		SMTPFrom: "git-ctx@example.test", SMTPTLSMode: "none",
		TestRecipient: "operator@example.test", Timeout: 2 * time.Second, MaxAttempts: 3,
	}
	if err = Validate(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	select {
	case body := <-message:
		if !strings.Contains(body, "Subject: git-ctx notification connection test") || !strings.Contains(body, "operator@example.test") {
			t.Fatalf("unexpected SMTP message: %s", body)
		}
	case err = <-serverError:
		t.Fatal(err)
	case <-time.After(2 * time.Second):
		t.Fatal("SMTP test message was not received")
	}
}
