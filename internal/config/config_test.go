package config

import "testing"

func TestDefaultPortIs4747(t *testing.T) {
	t.Setenv("GIT_CTX_DB_DRIVER", "sqlite")
	t.Setenv("GIT_CTX_DB_DSN", "file::memory:")
	t.Setenv("GIT_CTX_API_KEY_PEPPER", "01234567890123456789012345678901")
	t.Setenv("GIT_CTX_MASTER_KEY", "0123456789abcdef0123456789abcdef")
	t.Setenv("GIT_CTX_LISTEN", "")
	t.Setenv("GIT_CTX_PUBLIC_URL", "")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ListenAddress != ":4747" || cfg.PublicURL != "http://localhost:4747" {
		t.Fatalf("unexpected defaults: listen=%q publicURL=%q", cfg.ListenAddress, cfg.PublicURL)
	}
	if cfg.BackupDirectory != "backups" {
		t.Fatalf("unexpected backup directory: %q", cfg.BackupDirectory)
	}
}
