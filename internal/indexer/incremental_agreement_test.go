package indexer

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"testing"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

func snapshotOf(t *testing.T, s *store.Store, table, columns string) []string {
	t.Helper()
	rows, err := s.DB.Query(`SELECT ` + columns + ` FROM ` + table + ` WHERE repository_id='bitbucket:9'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	names, _ := rows.Columns()
	var out []string
	for rows.Next() {
		cells := make([]any, len(names))
		holders := make([]sql_null, len(names))
		for i := range cells {
			cells[i] = &holders[i]
		}
		if err := rows.Scan(cells...); err != nil {
			t.Fatal(err)
		}
		parts := make([]string, len(names))
		for i := range holders {
			parts[i] = names[i] + "=" + holders[i].String()
		}
		out = append(out, strings.Join(parts, " "))
	}
	sort.Strings(out)
	return out
}

type sql_null struct{ v any }

func (n *sql_null) Scan(src any) error { n.v = src; return nil }
func (n sql_null) String() string {
	switch value := n.v.(type) {
	case nil:
		return "<nil>"
	case []byte:
		return fmt.Sprintf("%x", value)
	default:
		return fmt.Sprint(value)
	}
}

func indexTo(t *testing.T, name string, steps []struct {
	files   map[string]string
	changes []source.Change
	commit  string
}) *store.Store {
	t.Helper()
	ctx := context.Background()
	s, err := store.Open(ctx, "sqlite", "file:"+name+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.DB.Close() })
	src := &incrementalSource{}
	idx := New(s, DefaultPolicy())
	repo := source.Repository{ID: 9, ProjectKey: "KCB", Slug: "incremental", Name: "Incremental", DefaultBranch: "main"}
	for _, step := range steps {
		src.files, src.changes = step.files, step.changes
		if err = idx.SyncRepository(ctx, src, "bitbucket", repo, []source.Reference{{Name: "main", LatestCommit: step.commit}}); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// An incremental pass and a full index of the same tree must leave the same
// database. Each has its own tests for the things it does; nothing required the
// two to agree, and they did not.
//
// What they disagreed about was repository_files.commit_id. An incremental pass
// is handed only the files that changed, so every other row kept the commit it
// was first seen at, while the chunks of those same files were restamped to the
// new one. That row is what read-file cites — so one database answered "@c1" for
// a path through read-file and "@c2" for the same path through search-code, for
// a file whose content had not moved at all.
//
// The comparison is over every table indexing writes, and it fails if a table is
// empty in both databases: agreeing about nothing is not agreement.
func TestAnIncrementalPassLeavesWhatAFullIndexWouldHave(t *testing.T) {
	type step = struct {
		files   map[string]string
		changes []source.Change
		commit  string
	}
	service := "package svc\n\nimport (\n\t\"errors\"\n\t\"example.com/lib/client\"\n)\n\n" +
		"// Alpha starts the service.\nfunc Alpha() error { return client.Dial() }\n\n" +
		"// Beta stops it.\nfunc Beta() error { return errors.New(\"stopped\") }\n\n" +
		"type Runner struct{ name string }\n\nfunc (r Runner) Run() error { return Alpha() }\n"
	trimmed := "package svc\n\nimport \"example.com/lib/client\"\n\n" +
		"// Alpha starts the service.\nfunc Alpha() error { return client.Dial() }\n\n" +
		"type Runner struct{ name string }\n\nfunc (r Runner) Run() error { return Alpha() }\n"
	helper := "package svc\n\nimport \"strings\"\n\nfunc Trim(v string) string { return strings.TrimSpace(v) }\n"

	first := map[string]string{
		"docs/a.md":     "# One\nkeep\n# Two\nold",
		"docs/b.md":     "# Gone\nobsolete",
		"docs/stays.md": "# Untouched\nnothing changes here",
		"service.go":    service,
		"helper.go":     helper,
		"go.mod":        "module demo\n\nrequire (\n\texample.com/lib v1.2.3\n\texample.com/other v0.1.0\n)\n",
		"package.json":  "{\"name\":\"demo\",\"dependencies\":{\"left-pad\":\"1.0.0\"}}",
	}
	second := map[string]string{
		"docs/a.md":     "# One\nkeep\n# Two\nnew",
		"docs/moved.md": "# Untouched\nnothing changes here",
		"docs/stays.md": "# Untouched\nnothing changes here",
		"service.go":    trimmed,
		"go.mod":        "module demo\n\nrequire example.com/lib v2.0.0\n",
	}
	// b.md deleted, stays.md copied to moved.md, service.go trimmed (Beta and an
	// import removed), helper.go deleted, go.mod loses a dependency, and the npm
	// manifest is deleted outright.
	changes := []source.Change{
		{Path: "docs/a.md", OldPath: "docs/a.md", Type: "modified"},
		{Path: "docs/b.md", OldPath: "docs/b.md", Type: "deleted"},
		{Path: "docs/moved.md", OldPath: "docs/moved.md", Type: "added"},
		{Path: "service.go", OldPath: "service.go", Type: "modified"},
		{Path: "helper.go", OldPath: "helper.go", Type: "deleted"},
		{Path: "go.mod", OldPath: "go.mod", Type: "modified"},
		{Path: "package.json", OldPath: "package.json", Type: "deleted"},
	}
	incremental := indexTo(t, "incr-a", []step{{first, nil, "c1"}, {second, changes, "c2"}})
	full := indexTo(t, "incr-b", []step{{second, nil, "c2"}})

	// The two tables that describe the same file must not name different commits:
	// they are cited by different tools, and an agent comparing citations is
	// entitled to assume one platform saw one snapshot.
	rows, err := incremental.DB.Query(`SELECT f.path, f.commit_id, COALESCE(MIN(c.commit_id),'-') FROM repository_files f
LEFT JOIN document_chunks c ON c.repository_id=f.repository_id AND c.ref_name=f.ref_name AND c.file_path=f.path
WHERE f.repository_id='bitbucket:9' GROUP BY f.path, f.commit_id ORDER BY f.path`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	compared := 0
	for rows.Next() {
		var path, fileCommit, chunkCommit string
		if err = rows.Scan(&path, &fileCommit, &chunkCommit); err != nil {
			t.Fatal(err)
		}
		if chunkCommit == "-" {
			continue
		}
		compared++
		if fileCommit != chunkCommit {
			t.Errorf("%s is cited as @%s by read-file and @%s by search-code", path, fileCommit, chunkCommit)
		}
	}
	if compared < 3 {
		t.Fatalf("only %d files had both a listing row and chunks, so the comparison proves little", compared)
	}

	for _, table := range []struct{ name, columns string }{
		{"document_chunks", "file_path,line_start,line_end,heading,content_type,content,content_hash,commit_id"},
		{"code_symbols", "file_path,name,qualified_name,symbol_kind,language,signature,documentation,line_start,line_end,content_hash,commit_id"},
		{"code_dependencies", "file_path,from_symbol,target,dependency_kind,line_number,commit_id"},
		{"repository_files", "ref_name,path,base_name,size_bytes,content_indexed,commit_id"},
		{"repository_packages", "ref_name,ecosystem,name,version,scope,manifest_path,commit_id"},
		{"repository_maps", "ref_name,commit_id"},
		{"repository_ref_states", "ref_name,commit_id,embedding_revision"},
	} {
		a := snapshotOf(t, incremental, table.name, table.columns)
		b := snapshotOf(t, full, table.name, table.columns)
		if len(a) == 0 && len(b) == 0 {
			t.Errorf("%s holds nothing in either database, so comparing them proves nothing", table.name)
			continue
		}
		if strings.Join(a, "\n") == strings.Join(b, "\n") {
			continue
		}
		t.Errorf("%s DIFFERS: incremental=%d rows, full=%d rows", table.name, len(a), len(b))
		onlyA, onlyB := diff(a, b), diff(b, a)
		for _, row := range onlyA {
			t.Logf("  only after incremental: %s", clip(row))
		}
		for _, row := range onlyB {
			t.Logf("  only after full index : %s", clip(row))
		}
	}
}

func diff(a, b []string) []string {
	in := map[string]bool{}
	for _, row := range b {
		in[row] = true
	}
	var out []string
	for _, row := range a {
		if !in[row] {
			out = append(out, row)
		}
	}
	return out
}

func clip(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
