package secret

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVaultKVV2Contract(t *testing.T) {
	requests := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests[r.Method+" "+r.URL.Path] = string(body)
		if r.Header.Get("X-Vault-Token") != "test-token" || r.Header.Get("X-Vault-Namespace") != "company/platform" {
			http.Error(w, "authentication missing", http.StatusForbidden)
			return
		}
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/auth/token/lookup-self":
			io.WriteString(w, `{"data":{"policies":["git-ctx"]}}`)
		case "POST /v1/secret/data/git-ctx/model-key":
			io.WriteString(w, `{"data":{"version":3}}`)
		case "GET /v1/secret/data/git-ctx/model-key":
			io.WriteString(w, `{"data":{"data":{"value":"resolved-key"},"metadata":{"version":3}}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	vault, err := NewVault(VaultConfig{BaseURL: server.URL, Token: "test-token", Namespace: "company/platform"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err = vault.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := vault.Put(ctx, "model-key", "secret-value")
	if err != nil || version != 3 {
		t.Fatalf("version=%d err=%v", version, err)
	}
	value, version, err := vault.Get(ctx, "model-key")
	if err != nil || value != "resolved-key" || version != 3 {
		t.Fatalf("value=%q version=%d err=%v", value, version, err)
	}
	if body := requests["POST /v1/secret/data/git-ctx/model-key"]; !strings.Contains(body, `"data":{"value":"secret-value"}`) {
		t.Fatalf("unexpected KV v2 body: %s", body)
	}
}

func TestVaultRejectsUnsafePaths(t *testing.T) {
	if _, err := NewVault(VaultConfig{BaseURL: "https://vault.example", Token: "x", Mount: "../secret"}); err == nil {
		t.Fatal("unsafe mount accepted")
	}
}
