package notification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"net/smtp"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"git-ctx/internal/netclient"
	"git-ctx/internal/store"
)

type Config struct {
	Enabled                bool
	ActiveSince            time.Time
	WebhookURL             string
	WebhookAuthorization   string
	MessengerWebhookURL    string
	MessengerAuthorization string
	SMTPEnabled            bool
	SMTPHost               string
	SMTPPort               int
	SMTPUsername           string
	SMTPPassword           string
	SMTPFrom               string
	SMTPTLSMode            string
	TestRecipient          string
	Timeout                time.Duration
	MaxAttempts            int
	TLSVerify              *bool
	CACertificate          string
	ProxyURL               string
}

type ConfigLoader func(context.Context) (Config, error)

type Service struct {
	store *store.Store
	load  ConfigLoader
	tick  time.Duration
}

type delivery struct {
	ID, NotificationID, Channel, UserID, Email, Type, ResourceID, Title, Message string
	Attempts                                                                     int
	NextAttempt                                                                  time.Time
}

type payload struct {
	Event          string    `json:"event"`
	NotificationID string    `json:"notificationId"`
	UserID         string    `json:"userId"`
	Type           string    `json:"type"`
	ResourceID     string    `json:"resourceId,omitempty"`
	Title          string    `json:"title"`
	Message        string    `json:"message"`
	CreatedAt      time.Time `json:"createdAt"`
}

