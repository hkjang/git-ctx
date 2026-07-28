package apikey

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"strings"
	"time"

	"git-ctx/internal/store"
)

type Service struct {
	store                *store.Store
	pepper               []byte
	rateLimitAlertLoader func(context.Context) bool
}
type Restrictions struct {
	AllowedCIDRs        []string `json:"allowedCidrs,omitempty"`
	AllowedRepositories []string `json:"allowedRepositories,omitempty"`
	RatePerMinute       int      `json:"ratePerMinute,omitempty"`
	RatePerHour         int      `json:"ratePerHour,omitempty"`
	RatePerDay          int      `json:"ratePerDay,omitempty"`
}
type Key struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Prefix       string       `json:"prefix"`
	Scopes       []string     `json:"scopes"`
	Restrictions Restrictions `json:"restrictions"`
	ExpiresAt    *time.Time   `json:"expiresAt,omitempty"`
	CreatedAt    time.Time    `json:"createdAt"`
	LastUsedAt   *time.Time   `json:"lastUsedAt,omitempty"`
	Status       string       `json:"status"`
	ReplacedBy   string       `json:"replacedBy,omitempty"`
}
type AuthInfo struct {
	UserID, KeyID, Prefix string
	Scopes                []string
	Restrictions          Restrictions
}
type RateLimitError struct{ RetryAfter time.Duration }

func (e *RateLimitError) Error() string { return "API key rate limit exceeded" }

func New(s *store.Store, pepper string) *Service { return &Service{store: s, pepper: []byte(pepper)} }

func (s *Service) SetRateLimitAlertLoader(loader func(context.Context) bool) {
	s.rateLimitAlertLoader = loader
}
func (s *Service) Create(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time) (Key, string, error) {
	return s.CreateWithRestrictions(ctx, userID, name, scopes, expiresAt, Restrictions{})
}
func (s *Service) CreateWithRestrictions(ctx context.Context, userID, name string, scopes []string, expiresAt *time.Time, restrictions Restrictions) (Key, string, error) {
	if name == "" || len(name) > 100 {
		return Key{}, "", errors.New("name is required and must not exceed 100 characters")
	}
	if len(scopes) == 0 {
		scopes = []string{"resolve-library-id", "query-docs"}
	}
	if err := validateScopes(scopes); err != nil {
		return Key{}, "", err
	}
	if err := validateRestrictions(restrictions); err != nil {
		return Key{}, "", err
	}
	id, err := randomHex(16)
	if err != nil {
		return Key{}, "", err
	}
	prefixBytes := make([]byte, 3)
	if _, err = rand.Read(prefixBytes); err != nil {
		return Key{}, "", err
	}
	prefix := strings.ToUpper(hex.EncodeToString(prefixBytes))
	secret := make([]byte, 32)
	if _, err = rand.Read(secret); err != nil {
		return Key{}, "", err
	}
	plain := "bctx_live_" + prefix + "_" + base64.RawURLEncoding.EncodeToString(secret)
	now := time.Now().UTC()
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Key{}, "", err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO api_keys(id,user_id,name,prefix,key_hash,scopes,expires_at,created_at) VALUES(?,?,?,?,?,?,?,?)`), id, userID, name, prefix, s.hash(plain), strings.Join(scopes, ","), expiresAt, now)
	if err != nil {
		return Key{}, "", err
	}
	_, err = tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO api_key_restrictions(api_key_id,allowed_cidrs,allowed_repositories,rate_per_minute,rate_per_hour,rate_per_day) VALUES(?,?,?,?,?,?)`), id, strings.Join(restrictions.AllowedCIDRs, ","), strings.Join(restrictions.AllowedRepositories, ","), restrictions.RatePerMinute, restrictions.RatePerHour, restrictions.RatePerDay)
	if err != nil {
		return Key{}, "", err
	}
	if err = tx.Commit(); err != nil {
		return Key{}, "", err
	}
	return Key{ID: id, Name: name, Prefix: prefix, Scopes: scopes, Restrictions: restrictions, ExpiresAt: expiresAt, CreatedAt: now, Status: "active"}, plain, nil
}

