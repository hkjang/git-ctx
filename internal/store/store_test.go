package store

import (
	"context"
	"path/filepath"
	"testing"

	"git-ctx/internal/toolcatalog"
)

func TestMCPToolPolicyCatalogCoversGrantableScopes(t *testing.T) {
	s, err := Open(context.Background(), "sqlite", "file:mcp-policy-catalog?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()

	rows, err := s.DB.Query(`SELECT name FROM mcp_tools`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	configured := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		configured[name] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	for _, name := range toolcatalog.Names() {
		if !configured[name] {
			t.Errorf("MCP tool %q can be granted to a key but has no administrator policy row", name)
		}
	}
}

func TestRepositoryIdentityIncludesSourceType(t *testing.T) {
	s, err := Open(context.Background(), "sqlite", "file::memory:?cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	insert := `INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id) VALUES(?,?,?,?,?,?,?)`
	if _, err = s.DB.Exec(insert, "bitbucket:1", "TEAM", "demo", "Demo", "bitbucket", "1", "/team/demo"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(insert, "gitlab:1", "TEAM", "demo", "Demo", "gitlab", "1", "/gitlab~team/demo"); err != nil {
		t.Fatalf("same project and slug from another source must be accepted: %v", err)
	}
}

func TestLegacyRepositoryUniquenessMigration(t *testing.T) {
	ctx := context.Background()
	dsn := "file:" + filepath.Join(t.TempDir(), "legacy.db") + "?_foreign_keys=on"
	s, err := Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`DELETE FROM schema_migrations WHERE version='020_repository_source_uniqueness.sql'`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DB.Exec(`PRAGMA foreign_keys=OFF`); err != nil {
		t.Fatal(err)
	}
	legacy := `
CREATE TABLE repositories_legacy (
  id TEXT PRIMARY KEY, project_key TEXT NOT NULL, slug TEXT NOT NULL,
  name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '',
  source_type TEXT NOT NULL DEFAULT 'bitbucket', source_external_id TEXT NOT NULL DEFAULT '',
  library_id TEXT NOT NULL UNIQUE, default_branch TEXT NOT NULL DEFAULT 'main',
  reputation TEXT NOT NULL DEFAULT 'Medium', enabled INTEGER NOT NULL DEFAULT 1,
  indexed_at TIMESTAMP, UNIQUE(project_key,slug)
);
INSERT INTO repositories_legacy SELECT * FROM repositories;
DROP TABLE repositories;
ALTER TABLE repositories_legacy RENAME TO repositories;`
	if _, err = s.DB.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	s.DB.Close()
	s, err = Open(ctx, "sqlite", dsn)
	if err != nil {
		t.Fatalf("upgrade legacy schema: %v", err)
	}
	defer s.DB.Close()
	insert := `INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id) VALUES(?,?,?,?,?,?,?)`
	_, _ = s.DB.Exec(insert, "bitbucket:1", "TEAM", "demo", "Demo", "bitbucket", "1", "/team/demo")
	if _, err = s.DB.Exec(insert, "gitlab:1", "TEAM", "demo", "Demo", "gitlab", "1", "/gitlab~team/demo"); err != nil {
		t.Fatalf("migrated uniqueness still conflicts: %v", err)
	}
}
