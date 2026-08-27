package store

import (
	"context"
	"os"
	"strings"
	"testing"
)

// A search term keeps its punctuation — "dcgm-exporter" arrives as one token —
// while the index tokenizes the content it stores on the same characters, into
// "dcgm" and "exporter". The query builder used to delete the punctuation, so it
// asked the index for "dcgmexporter": a word that appears in no document.
//
// Every image tag, npm package, Helm chart and service name in an enterprise
// catalogue is hyphenated, and none of them could be found through the index.
// The scan behind it still found them, which is why this went unnoticed — but
// the scan is capped, so on a large corpus those searches quietly became a
// sample of the catalogue while every unhyphenated one was answered in full.
func TestAHyphenatedTermReachesTheIndex(t *testing.T) {
	for _, c := range []struct {
		term  string
		parts []string
	}{
		{"git-ctx", []string{"git", "ctx"}},
		{"dcgm-exporter", []string{"dcgm", "exporter"}},
		{"spring-boot-starter", []string{"spring", "boot", "starter"}},
		{"read:write", []string{"read", "write"}},
		{"c++", []string{"c++"}},                 // both pieces too short to ask for
		{"values.yaml", []string{"values.yaml"}}, // a dot is not an operator in either language
		{"api/v1/admin", []string{"api/v1/admin"}},
	} {
		query := FullTextQuery([]string{c.term})
		for _, part := range c.parts {
			if part == "c++" {
				if query != "" {
					t.Errorf("a term with no usable piece produced %q, which the index cannot answer", query)
				}
				continue
			}
			if !strings.Contains(query, `"`+part+`"`) {
				t.Errorf("FullTextQuery(%q) = %q, which never asks for %q", c.term, query, part)
			}
		}
		// The pieces of one term are required together: "dcgm-exporter" asks for
		// a row holding both, not for either.
		if len(c.parts) > 1 && !strings.Contains(query, " AND ") {
			t.Errorf("FullTextQuery(%q) = %q, which does not require the pieces together", c.term, query)
		}
	}
}

// The same term, the same shape, on the other engine — and every input has to
// produce a tsquery PostgreSQL will parse. An invalid one is worse than a miss:
// the query fails instead of returning nothing.
func TestPostgresAcceptsEveryQueryThisBuilderProduces(t *testing.T) {
	dsn := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GIT_CTX_TEST_POSTGRES_DSN is not set")
	}
	s, err := Open(context.Background(), "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer s.DB.Close()
	const document = `the git ctx image is registry.company/git-ctx:latest and the dcgm exporter emits metrics; see values.yaml and api/v1/admin`
	matched := 0
	for _, c := range []struct {
		terms []string
		want  bool
	}{
		{[]string{"git-ctx"}, true},
		{[]string{"dcgm-exporter"}, true},
		{[]string{"values.yaml"}, true},
		{[]string{"api/v1/admin"}, true},
		{[]string{"spring-boot-starter"}, false},
		// Shapes that are query syntax in one language or the other. None may
		// raise an error, whatever they match.
		{[]string{"c++", "a&b", "x|y", "!!", "--", `"quoted"`, "(paren)", "a:b:c", "^caret", "back\\slash"}, false},
	} {
		query := s.FullTextQuery(c.terms)
		if query == "" {
			continue
		}
		var hit bool
		if err := s.DB.QueryRow(`SELECT to_tsvector('simple',$1) @@ to_tsquery('simple',$2)`, document, query).Scan(&hit); err != nil {
			t.Errorf("PostgreSQL refused the query built from %v (%q): %v", c.terms, query, err)
			continue
		}
		if hit != c.want {
			t.Errorf("%v built %q, matched=%v want=%v", c.terms, query, hit, c.want)
		}
		if hit {
			matched++
		}
	}
	if matched < 4 {
		t.Fatalf("only %d queries matched anything, so this proves little about the builder", matched)
	}
}