func New(s *store.Store, loader ConfigLoader) *Service {
	return &Service{store: s, load: loader, tick: 10 * time.Second}
}

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()
	for {
		_ = s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) RunOnce(ctx context.Context) error {
	cfg, err := s.load(ctx)
	if err != nil || !cfg.Enabled {
		return err
	}
	if err = ValidateConfig(cfg); err != nil {
		return err
	}
	now := time.Now().UTC()
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE notification_deliveries SET status='failed',next_attempt_at=?,last_error='delivery lease expired',updated_at=? WHERE status='sending' AND updated_at<?`), now, now, now.Add(-5*time.Minute))
	if err = s.seed(ctx, cfg, now); err != nil {
		return err
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT d.id,d.notification_id,d.channel,d.attempts,d.next_attempt_at,n.user_id,u.email,n.notification_type,n.resource_id,n.title,n.message
FROM notification_deliveries d JOIN notifications n ON n.id=d.notification_id JOIN users u ON u.id=n.user_id
WHERE d.status IN ('pending','failed') ORDER BY d.created_at LIMIT 100`)
	if err != nil {
		return err
	}
	var deliveries []delivery
	for rows.Next() {
		var item delivery
		if err = rows.Scan(&item.ID, &item.NotificationID, &item.Channel, &item.Attempts, &item.NextAttempt, &item.UserID, &item.Email, &item.Type, &item.ResourceID, &item.Title, &item.Message); err != nil {
			rows.Close()
			return err
		}
		if !item.NextAttempt.After(now) {
			deliveries = append(deliveries, item)
		}
	}
	if err = rows.Close(); err != nil {
		return err
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return err
	}
	for _, item := range deliveries {
		result, claimErr := s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE notification_deliveries SET status='sending',attempts=attempts+1,updated_at=? WHERE id=? AND status IN ('pending','failed')`), now, item.ID)
		if claimErr != nil {
			continue
		}
		affected, _ := result.RowsAffected()
		if affected != 1 {
			continue
		}
		sendErr := s.send(ctx, client, cfg, item)
		if sendErr == nil {
			_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE notification_deliveries SET status='delivered',delivered_at=?,last_error='',updated_at=? WHERE id=? AND status='sending'`), time.Now().UTC(), time.Now().UTC(), item.ID)
			continue
		}
		attempts := item.Attempts + 1
		status := "failed"
		if attempts >= cfg.MaxAttempts {
			status = "dead"
		}
		delay := time.Minute * time.Duration(1<<min(attempts-1, 6))
		_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE notification_deliveries SET status=?,next_attempt_at=?,last_error=?,updated_at=? WHERE id=? AND status='sending'`), status, time.Now().UTC().Add(delay), truncate(sendErr.Error(), 500), time.Now().UTC(), item.ID)
	}
	return nil
}

func (s *Service) seed(ctx context.Context, cfg Config, now time.Time) error {
	for _, destination := range []struct{ channel, value string }{
		{"webhook", cfg.WebhookURL},
		{"messenger", cfg.MessengerWebhookURL},
	} {
		if destination.value == "" {
			continue
		}
		if err := s.seedFixedDestination(ctx, destination.channel, destination.value, cfg.ActiveSince, now); err != nil {
			return err
		}
	}
	if cfg.SMTPEnabled {
		return s.seedEmail(ctx, cfg.ActiveSince, now)
	}
	return nil
}

func (s *Service) seedFixedDestination(ctx context.Context, channel, destination string, activeSince, now time.Time) error {
	hash := destinationHash(channel, destination)
	activeSinceValue := any(activeSince)
	if s.store.Driver() == "sqlite" {
		// SQLite CURRENT_TIMESTAMP has second precision. Round down so a
		// notification created later in the same second as the setting update
		// is not skipped.
		activeSinceValue = activeSince.UTC().Truncate(time.Second).Format("2006-01-02 15:04:05")
	}
	for {
		rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT n.id
FROM notifications n
WHERE n.created_at>=? AND NOT EXISTS (
  SELECT 1 FROM notification_deliveries d
  WHERE d.notification_id=n.id AND d.channel=? AND d.destination_hash=?
)
ORDER BY n.created_at,n.id LIMIT 1000`), activeSinceValue, channel, hash)
		if err != nil {
			return err
		}
		var notificationIDs []string
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			notificationIDs = append(notificationIDs, id)
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, notificationID := range notificationIDs {
			id := deliveryID(notificationID, channel, hash)
			if _, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at) VALUES(?,?,?,?,?) ON CONFLICT(notification_id,channel,destination_hash) DO NOTHING`), id, notificationID, channel, hash, now); err != nil {
				return err
			}
		}
		if len(notificationIDs) < 1000 {
			return nil
		}
	}
}

func (s *Service) seedEmail(ctx context.Context, activeSince, now time.Time) error {
	activeSinceValue := any(activeSince)
	if s.store.Driver() == "sqlite" {
		activeSinceValue = activeSince.UTC().Truncate(time.Second).Format("2006-01-02 15:04:05")
	}
	for {
		rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT n.id,u.email
FROM notifications n JOIN users u ON u.id=n.user_id
WHERE n.created_at>=? AND u.email<>'' AND NOT EXISTS (
  SELECT 1 FROM notification_deliveries d
  WHERE d.notification_id=n.id AND d.channel='email'
)
ORDER BY n.created_at,n.id LIMIT 1000`), activeSinceValue)
		if err != nil {
			return err
		}
		type recipient struct{ notificationID, email string }
		var recipients []recipient
		for rows.Next() {
			var item recipient
			if err = rows.Scan(&item.notificationID, &item.email); err != nil {
				rows.Close()
				return err
			}
			recipients = append(recipients, item)
		}
		if err = rows.Close(); err != nil {
			return err
		}
		for _, item := range recipients {
			hash := destinationHash("email", item.email)
			id := deliveryID(item.notificationID, "email", hash)
			if _, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO notification_deliveries(id,notification_id,channel,destination_hash,next_attempt_at) VALUES(?,?,?,?,?) ON CONFLICT(notification_id,channel,destination_hash) DO NOTHING`), id, item.notificationID, "email", hash, now); err != nil {
				return err
			}
		}
		if len(recipients) < 1000 {
			return nil
		}
	}
}

func (s *Service) send(ctx context.Context, client *http.Client, cfg Config, item delivery) error {
	switch item.Channel {
	case "webhook":
		return postWebhook(ctx, client, cfg.WebhookURL, cfg.WebhookAuthorization, item)
	case "messenger":
		return postWebhook(ctx, client, cfg.MessengerWebhookURL, cfg.MessengerAuthorization, item)
	case "email":
		return sendEmail(ctx, cfg, item.Email, item.Title, item.Message)
	default:
		return errors.New("unsupported notification channel")
	}
}

func postWebhook(ctx context.Context, client *http.Client, endpoint, authorization string, item delivery) error {
	body, _ := json.Marshal(payload{Event: "git_ctx.notification", NotificationID: item.NotificationID, UserID: item.UserID, Type: item.Type, ResourceID: item.ResourceID, Title: item.Title, Message: item.Message, CreatedAt: time.Now().UTC()})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %s", response.Status)
	}
	return nil
}

func Validate(ctx context.Context, cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return err
	}
	test := delivery{NotificationID: "connection-test", UserID: "administrator", Type: "connection_test", ResourceID: "notifications", Title: "git-ctx notification connection test", Message: "The administrator requested this test message."}
	if cfg.WebhookURL != "" {
		if err = postWebhook(ctx, client, cfg.WebhookURL, cfg.WebhookAuthorization, test); err != nil {
			return fmt.Errorf("webhook test: %w", err)
		}
	}
	if cfg.MessengerWebhookURL != "" {
		if err = postWebhook(ctx, client, cfg.MessengerWebhookURL, cfg.MessengerAuthorization, test); err != nil {
			return fmt.Errorf("messenger webhook test: %w", err)
		}
	}
	if cfg.SMTPEnabled {
		if cfg.TestRecipient == "" {
			return errors.New("notifications.testRecipient is required for an SMTP connection test")
		}
		if err = sendEmail(ctx, cfg, cfg.TestRecipient, test.Title, test.Message); err != nil {
			return fmt.Errorf("SMTP test: %w", err)
		}
	}
	return nil
}

func ValidateConfig(cfg Config) error {
	if cfg.Timeout <= 0 || cfg.Timeout > 2*time.Minute {
		return errors.New("notifications.timeoutSeconds must be 1..120")
	}
	if cfg.MaxAttempts < 1 || cfg.MaxAttempts > 20 {
		return errors.New("notifications.maxAttempts must be 1..20")
	}
	for name, raw := range map[string]string{"webhookUrl": cfg.WebhookURL, "messengerWebhookUrl": cfg.MessengerWebhookURL} {
		if raw == "" {
			continue
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("notifications.%s must be an absolute URL without credentials or fragment", name)
		}
		if parsed.Scheme != "https" && parsed.Hostname() != "localhost" && parsed.Hostname() != "127.0.0.1" {
			return fmt.Errorf("notifications.%s must use HTTPS outside localhost", name)
		}
	}
	if cfg.SMTPEnabled {
		if strings.TrimSpace(cfg.SMTPHost) == "" || cfg.SMTPPort < 1 || cfg.SMTPPort > 65535 {
			return errors.New("notifications SMTP host and port are required")
		}
		if _, err := mail.ParseAddress(cfg.SMTPFrom); err != nil {
			return errors.New("notifications.smtpFrom must be a valid email address")
		}
		if cfg.SMTPTLSMode != "tls" && cfg.SMTPTLSMode != "starttls" && cfg.SMTPTLSMode != "none" {
			return errors.New("notifications.smtpTlsMode must be tls, starttls, or none")
		}
		if cfg.SMTPTLSMode == "none" && cfg.SMTPHost != "localhost" && cfg.SMTPHost != "127.0.0.1" && cfg.SMTPHost != "::1" {
			return errors.New("notifications.smtpTlsMode none is allowed only for localhost")
		}
		if cfg.TestRecipient != "" {
			if _, err := mail.ParseAddress(cfg.TestRecipient); err != nil {
				return errors.New("notifications.testRecipient must be a valid email address")
			}
		}
	}
	if cfg.Enabled && cfg.WebhookURL == "" && cfg.MessengerWebhookURL == "" && !cfg.SMTPEnabled {
		return errors.New("notifications external delivery is enabled but no channel is configured")
	}
	return nil
}

func sendEmail(ctx context.Context, cfg Config, recipient, subject, message string) error {
	to, err := mail.ParseAddress(recipient)
	if err != nil {
		return errors.New("notification recipient email is invalid")
	}
	from, err := mail.ParseAddress(cfg.SMTPFrom)
	if err != nil {
		return errors.New("notification sender email is invalid")
	}
	address := net.JoinHostPort(cfg.SMTPHost, strconv.Itoa(cfg.SMTPPort))
	dialer := &net.Dialer{Timeout: cfg.Timeout}
	var connection net.Conn
	tlsConfig, err := notificationTLSConfig(cfg)
	if err != nil {
		return err
	}
	if cfg.SMTPTLSMode == "tls" {
		connection, err = tls.DialWithDialer(dialer, "tcp", address, tlsConfig)
	} else {
		connection, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return err
	}
	defer connection.Close()
	if err = connection.SetDeadline(time.Now().Add(cfg.Timeout)); err != nil {
		return err
	}
	client, err := smtp.NewClient(connection, cfg.SMTPHost)
	if err != nil {
		return err
	}
	defer client.Close()
	if cfg.SMTPTLSMode == "starttls" {
		ok, _ := client.Extension("STARTTLS")
		if !ok {
			return errors.New("SMTP server does not support STARTTLS")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			return err
		}
	}
	if cfg.SMTPUsername != "" {
		if ok, _ := client.Extension("AUTH"); !ok {
			return errors.New("SMTP server does not support AUTH")
		}
		if err = client.Auth(smtp.PlainAuth("", cfg.SMTPUsername, cfg.SMTPPassword, cfg.SMTPHost)); err != nil {
			return err
		}
	}
	if err = client.Mail(from.Address); err != nil {
		return err
	}
	if err = client.Rcpt(to.Address); err != nil {
		return err
	}
	writer, err := client.Data()
	if err != nil {
		return err
	}
	safeSubject := strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)
	raw := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=UTF-8\r\n\r\n%s\r\n", from.String(), to.String(), safeSubject, message)
	if _, err = writer.Write([]byte(raw)); err != nil {
		writer.Close()
		return err
	}
	if err = writer.Close(); err != nil {
		return err
	}
	return client.Quit()
}

func notificationTLSConfig(cfg Config) (*tls.Config, error) {
	verify := true
	if cfg.TLSVerify != nil {
		verify = *cfg.TLSVerify
	}
	tlsConfig := &tls.Config{ServerName: cfg.SMTPHost, MinVersion: tls.VersionTLS12, InsecureSkipVerify: !verify} // #nosec G402 -- administrator-controlled compatibility option
	if strings.TrimSpace(cfg.CACertificate) == "" {
		return tlsConfig, nil
	}
	pem := []byte(cfg.CACertificate)
	if !strings.Contains(cfg.CACertificate, "BEGIN CERTIFICATE") {
		loaded, err := os.ReadFile(cfg.CACertificate)
		if err != nil {
			return nil, fmt.Errorf("read notification CA certificate: %w", err)
		}
		pem = loaded
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(pem) {
		return nil, errors.New("notification CA certificate contains no valid certificate")
	}
	tlsConfig.RootCAs = roots
	return tlsConfig, nil
}

func destinationHash(channel, destination string) string {
	sum := sha256.Sum256([]byte(channel + "\x00" + strings.ToLower(strings.TrimSpace(destination))))
	return hex.EncodeToString(sum[:])
}

func deliveryID(notificationID, channel, hash string) string {
	sum := sha256.Sum256([]byte(notificationID + "\x00" + channel + "\x00" + hash))
	return "delivery:" + hex.EncodeToString(sum[:16])
}

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	// Cut on a rune boundary; notification titles and messages are Korean.
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
