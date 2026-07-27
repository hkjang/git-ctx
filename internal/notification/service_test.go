package notification

import (
	"context"
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
}
