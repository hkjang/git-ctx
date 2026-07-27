package secret

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"git-ctx/internal/store"
)

func TestDatabaseSecretRotationResolutionAndDisable(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:managed-secret?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	seal := func(raw []byte) ([]byte, error) { return append([]byte("sealed:"), raw...), nil }
	open := func(raw []byte) ([]byte, error) {
		if !bytes.HasPrefix(raw, []byte("sealed:")) {
			return nil, errors.New("invalid ciphertext")
		}
		return bytes.TrimPrefix(raw, []byte("sealed:")), nil
	}
	service := New(db, seal, open, nil)
	first, err := service.Put(ctx, "bitbucket-pat", "database", "first-value", "admin", "create")
	if err != nil || first.Version != 1 {
		t.Fatalf("first=%#v err=%v", first, err)
	}
	second, err := service.Put(ctx, "bitbucket-pat", "database", "rotated-value", "admin", "rotate")
	if err != nil || second.Version != 2 {
		t.Fatalf("second=%#v err=%v", second, err)
	}
	resolved, err := service.Resolve(ctx, map[string]any{"pat": "secret://bitbucket-pat", "nested": []any{map[string]any{"token": "secret://bitbucket-pat"}}})
	if err != nil || resolved["pat"] != "rotated-value" {
		t.Fatalf("resolved=%#v err=%v", resolved, err)
	}
	var current []byte
	var versions int
	_ = db.DB.QueryRow(`SELECT value_encrypted FROM managed_secrets WHERE name='bitbucket-pat'`).Scan(&current)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM managed_secret_versions WHERE name='bitbucket-pat'`).Scan(&versions)
	if bytes.Equal(current, []byte("rotated-value")) || !bytes.HasPrefix(current, []byte("sealed:")) || versions != 2 {
		t.Fatalf("ciphertext/version failure value=%q versions=%d", current, versions)
	}
	if err = service.Disable(ctx, "bitbucket-pat", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Get(ctx, "bitbucket-pat"); err == nil {
		t.Fatal("disabled secret remained readable")
	}
}

func TestSecretValidation(t *testing.T) {
	db, err := store.Open(context.Background(), "sqlite", "file:secret-validation?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	service := New(db, func(raw []byte) ([]byte, error) { return raw, nil }, func(raw []byte) ([]byte, error) { return raw, nil }, nil)
	if _, err = service.Put(context.Background(), "../escape", "database", "value", "admin", ""); err == nil {
		t.Fatal("unsafe secret name accepted")
	}
	if _, err = service.Put(context.Background(), "safe", "vault", "value", "admin", ""); err == nil {
		t.Fatal("unconfigured Vault backend accepted")
	}
}
