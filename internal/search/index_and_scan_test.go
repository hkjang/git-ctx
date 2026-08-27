package search

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"

	"git-ctx/internal/embedding"
	"git-ctx/internal/store"
)

// Two routes answer the same question: the full-text index, and the scan behind
// it. They are not meant to return the same rows — the scan also matches inside
// a word, which is how "invoice" finds settleInvoice — but the index must never
// find something the scan misses, and it must not come back empty for a term the
// scan answers easily. The second is what happened to every hyphenated name in
// the catalogue: the builder deleted the hyphen and asked for a word no document
// contains, and only the capped scan behind it kept those searches working.
func TestTheIndexFindsWhatTheScanFindsForTheShapesACatalogueHolds(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:index-and-scan?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	if !db.FullTextAvailable() {
		t.Skip("this build has no full-text index")
	}
	exec := func(q string, a ...any) {
		if _, err := db.DB.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r1','KCB','alpha','Alpha','bitbucket','1','/kcb/alpha','main',1)`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','read')`)
	for _, d := range []struct{ id, path, heading, content string }{
		{"c1", "docs/gpu.md", "GPU metrics", "Use the dcgm-exporter DaemonSet for Pod GPU metrics."},
		{"c2", "internal/search/service.go", "Service.SearchCode", "func (s *Service) SearchCode(ctx context.Context) error { return nil }"},
		{"c3", "config/values.yaml", "values", "replicaCount: 3\nimage: registry.company/git-ctx:latest\n"},
		{"c4", "docs/한글.md", "설치 안내", "설치 후 색인이 완료될 때까지 기다립니다."},
		{"c5", "package.json", "dependencies", `{"dependencies":{"left-pad":"1.0.0","spring-boot-starter":"2.7.0"}}`},
		{"c6", "Makefile", "build", "build:\n\tgo build -tags sqlite_fts5 -o bin/git-ctx ./cmd/git-ctx\n"},
	} {
		exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'r1','main','abc',?,1,10,?,'document',?,?)`,
			d.id, d.path, d.heading, d.content, d.id)
	}
	s := New(db)
	principals := []string{"alice"}

	answered := 0
	for _, query := range []string{
		"dcgm-exporter", "git-ctx", "left-pad", "spring-boot-starter", "sqlite_fts5",
		"values.yaml", "api/v1/admin", "SearchCode", "설치", "GPU metrics",
	} {
		terms := unique(embedding.Tokens(query))
		indexed, indexedArgs, ok := s.fullTextCandidates(terms, "", "", "", "", principals)
		if !ok {
			continue
		}
		fromIndex := candidateKeys(t, s, indexed, indexedArgs)
		scanned, scanArgs := s.scanCandidates(terms, "", "", "", "", principals)
		fromScan := candidateKeys(t, s, scanned, scanArgs)

		for _, row := range fromIndex {
			if !slices.Contains(fromScan, row) {
				t.Errorf("%q: the index returned %s and the scan behind it does not", query, row)
			}
		}
		if len(fromScan) > 0 && len(fromIndex) == 0 {
			t.Errorf("%q: the scan found %d chunk(s) and the index found none, so this search never uses the index", query, len(fromScan))
		}
		if len(fromScan) > 0 {
			answered++
		}
	}
	if answered < 8 {
		t.Fatalf("only %d of the queries matched anything, so comparing the two routes proves little", answered)
	}
}

func candidateKeys(t *testing.T, s *Service, statement string, args []any) []string {
	t.Helper()
	rows, err := s.store.DB.QueryContext(context.Background(), s.store.Rebind(statement), args...)
	if err != nil {
		t.Fatalf("candidate query failed: %v", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var lib, sourceType, project, slug, ref, commit, path, heading, content string
		var start, end int
		if err := rows.Scan(&lib, &sourceType, &project, &slug, &ref, &commit, &path, &heading, &content, &start, &end); err != nil {
			t.Fatal(err)
		}
		out = append(out, fmt.Sprintf("%s@%s:%s#%d", lib, ref, path, start))
	}
	sort.Strings(out)
	return out
}
