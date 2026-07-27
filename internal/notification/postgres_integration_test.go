package notification

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"git-ctx/internal/store"
)

func TestPostgresOutboxIntegration(t *testing.T) {
	dsn := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GIT_CTX_TEST_POSTGRES_DSN is not set")
	}
	db, err := store.Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	userID, notificationID := "notification-user-"+suffix, "notification-"+suffix
	_, err = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES($1,$2,$3,$4)`, userID, "subject-"+suffix, "notification-user", "user@example.test")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Exec(`DELETE FROM users WHERE id=$1`, userID)
	_, err = db.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES($1,$2,'integration',$3,'Postgres notification','Safe message')`, notificationID, userID, notificationID)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Exec(`DELETE FROM notifications WHERE id=$1`, notificationID)

	var calls atomic.Int32
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	cfg := Config{
		Enabled: true, ActiveSince: time.Unix(0, 0).UTC(), WebhookURL: endpoint.URL,
		Timeout: 2 * time.Second, MaxAttempts: 3,
	}
	service := New(db, func(context.Context) (Config, error) { return cfg, nil })
	if err = service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = service.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	var status string
	var attempts int
	if err = db.DB.QueryRow(`SELECT status,attempts FROM notification_deliveries WHERE notification_id=$1`, notificationID).Scan(&status, &attempts); err != nil {
		t.Fatal(err)
	}
	if status != "delivered" || attempts != 1 || calls.Load() != 1 {
		t.Fatalf("status=%s attempts=%d calls=%d", status, attempts, calls.Load())
	}
}
