package config

import "testing"

func TestDefaultPortIs4747(t *testing.T) {
	t.Setenv("GIT_CTX_DB_DSN", "file::memory:")
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
	if cfg.DatabaseDriver != "sqlite" || len(cfg.MasterKey) != 32 || len(cfg.KeyPepper) != 32 {
		t.Fatalf("unexpected derived config")
	}
}

func TestOnlyDSNControlsBootstrap(t *testing.T) {
	t.Setenv("GIT_CTX_DB_DSN", "postgres://gitctx:secret@db/gitctx")
	t.Setenv("GIT_CTX_MASTER_KEY", "ignored")
	t.Setenv("GIT_CTX_API_KEY_PEPPER", "ignored")
	t.Setenv("GIT_CTX_LISTEN", ":9999")
	cfg, err := FromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseDriver != "postgres" || cfg.ListenAddress != ":4747" || len(cfg.MasterKey) != 32 || len(cfg.KeyPepper) != 32 {
		t.Fatalf("unexpected config")
	}
}

func TestDSNRequired(t *testing.T) {
	t.Setenv("GIT_CTX_DB_DSN", "")
	if _, err := FromEnv(); err == nil {
		t.Fatal("missing DSN accepted")
	}
}
