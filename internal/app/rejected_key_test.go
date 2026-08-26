package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"git-ctx/internal/apikey"
	"git-ctx/internal/config"
)

// A rejected API key used to leave no trace at all — not an audit row, not a
// notification, nothing. An operator asking "is somebody probing our MCP
// endpoint with keys we never issued" had no way to answer, on a platform whose
// premise is that every access is accounted for.
//
// Writing a row per attempt is why it was left out: an unthrottled endpoint
// floods the audit log, which is the same problem pointing the other way. The
// rows are spaced by count instead — the first rejection from an address, then
// the tenth, the hundredth — so one probe is visible immediately and a flood
// costs a handful of rows, each saying how many attempts it stands for.
func TestARejectedKeyLeavesATraceWithoutFloodingTheAudit(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:rejected-keys?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	probe := func(key string) int {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		request.Header.Set("CONTEXT7_API_KEY", key)
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder.Code
	}
	rows := func() (count int) {
		if err := a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='apikey.auth' AND outcome='failure'`).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}

	if status := probe("bctx_live_AAAAAA_forged"); status != http.StatusUnauthorized {
		t.Fatalf("a forged key must be refused: %d", status)
	}
	if got := rows(); got != 1 {
		t.Fatalf("the first rejection left %d audit rows, want 1", got)
	}

	// The row has to name the key an operator would look up — the same prefix the
	// api_keys table stores — and must not carry the secret half of the value.
	var resource, metadata string
	if err = a.store.DB.QueryRow(`SELECT resource_id,metadata FROM audit_logs WHERE action='apikey.auth' ORDER BY id LIMIT 1`).Scan(&resource, &metadata); err != nil {
		t.Fatal(err)
	}
	if resource != "AAAAAA" {
		t.Errorf("the rejection does not name the key prefix: %q", resource)
	}
	if strings.Contains(metadata, "forged") || strings.Contains(resource, "forged") {
		t.Errorf("the secret half of the key was recorded: resource=%q metadata=%q", resource, metadata)
	}
	if !strings.Contains(metadata, "attemptsInLastHour") {
		t.Errorf("the row does not say how many attempts it stands for: %s", metadata)
	}

	// Now the flood. Nine more attempts reach ten, which is the next row; the
	// ninety after that reach a hundred, which is the row after it.
	for i := 0; i < 99; i++ {
		probe("bctx_live_BBBBBB_forged")
	}
	if got := rows(); got != 3 {
		t.Fatalf("100 rejections produced %d audit rows; the spacing is not holding", got)
	}
	var latest string
	if err = a.store.DB.QueryRow(`SELECT metadata FROM audit_logs WHERE action='apikey.auth' ORDER BY id DESC LIMIT 1`).Scan(&latest); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(latest, "100") {
		t.Errorf("the last row does not carry the running count: %s", latest)
	}

	// A value that is not a key at all never reaches the API-key path, so it
	// cannot be used to write rows either.
	before := rows()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	request.Header.Set("Authorization", "Bearer not-a-key")
	a.Handler().ServeHTTP(httptest.NewRecorder(), request)
	if rows() != before {
		t.Error("a bearer token that is not an API key was recorded as a rejected key")
	}
}

// A caller told only "rate limit exceeded" has one move: try again, now. The
// reset is in a Retry-After header the caller may never surface to whoever is
// reading the failure, so the body says it too.
func TestARateLimitedCallIsToldWhenTheWindowResets(t *testing.T) {
	a, err := New(context.Background(), config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:rate-limit-message?mode=memory&cache=shared&_foreign_keys=on",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap", PublicURL: "http://localhost:4747",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	if _, err = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u1','u1','agent','','active')`); err != nil {
		t.Fatal(err)
	}
	_, secret, err := a.keys.CreateWithRestrictions(context.Background(), "u1", "cli", []string{"search-repositories"}, nil,
		apikey.Restrictions{RatePerMinute: 1})
	if err != nil {
		t.Fatal(err)
	}
	call := func() *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
		request.Header.Set("CONTEXT7_API_KEY", secret)
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}
	if first := call(); first.Code != http.StatusOK {
		t.Fatalf("the first call within the limit was refused: %d %s", first.Code, first.Body.String())
	}
	second := call()
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("the call past the limit was allowed: %d", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After header")
	}
	if !strings.Contains(second.Body.String(), "resets in") {
		t.Errorf("the body does not say when the window resets: %s", second.Body.String())
	}

	// Being over the quota is not a rejected credential; it must not be recorded
	// as one, or a busy agent looks like an intruder.
	var rejected int
	if err = a.store.DB.QueryRow(`SELECT COUNT(*) FROM audit_logs WHERE action='apikey.auth' AND outcome='failure'`).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if rejected != 0 {
		t.Errorf("a rate-limited call was audited as a rejected key %d time(s)", rejected)
	}
}
