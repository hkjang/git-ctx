package apikey

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/store"
)

func TestCreateAuthenticateAndRevoke(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, err = s.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','alice','alice','alice@example.test')`)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(s, strings.Repeat("p", 32))
	key, raw, err := svc.Create(ctx, "u1", "codex", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(raw, "bctx_live_"+key.Prefix+"_") {
		t.Fatalf("unexpected key format: %q", raw)
	}

	var stored string
	if err := s.DB.QueryRow(`SELECT hex(key_hash) FROM api_keys WHERE id=?`, key.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, raw) {
		t.Fatal("plaintext key was stored")
	}
	userID, _, prefix, scopes, err := svc.Authenticate(ctx, raw)
	if err != nil || userID != "u1" || prefix != key.Prefix || len(scopes) != 2 {
		t.Fatalf("authentication failed: %v", err)
	}
	if err := svc.SetStatus(ctx, "u1", key.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := svc.Authenticate(ctx, raw); err == nil {
		t.Fatal("revoked key authenticated")
	}
}

func TestUpdateExistingKeyScopes(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:scope-update?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, _ = s.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','alice','alice','')`)
	svc := New(s, strings.Repeat("p", 32))
	key, raw, err := svc.Create(ctx, "u1", "editable", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.UpdateScopes(ctx, "u1", key.ID, []string{"search-code", "query-docs"}); err != nil {
		t.Fatal(err)
	}
	_, _, _, scopes, err := svc.Authenticate(ctx, raw)
	if err != nil || len(scopes) != 2 || scopes[0] != "search-code" {
		t.Fatalf("scopes=%v err=%v", scopes, err)
	}
	if err = svc.UpdateScopes(ctx, "other", key.ID, []string{"query-docs"}); err == nil {
		t.Fatal("another user changed the key scopes")
	}
	if err = svc.UpdateScopes(ctx, "u1", key.ID, []string{"unknown-tool"}); err == nil {
		t.Fatal("unsupported scope was accepted")
	}
}

func TestRestrictionsRateLimitRotationAndPause(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, err = s.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u2','bob','bob','')`)
	if err != nil {
		t.Fatal(err)
	}
	svc := New(s, strings.Repeat("q", 32))
	restrictions := Restrictions{AllowedCIDRs: []string{"10.0.0.0/8"}, AllowedRepositories: []string{"/kcb/demo"}, RatePerMinute: 1}
	key, raw, err := svc.CreateWithRestrictions(ctx, "u2", "restricted", []string{"query-docs"}, nil, restrictions)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.AuthenticateRequest(ctx, raw, "192.168.1.1"); err == nil {
		t.Fatal("disallowed IP authenticated")
	}
	info, err := svc.AuthenticateRequest(ctx, raw, "10.20.30.40")
	if err != nil || len(info.Restrictions.AllowedRepositories) != 1 {
		t.Fatalf("authentication=%#v err=%v", info, err)
	}
	if _, err = svc.AuthenticateRequest(ctx, raw, "10.20.30.40"); err == nil {
		t.Fatal("rate limit was not enforced")
	} else {
		var limited *RateLimitError
		if !errors.As(err, &limited) {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	var notices int
	_ = s.DB.QueryRow(`SELECT COUNT(*) FROM notifications WHERE user_id='u2' AND notification_type='api_key_rate_limit'`).Scan(&notices)
	if notices != 1 {
		t.Fatalf("rate notifications=%d", notices)
	}
	if err = svc.SetStatus(ctx, "u2", key.ID, "disabled"); err != nil {
		t.Fatal(err)
	}
	if _, err = svc.AuthenticateRequest(ctx, raw, "10.20.30.40"); err == nil {
		t.Fatal("disabled key authenticated")
	}
	if err = svc.SetStatus(ctx, "u2", key.ID, "enabled"); err != nil {
		t.Fatal(err)
	}

	replacement, replacementRaw, err := svc.Rotate(ctx, "u2", key.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if replacement.ID == key.ID || replacementRaw == raw {
		t.Fatal("rotation did not issue a new key")
	}
	if _, err = svc.AuthenticateRequest(ctx, raw, "10.20.30.40"); err == nil {
		t.Fatal("zero-overlap old key authenticated")
	}
	if _, err = svc.AuthenticateRequest(ctx, replacementRaw, "10.20.30.40"); err != nil {
		t.Fatalf("replacement failed: %v", err)
	}
}

func TestRejectsInvalidRestrictions(t *testing.T) {
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	_, _ = s.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u3','carol','carol','')`)
	svc := New(s, strings.Repeat("x", 32))
	if _, _, err := svc.CreateWithRestrictions(ctx, "u3", "bad", nil, nil, Restrictions{AllowedCIDRs: []string{"not-cidr"}}); err == nil {
		t.Fatal("invalid CIDR accepted")
	}
	if _, _, err := svc.CreateWithRestrictions(ctx, "u3", "bad", nil, nil, Restrictions{AllowedRepositories: []string{"repo"}}); err == nil {
		t.Fatal("invalid repository accepted")
	}
}

// Recording "last used" on every request put a database write in front of every
// authenticated call, which on SQLite serialises a burst of tool calls behind
// itself for a timestamp operators read as a date.
func TestLastUsedWriteIsCoalesced(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:apikey-touch?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if _, err = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','alice','alice','')`); err != nil {
		t.Fatal(err)
	}
	service := New(db, strings.Repeat("p", 32))
	clock := time.Now().UTC()
	service.now = func() time.Time { return clock }
	_, secret, err := service.Create(ctx, "u1", "agent", []string{"search-code"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	read := func() time.Time {
		t.Helper()
		var stamp sql.NullTime
		if err := db.DB.QueryRow(`SELECT last_used_at FROM api_keys WHERE user_id='u1'`).Scan(&stamp); err != nil {
			t.Fatal(err)
		}
		return stamp.Time.UTC()
	}

	for index := 0; index < 20; index++ {
		if _, err = service.AuthenticateRequest(ctx, secret, "10.0.0.1"); err != nil {
			t.Fatalf("call %d: %v", index, err)
		}
	}
	first := read()
	if first.IsZero() {
		t.Fatal("the first use must be recorded")
	}
	// Twenty calls inside the window wrote once: the stored value is still the
	// first one even though the clock moved.
	clock = clock.Add(30 * time.Second)
	if _, err = service.AuthenticateRequest(ctx, secret, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if again := read(); !again.Equal(first) {
		t.Fatalf("a write happened inside the coalescing window: %s then %s", first, again)
	}

	// Past the window the timestamp moves again, so "last used" stays useful.
	clock = clock.Add(2 * lastUsedInterval)
	if _, err = service.AuthenticateRequest(ctx, secret, "10.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if later := read(); !later.After(first) {
		t.Fatalf("the timestamp stopped advancing: %s then %s", first, later)
	}
}
