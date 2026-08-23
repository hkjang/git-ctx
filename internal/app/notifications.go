package app

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"time"

	"git-ctx/internal/auth"
	outboundnotification "git-ctx/internal/notification"
	"git-ctx/internal/scheduler"
)

// Notification policy and delivery retries.

func (a *App) notificationPolicy(ctx context.Context) scheduler.NotificationPolicy {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		return scheduler.NotificationPolicy{Enabled: true, APIKeyExpiryWarningDays: 7}
	}
	enabled := true
	if value, ok := settings["inAppEnabled"].(bool); ok {
		enabled = value
	}
	days := 7
	if value, ok := settings["apiKeyExpiryWarningDays"].(float64); ok {
		days = int(value)
	}
	return scheduler.NotificationPolicy{Enabled: enabled, APIKeyExpiryWarningDays: days}
}

func (a *App) notificationDeliveryConfig(ctx context.Context) (outboundnotification.Config, error) {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return notificationDeliveryConfigFromMap(map[string]any{}, time.Now().UTC())
		}
		return outboundnotification.Config{}, err
	}
	if a.secrets != nil {
		settings, err = a.secrets.Resolve(ctx, settings)
		if err != nil {
			return outboundnotification.Config{}, err
		}
	}
	activeSince := time.Now().UTC()
	_ = a.store.DB.QueryRowContext(ctx, a.store.Rebind(`SELECT updated_at FROM system_settings WHERE category=?`), "notifications").Scan(&activeSince)
	return notificationDeliveryConfigFromMap(settings, activeSince)
}

func notificationDeliveryConfigFromMap(settings map[string]any, activeSince time.Time) (outboundnotification.Config, error) {
	cfg := outboundnotification.Config{
		ActiveSince: activeSince,
		SMTPPort:    587,
		SMTPTLSMode: "starttls",
		Timeout:     15 * time.Second,
		MaxAttempts: 5,
	}
	cfg.Enabled, _ = settings["externalEnabled"].(bool)
	cfg.WebhookURL, _ = settings["webhookUrl"].(string)
	cfg.WebhookAuthorization, _ = settings["webhookAuthorization"].(string)
	cfg.MessengerWebhookURL, _ = settings["messengerWebhookUrl"].(string)
	cfg.MessengerAuthorization, _ = settings["messengerAuthorization"].(string)
	cfg.SMTPEnabled, _ = settings["smtpEnabled"].(bool)
	cfg.SMTPHost, _ = settings["smtpHost"].(string)
	cfg.SMTPUsername, _ = settings["smtpUsername"].(string)
	cfg.SMTPPassword, _ = settings["smtpPassword"].(string)
	cfg.SMTPFrom, _ = settings["smtpFrom"].(string)
	cfg.TestRecipient, _ = settings["testRecipient"].(string)
	if value, ok := settings["smtpPort"].(float64); ok {
		cfg.SMTPPort = int(value)
	}
	if value, ok := settings["smtpTlsMode"].(string); ok && value != "" {
		cfg.SMTPTLSMode = value
	}
	if value, ok := settings["timeoutSeconds"].(float64); ok {
		cfg.Timeout = time.Duration(value * float64(time.Second))
	}
	if value, ok := settings["maxAttempts"].(float64); ok {
		cfg.MaxAttempts = int(value)
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	cfg.CACertificate, _ = settings["caCertificate"].(string)
	cfg.ProxyURL, _ = settings["proxyUrl"].(string)
	return cfg, nil
}

func (a *App) rateLimitAlertsEnabled(ctx context.Context) bool {
	settings, err := a.loadSettingMap(ctx, "notifications")
	if err != nil {
		return true
	}
	if enabled, ok := settings["inAppEnabled"].(bool); ok && !enabled {
		return false
	}
	if enabled, ok := settings["rateLimitAlertsEnabled"].(bool); ok {
		return enabled
	}
	return true
}
func (a *App) notificationDeliveries(w http.ResponseWriter, r *http.Request) {
	rows, err := a.store.DB.QueryContext(r.Context(), `SELECT d.id,d.channel,d.status,d.attempts,d.next_attempt_at,d.last_error,d.delivered_at,d.created_at,n.notification_type,n.title,u.username
FROM notification_deliveries d
JOIN notifications n ON n.id=d.notification_id
JOIN users u ON u.id=n.user_id
ORDER BY d.created_at DESC LIMIT 500`)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, channel, status, lastError, notificationType, title, username string
		var attempts int
		var nextAttempt, createdAt time.Time
		var deliveredAt sql.NullTime
		if err = rows.Scan(&id, &channel, &status, &attempts, &nextAttempt, &lastError, &deliveredAt, &createdAt, &notificationType, &title, &username); err != nil {
			problem(w, http.StatusInternalServerError, "internal_error", err.Error())
			return
		}
		item := map[string]any{
			"id": id, "channel": channel, "status": status, "attempts": attempts,
			"nextAttemptAt": nextAttempt, "lastError": lastError, "createdAt": createdAt,
			"notificationType": notificationType, "title": title, "username": username,
		}
		if deliveredAt.Valid {
			item["deliveredAt"] = deliveredAt.Time
		}
		out = append(out, item)
	}
	jsonOut(w, http.StatusOK, out)
}
func (a *App) retryNotificationDelivery(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	result, err := a.store.DB.ExecContext(r.Context(), a.store.Rebind(`UPDATE notification_deliveries SET status='pending',next_attempt_at=?,last_error='',delivered_at=NULL,updated_at=? WHERE id=? AND status IN ('failed','dead')`), time.Now().UTC(), time.Now().UTC(), id)
	if err != nil {
		problem(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		problem(w, http.StatusConflict, "delivery_not_retryable", "Only failed or dead notification deliveries can be retried")
		return
	}
	a.audit(r, p, "notification_delivery.retry", "notification_delivery", id, "success", nil)
	w.WriteHeader(http.StatusAccepted)
}
