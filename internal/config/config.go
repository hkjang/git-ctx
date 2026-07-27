package config

import (
	"crypto/sha256"
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
	dsn := strings.TrimSpace(os.Getenv("GIT_CTX_DB_DSN"))
	if dsn == "" {
		return Config{}, errors.New("GIT_CTX_DB_DSN is required")
	}
	driver := "postgres"
	if strings.HasPrefix(dsn, "file:") || dsn == ":memory:" {
		driver = "sqlite"
	}
	master := sha256.Sum256([]byte("git-ctx/settings/v1\x00" + dsn))
	pepper := sha256.Sum256([]byte("git-ctx/api-keys/v1\x00" + dsn))
	c := Config{
		ListenAddress:   ":4747",
		DatabaseDriver:  driver,
		DatabaseDSN:     dsn,
		KeyPepper:       string(pepper[:]),
		MasterKey:       string(master[:]),
		PublicURL:       "http://localhost:4747",
		BackupDirectory: "backups",
	}
	return c, nil
}
