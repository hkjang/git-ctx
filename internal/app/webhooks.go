package app

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"

	"git-ctx/internal/webhook"
)

// Inbound Bitbucket and GitLab webhooks.

func (a *App) receiveWebhook(w http.ResponseWriter, r *http.Request) {
	sourceType := strings.TrimPrefix(r.URL.Path, "/webhooks/")
	settings, err := a.loadSettingMap(r.Context(), sourceType)
	if err != nil {
		// No row means this source has never been configured, which an operator
		// has to fix. Any other failure is this platform's problem and the sender
		// should try again rather than being told its hook is misconfigured.
		if errors.Is(err, sql.ErrNoRows) {
			problem(w, http.StatusServiceUnavailable, "webhook_not_configured", "Webhook receiver is not configured")
			return
		}
		problem(w, http.StatusInternalServerError, "webhook_failed", "Webhook could not be received; retry is expected")
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
		// The status code is what the source server acts on: it stops retrying a
		// 4xx and keeps retrying a 5xx. Reporting a database failure as a
		// rejection therefore drops the event for good.
		switch {
		case errors.Is(err, webhook.ErrPayloadUnreadable):
			problem(w, http.StatusBadRequest, "invalid_payload", err.Error())
		case errors.Is(err, webhook.ErrRepositoryUnknown), errors.Is(err, webhook.ErrSourceUnsupported):
			problem(w, http.StatusNotFound, "webhook_rejected", err.Error())
		default:
			problem(w, http.StatusInternalServerError, "webhook_failed", "Webhook could not be recorded; retry is expected")
		}
		return
	}
	status := http.StatusAccepted
	if result.Duplicate {
		status = http.StatusOK
	}
	jsonOut(w, status, result)
}
