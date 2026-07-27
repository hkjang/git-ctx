package secret

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestRealVaultKVV2RoundTrip(t *testing.T) {
	baseURL, token := os.Getenv("GIT_CTX_TEST_VAULT_URL"), os.Getenv("GIT_CTX_TEST_VAULT_TOKEN")
	if baseURL == "" || token == "" {
		t.Skip("GIT_CTX_TEST_VAULT_URL and GIT_CTX_TEST_VAULT_TOKEN are not set")
	}
	vault, err := NewVault(VaultConfig{BaseURL: baseURL, Token: token, Mount: "secret", Prefix: "git-ctx-integration"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err = vault.Validate(ctx); err != nil {
		t.Fatal(err)
	}
	name := "roundtrip-" + time.Now().UTC().Format("20060102150405")
	writtenVersion, err := vault.Put(ctx, name, "integration-secret")
	if err != nil {
		t.Fatal(err)
	}
	value, readVersion, err := vault.Get(ctx, name)
	if err != nil || value != "integration-secret" || readVersion != writtenVersion {
		t.Fatalf("value=%q written=%d read=%d err=%v", value, writtenVersion, readVersion, err)
	}
}