func validateScopes(scopes []string) error {
	allowed := map[string]bool{
		"resolve-library-id": true, "query-docs": true,
		"search-repositories": true, "search-source": true, "search-code": true, "find-file": true,
		"get-repository-map": true, "find-symbol": true, "get-symbol-context": true,
		"trace-dependencies": true, "compare-refs": true, "get-change-impact": true,
		"get-context-pack": true, "find-runbook": true, "export-context": true,
		"explain-search-result": true,
		"get-platform-status":   true, "list-index-jobs": true, "reindex-repository": true,
	}
	if len(scopes) == 0 {
		return errors.New("at least one scope is required")
	}
	seen := map[string]bool{}
	for _, scope := range scopes {
		if !allowed[scope] {
			return fmt.Errorf("unsupported scope %q", scope)
		}
		if seen[scope] {
			return fmt.Errorf("duplicate scope %q", scope)
		}
		seen[scope] = true
	}
	return nil
}

func (s *Service) UpdateScopes(ctx context.Context, userID, id string, scopes []string) error {
	return s.updateScopes(ctx, userID, id, scopes, false)
}

func (s *Service) UpdateScopesAdmin(ctx context.Context, id string, scopes []string) error {
	return s.updateScopes(ctx, "", id, scopes, true)
}

func (s *Service) updateScopes(ctx context.Context, userID, id string, scopes []string, administrator bool) error {
	if err := validateScopes(scopes); err != nil {
		return err
	}
	query := `UPDATE api_keys SET scopes=? WHERE id=? AND revoked_at IS NULL`
	args := []any{strings.Join(scopes, ","), id}
	if !administrator {
		query += ` AND user_id=?`
		args = append(args, userID)
	}
	result, err := s.store.DB.ExecContext(ctx, s.store.Rebind(query), args...)
	if err != nil {
		return err
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		return errors.New("active API key not found")
	}
	return nil
}
func (s *Service) Authenticate(ctx context.Context, raw string) (userID, keyID, prefix string, scopes []string, err error) {
	info, err := s.AuthenticateRequest(ctx, raw, "")
	return info.UserID, info.KeyID, info.Prefix, info.Scopes, err
}
func (s *Service) AuthenticateRequest(ctx context.Context, raw, ip string) (AuthInfo, error) {
	parts := strings.SplitN(raw, "_", 4)
	if len(parts) != 4 || parts[0] != "bctx" || parts[1] != "live" {
		return AuthInfo{}, errors.New("invalid API key")
	}
	prefix := parts[2]
	var info AuthInfo
	info.Prefix = prefix
	var hash []byte
	var scopeText, cidrs, repos string
	var expires, disabled, revoked sql.NullTime
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT k.id,k.user_id,k.key_hash,k.scopes,k.expires_at,k.disabled_at,k.revoked_at,COALESCE(r.allowed_cidrs,''),COALESCE(r.allowed_repositories,''),COALESCE(r.rate_per_minute,0),COALESCE(r.rate_per_hour,0),COALESCE(r.rate_per_day,0) FROM api_keys k LEFT JOIN api_key_restrictions r ON r.api_key_id=k.id WHERE k.prefix=?`), prefix).Scan(&info.KeyID, &info.UserID, &hash, &scopeText, &expires, &disabled, &revoked, &cidrs, &repos, &info.Restrictions.RatePerMinute, &info.Restrictions.RatePerHour, &info.Restrictions.RatePerDay)
	if err != nil || !hmac.Equal(hash, s.hash(raw)) {
		return AuthInfo{}, errors.New("invalid API key")
	}
	if disabled.Valid || revoked.Valid || (expires.Valid && time.Now().After(expires.Time)) {
		return AuthInfo{}, errors.New("inactive API key")
	}
	info.Scopes = split(scopeText)
	info.Restrictions.AllowedCIDRs = split(cidrs)
	info.Restrictions.AllowedRepositories = split(repos)
	if err = validateIP(ip, info.Restrictions.AllowedCIDRs); err != nil {
		return AuthInfo{}, err
	}
	if err = s.applyRateLimits(ctx, info.KeyID, info.Restrictions); err != nil {
		return AuthInfo{}, err
	}
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE api_keys SET last_used_at=? WHERE id=?`), time.Now().UTC(), info.KeyID)
	return info, nil
}
func (s *Service) List(ctx context.Context, userID string) ([]Key, error) {
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT k.id,k.name,k.prefix,k.scopes,k.expires_at,k.created_at,k.last_used_at,k.disabled_at,k.revoked_at,COALESCE(k.replaced_by,''),COALESCE(r.allowed_cidrs,''),COALESCE(r.allowed_repositories,''),COALESCE(r.rate_per_minute,0),COALESCE(r.rate_per_hour,0),COALESCE(r.rate_per_day,0) FROM api_keys k LEFT JOIN api_key_restrictions r ON r.api_key_id=k.id WHERE k.user_id=? ORDER BY k.created_at DESC`), userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []Key
	for rows.Next() {
		var k Key
		var scopes, cidrs, repos string
		var expires, used, disabled, revoked sql.NullTime
		if err := rows.Scan(&k.ID, &k.Name, &k.Prefix, &scopes, &expires, &k.CreatedAt, &used, &disabled, &revoked, &k.ReplacedBy, &cidrs, &repos, &k.Restrictions.RatePerMinute, &k.Restrictions.RatePerHour, &k.Restrictions.RatePerDay); err != nil {
			return nil, err
		}
		k.Scopes = split(scopes)
		k.Restrictions.AllowedCIDRs = split(cidrs)
		k.Restrictions.AllowedRepositories = split(repos)
		if expires.Valid {
			k.ExpiresAt = &expires.Time
		}
		if used.Valid {
			k.LastUsedAt = &used.Time
		}
		k.Status = "active"
		if disabled.Valid {
			k.Status = "disabled"
		}
		if revoked.Valid {
			k.Status = "revoked"
		}
		if expires.Valid && time.Now().After(expires.Time) {
			k.Status = "expired"
		}
		result = append(result, k)
	}
	return result, rows.Err()
}
func (s *Service) Rotate(ctx context.Context, userID, id string, overlap time.Duration) (Key, string, error) {
	if overlap < 0 || overlap > 24*time.Hour {
		return Key{}, "", errors.New("overlap must be between 0 and 24 hours")
	}
	var name, scopes, cidrs, repos string
	var expires, disabled, revoked sql.NullTime
	var r Restrictions
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT k.name,k.scopes,k.expires_at,k.disabled_at,k.revoked_at,COALESCE(x.allowed_cidrs,''),COALESCE(x.allowed_repositories,''),COALESCE(x.rate_per_minute,0),COALESCE(x.rate_per_hour,0),COALESCE(x.rate_per_day,0) FROM api_keys k LEFT JOIN api_key_restrictions x ON x.api_key_id=k.id WHERE k.id=? AND k.user_id=?`), id, userID).Scan(&name, &scopes, &expires, &disabled, &revoked, &cidrs, &repos, &r.RatePerMinute, &r.RatePerHour, &r.RatePerDay)
	if err != nil {
		return Key{}, "", err
	}
	if disabled.Valid || revoked.Valid || (expires.Valid && time.Now().After(expires.Time)) {
		return Key{}, "", errors.New("only an active key can be rotated")
	}
	r.AllowedCIDRs = split(cidrs)
	r.AllowedRepositories = split(repos)
	var newExpiry *time.Time
	if expires.Valid {
		t := expires.Time
		newExpiry = &t
	}
	next, plain, err := s.CreateWithRestrictions(ctx, userID, name+" (rotated)", split(scopes), newExpiry, r)
	if err != nil {
		return Key{}, "", err
	}
	oldEnd := time.Now().UTC().Add(overlap)
	if expires.Valid && expires.Time.Before(oldEnd) {
		oldEnd = expires.Time
	}
	_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE api_keys SET expires_at=?,replaced_by=? WHERE id=? AND user_id=?`), oldEnd, next.ID, id, userID)
	if err != nil {
		return Key{}, "", err
	}
	return next, plain, nil
}
func (s *Service) SetStatus(ctx context.Context, userID, id, status string) error {
	var query string
	var args []any
	switch status {
	case "disabled":
		query = `UPDATE api_keys SET disabled_at=? WHERE id=? AND user_id=? AND revoked_at IS NULL`
		args = []any{time.Now().UTC(), id, userID}
	case "enabled":
		query = `UPDATE api_keys SET disabled_at=NULL WHERE id=? AND user_id=? AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at>?)`
		args = []any{id, userID, time.Now().UTC()}
	case "revoked":
		query = `UPDATE api_keys SET revoked_at=? WHERE id=? AND user_id=?`
		args = []any{time.Now().UTC(), id, userID}
	default:
		return errors.New("unsupported key status")
	}
	res, err := s.store.DB.ExecContext(ctx, s.store.Rebind(query), args...)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}
func (s *Service) applyRateLimits(ctx context.Context, keyID string, r Restrictions) error {
	now := time.Now().UTC()
	checks := []struct {
		kind  string
		start time.Time
		limit int
		retry time.Duration
	}{{"minute", now.Truncate(time.Minute), r.RatePerMinute, time.Until(now.Truncate(time.Minute).Add(time.Minute))}, {"hour", now.Truncate(time.Hour), r.RatePerHour, time.Until(now.Truncate(time.Hour).Add(time.Hour))}, {"day", time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC), r.RatePerDay, time.Until(time.Date(now.Year(), now.Month(), now.Day()+1, 0, 0, 0, 0, time.UTC))}}
	for _, x := range checks {
		if x.limit <= 0 {
			continue
		}
		var count int
		err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`INSERT INTO api_key_usage_buckets(api_key_id,bucket_kind,bucket_start,call_count) VALUES(?,?,?,1) ON CONFLICT(api_key_id,bucket_kind,bucket_start) DO UPDATE SET call_count=api_key_usage_buckets.call_count+1 RETURNING call_count`), keyID, x.kind, x.start).Scan(&count)
		if err != nil {
			return err
		}
		if count > x.limit {
			if s.rateLimitAlertLoader == nil || s.rateLimitAlertLoader(ctx) {
				s.notifyRateLimit(ctx, keyID, x.kind, x.start)
			}
			return &RateLimitError{RetryAfter: x.retry}
		}
	}
	return nil
}
func (s *Service) notifyRateLimit(ctx context.Context, keyID, kind string, start time.Time) {
	id := "rate:" + keyID + ":" + kind + ":" + start.UTC().Format("200601021504")
	message := "The " + kind + " call limit was exceeded for an MCP API key."
	_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) SELECT ?,user_id,'api_key_rate_limit',?,'Unusual MCP API key usage',? FROM api_keys WHERE id=? ON CONFLICT(user_id,notification_type,resource_id) DO NOTHING`), id, keyID+":"+kind, message, keyID)
}
func validateRestrictions(r Restrictions) error {
	for _, cidr := range r.AllowedCIDRs {
		if _, err := netip.ParsePrefix(cidr); err != nil {
			return fmt.Errorf("invalid CIDR %q", cidr)
		}
	}
	for _, repo := range r.AllowedRepositories {
		parts := strings.Split(strings.TrimPrefix(repo, "/"), "/")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return fmt.Errorf("repository restriction %q must use /project/repository", repo)
		}
	}
	if r.RatePerMinute < 0 || r.RatePerHour < 0 || r.RatePerDay < 0 {
		return errors.New("rate limits cannot be negative")
	}
	return nil
}
func validateIP(raw string, cidrs []string) error {
	if len(cidrs) == 0 {
		return nil
	}
	ip, err := netip.ParseAddr(raw)
	if err != nil {
		return errors.New("client IP is not allowed")
	}
	for _, cidr := range cidrs {
		p, _ := netip.ParsePrefix(cidr)
		if p.Contains(ip) {
			return nil
		}
	}
	return errors.New("client IP is not allowed")
}
func (s *Service) hash(value string) []byte {
	h := hmac.New(sha256.New, s.pepper)
	h.Write([]byte(value))
	return h.Sum(nil)
}
func randomHex(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
