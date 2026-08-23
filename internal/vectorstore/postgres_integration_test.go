package vectorstore

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"git-ctx/internal/testsupport"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// This contract starts with the pgvector server package available but does not
// require CREATE EXTENSION to have been run in the target database. It catches
// the settings-save regression where Status rejected the configuration before
// the connection test had a chance to activate the extension.
func TestPgvectorConnectionActivatesAvailableExtensionIntegration(t *testing.T) {
	base := os.Getenv("GIT_CTX_TEST_PGVECTOR_DSN")
	if reason := testsupport.SkipReason("GIT_CTX_TEST_PGVECTOR_DSN", base); reason != "" {
		t.Skip(reason)
	}
	dsn, dropDatabase, err := testsupport.NewPostgresDatabase(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dropDatabase)
	const table = "git_ctx_vector_connection_test"
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Cleanup(func() {
		_, _ = db.Exec(`DROP TABLE IF EXISTS ` + table)
	})

	cfg := FromMap(map[string]any{
		"provider": "pgvector", "dsn": dsn, "collection": table,
		"dimensions": float64(3), "timeoutSeconds": float64(10),
	})
	status, err := TestConnection(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	if !status.Ready || status.Provider != "pgvector" || status.ExtensionVersion == "" || status.ExtensionSchema == "" {
		t.Fatalf("unexpected status: %#v", status)
	}
	store, err := Open(cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err = store.Upsert(context.Background(), []Chunk{
		{ID: "closest", RepositoryID: "repo", Ref: "main", Revision: "model-v2", Vector: []float32{1, 0, 0}},
		{ID: "far", RepositoryID: "repo", Ref: "main", Revision: "model-v2", Vector: []float32{0, 1, 0}},
		{ID: "stale", RepositoryID: "repo", Ref: "main", Revision: "model-v1", Vector: []float32{1, 0, 0}},
	}); err != nil {
		t.Fatal(err)
	}
	matches, err := store.Search(context.Background(), "repo", "main", "model-v2", []float32{1, 0, 0}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 || matches[0].ID != "closest" || !strings.Contains(status.Target, "PostgreSQL") {
		t.Fatalf("matches=%#v status=%#v", matches, status)
	}
}
