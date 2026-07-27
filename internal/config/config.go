package config

import (
	"errors"
	"os"
	"strings"
)

type Config struct {
	ListenAddress   string
	DatabaseDriver  string
	DatabaseDSN     string
	KeyPepper       string
	MasterKey       string
	BootstrapAdmin  string
	PublicURL       string
	BackupDirectory string
}

func FromEnv() (Config, error) {
	c := Config{
		ListenAddress:   env("GIT_CTX_LISTEN", ":4747"),
		DatabaseDriver:  strings.ToLower(env("GIT_CTX_DB_DRIVER", "postgres")),
		DatabaseDSN:     os.Getenv("GIT_CTX_DB_DSN"),
		KeyPepper:       os.Getenv("GIT_CTX_API_KEY_PEPPER"),
		MasterKey:       os.Getenv("GIT_CTX_MASTER_KEY"),
		BootstrapAdmin:  os.Getenv("GIT_CTX_BOOTSTRAP_ADMIN"),
		PublicURL:       env("GIT_CTX_PUBLIC_URL", "http://localhost:4747"),
		BackupDirectory: env("GIT_CTX_BACKUP_DIR", "backups"),
	}
	if c.DatabaseDSN == "" {
		if c.DatabaseDriver == "sqlite" {
			c.DatabaseDSN = "file:git-ctx.db?_foreign_keys=on&_busy_timeout=5000"
		} else {
			return Config{}, errors.New("GIT_CTX_DB_DSN is required for postgres")
		}
	}
	if c.DatabaseDriver != "postgres" && c.DatabaseDriver != "sqlite" {
		return Config{}, errors.New("GIT_CTX_DB_DRIVER must be postgres or sqlite")
	}
	if len(c.KeyPepper) < 32 {
		return Config{}, errors.New("GIT_CTX_API_KEY_PEPPER must contain at least 32 characters")
	}
	if len(c.MasterKey) != 32 {
		return Config{}, errors.New("GIT_CTX_MASTER_KEY must be exactly 32 characters")
	}
	return c, nil
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
