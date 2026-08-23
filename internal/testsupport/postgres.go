// Package testsupport provisions throwaway PostgreSQL databases for the
// integration tests.
//
// Those tests used to share the one database named by the environment. go test
// runs packages in parallel, so they deleted each other's rows mid-run, and
// several left their fixtures behind so a second run failed on a duplicate key.
// Between the two the suite was effectively unrunnable, which is why it had
// never been run. Each test now gets a database of its own and drops it again.
package testsupport

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// NewPostgresDatabase creates an empty database on the server the base DSN
// points at and returns a DSN for it together with a function that drops it.
// The returned cleanup is safe to call even when the caller failed part way.
func NewPostgresDatabase(ctx context.Context, baseDSN string) (dsn string, cleanup func(), err error) {
	parsed, err := url.Parse(baseDSN)
	if err != nil {
		return "", nil, fmt.Errorf("parse test DSN: %w", err)
	}
	suffix := make([]byte, 6)
	if _, err = rand.Read(suffix); err != nil {
		return "", nil, err
	}
	name := "git_ctx_test_" + hex.EncodeToString(suffix)

	admin, err := sql.Open("pgx", baseDSN)
	if err != nil {
		return "", nil, err
	}
	defer admin.Close()
	// The name is generated here from hex, never from caller input, so it cannot
	// carry anything an identifier would have to escape.
	if _, err = admin.ExecContext(ctx, `CREATE DATABASE `+name); err != nil {
		return "", nil, fmt.Errorf("create test database: %w", err)
	}

	target := *parsed
	target.Path = "/" + name
	dropped := false
	cleanup = func() {
		if dropped {
			return
		}
		dropped = true
		cleaner, openErr := sql.Open("pgx", baseDSN)
		if openErr != nil {
			return
		}
		defer cleaner.Close()
		// FORCE disconnects anything the test left open, so a leaked connection
		// cannot keep the database alive after the run.
		_, _ = cleaner.ExecContext(context.WithoutCancel(ctx), `DROP DATABASE IF EXISTS `+name+` WITH (FORCE)`)
	}
	return target.String(), cleanup, nil
}

// SkipReason explains why an integration test cannot run, or is empty when it
// can. Keeping the message in one place stops each test inventing its own.
func SkipReason(envKey, dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return envKey + " is not set"
	}
	return ""
}
