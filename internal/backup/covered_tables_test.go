package backup

import (
	"context"
	"sort"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

// excludedFromBackup lists every table a backup deliberately leaves behind, and
// why. A table is either carried or it is named here; there is no third
// category, because the third category is what happened to
// context_pack_entrypoints — added to the schema in migration 040, never added
// to the archive, so a backup carried a context pack and its items and left the
// symbols an operator had typed in behind. The restore reported success.
var excludedFromBackup = map[string]string{
	"schema_migrations": "the restoring installation's own migration history; carrying it would overwrite the state of the database being restored into",

	"document_chunks_staging":     "a generation being built; it is discarded when indexing ends either way",
	"code_symbols_staging":        "as above",
	"code_dependencies_staging":   "as above",
	"repository_packages_staging": "as above",
	"index_maintenance":           "bookkeeping for the current process's maintenance pass",

	"document_chunks_fts":         "SQLite's external-content FTS shadow tables, rebuilt from document_chunks",
	"document_chunks_fts_data":    "as above",
	"document_chunks_fts_idx":     "as above",
	"document_chunks_fts_docsize": "as above",
	"document_chunks_fts_config":  "as above",

	"user_sessions":         "a restore ends every active session on purpose, which is what the console tells the operator",
	"mcp_sessions":          "as above",
	"auth_flows":            "a login in progress cannot outlive the restore that interrupts it",
	"admin_recovery_tokens": "single-use and short-lived; carrying one would extend a recovery window past the event it was issued for",
	"backup_records":        "the list of archives held by the installation doing the restoring, not by the one that was captured",
}

// Every table in the schema is either backed up or excluded above. A table that
// is neither is data an operator will lose in a disaster and be told nothing
// about.
func TestEveryTableIsBackedUpOrDeliberatelyExcluded(t *testing.T) {
	db, err := store.Open(context.Background(), "sqlite", "file:backup-coverage?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	rows, err := db.DB.Query(`SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var live []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		live = append(live, name)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(live) < 40 {
		t.Fatalf("the schema walk found only %d tables, so it is not reading the database", len(live))
	}

	carried := map[string]bool{}
	for _, name := range tables {
		carried[name] = true
	}
	for _, name := range live {
		if !carried[name] && excludedFromBackup[name] == "" {
			t.Errorf("%s is in the schema, is not in the archive, and no reason is recorded for leaving it out", name)
		}
	}

	// The reverse: a name in the archive list that no longer exists would make
	// every backup fail at the first read.
	present := map[string]bool{}
	for _, name := range live {
		present[name] = true
	}
	var phantom []string
	for _, name := range tables {
		if !present[name] {
			phantom = append(phantom, name)
		}
	}
	sort.Strings(phantom)
	if len(phantom) > 0 {
		t.Errorf("the archive lists tables the schema does not have: %s", strings.Join(phantom, ", "))
	}

	// And an exclusion for a table that is gone is a comment nobody will check
	// against reality again. The FTS shadow tables are the one exception: they
	// exist only in a build carrying the sqlite_fts5 tag, so their absence says
	// which binary is running, not that the exclusion is stale.
	for name := range excludedFromBackup {
		if !present[name] && !strings.HasPrefix(name, "document_chunks_fts") {
			t.Errorf("%s is excluded from the archive but no longer exists", name)
		}
	}
}
