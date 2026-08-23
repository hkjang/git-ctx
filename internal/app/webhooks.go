package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"
)

// Inbound Bitbucket and GitLab webhooks.

func (a *App) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	sourceType := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	settings, err := a.loadSettingMap(r.Context(), sourceType)
	if err != nil {
		problem(w, http.StatusServiceUnavailable, "webhook_not_configured", "Webhook receiver is not configured")
		return
	}
	secret, _ := settings["webhookSecret"].(string)
	if secret == "" {
		problem(w, http.StatusServiceUnavailable, "webhook_not_configured", "Webhook secret is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		problem(w, http.StatusBadRequest, "invalid_payload", "Webhook payload is too large or unreadable")
		return
	}
	eventID, eventType := "", ""
	valid := false
	if sourceType == "bitbucket" {
		eventID = r.Header.Get("X-Request-Id")
		eventType = r.Header.Get("X-Event-Key")
		signature := strings.TrimPrefix(r.Header.Get("X-Hub-Signature"), "sha256=")
		expected := hmac.New(sha256.New, []byte(secret))
		expected.Write(payload)
		actual, decodeErr := hex.DecodeString(signature)
		valid = decodeErr == nil && hmac.Equal(expected.Sum(nil), actual)
	} else {
		eventID = r.Header.Get("X-Gitlab-Event-UUID")
		eventType = r.Header.Get("X-Gitlab-Event")
		valid = hmac.Equal([]byte(r.Header.Get("X-Gitlab-Token")), []byte(secret))
	}
	if !valid {
		problem(w, http.StatusUnauthorized, "invalid_webhook_signature", "Webhook authentication failed")
		return
	}
	result, err := a.hooks.Enqueue(r.Context(), sourceType, eventID, eventType, payload)
	if err != nil {
		problem(w, http.StatusNotFound, "webhook_rejected", err.Error())
		return
	}
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	jsonOut(w, status, result)
}
