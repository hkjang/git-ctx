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
