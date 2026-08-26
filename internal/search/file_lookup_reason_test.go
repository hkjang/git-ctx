package search

import (
	"context"
	"strings"
	"testing"

	"git-ctx/internal/store"
)

// probeFixture builds a catalogue with the shapes a file lookup can fail on:
// a path in one library, the same name in another, a path that exists only on a
// non-default ref, and a repository this identity cannot read.
func probeFixture(t *testing.T) (context.Context, *Service, []string) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:lookup-"+t.Name()+"?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	exec := func(q string, a ...any) {
		if _, err := db.DB.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r1','KCB','alpha','Alpha','bitbucket','1','/kcb/alpha','main',1)`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r2','KCB','beta','Beta','bitbucket','2','/kcb/beta','main',1)`)
	exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch,enabled) VALUES('r3','KCB','secret','Secret','bitbucket','3','/kcb/secret','main',1)`)
	for _, r := range []string{"r1", "r2", "r3"} {
		exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'alice','read')`, r)
	}
	exec(`DELETE FROM repository_permissions WHERE repository_id='r3'`)
	exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r3','bob','read')`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','main.go','main.go',10,1,'abc')`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','release','only-on-release.go','only-on-release.go',10,1,'abc')`)
	exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r2','main','shared.go','shared.go',10,1,'abc')`)

	return ctx, New(db), []string{"alice"}
}

// One sentence used to answer every failed file lookup: "no accessible
// repository contains %q; run find-file first or pass libraryId". It was
// returned even when libraryId had just been passed, and even when the named
// repository — not the path — was the thing that could not be reached. An agent
// told to do what it already did has no move left, and find-file finds nothing
// either when the repository is the problem.
//
// Each situation now names the constraint that emptied the lookup, and each
// answer carries a move the agent has not already made.
func TestAFailedFileLookupSaysWhichConstraintEmptiedIt(t *testing.T) {
	ctx, s, p := probeFixture(t)
	for _, c := range []struct {
		name, path, lib, ref string
		want                 []string
	}{
		{
			name: "the path is in another library", path: "shared.go", lib: "/kcb/alpha",
			want: []string{"not in /kcb/alpha", "/kcb/beta"},
		},
		{
			name: "the path is only on another ref", path: "only-on-release.go", lib: "/kcb/alpha",
			want: []string{"not on its default branch", "release", "pass ref"},
		},
		{
			name: "the named ref does not hold it", path: "only-on-release.go", lib: "/kcb/alpha", ref: "main",
			want: []string{`not on ref "main"`, "release"},
		},
		{
			// A repository this identity cannot read and one that does not exist
			// answer alike on purpose: the difference is what an ACL is for.
			name: "the library cannot be read", path: "main.go", lib: "/kcb/secret",
			want: []string{"/kcb/secret", "this identity can read", "resolve-library-id"},
		},
		{
			name: "the library does not exist", path: "main.go", lib: "/kcb/ghost",
			want: []string{"/kcb/ghost", "this identity can read"},
		},
		{
			name: "the path is indexed nowhere", path: "nothing.go",
			want: []string{"no accessible repository contains", "find-file"},
		},
		{
			name: "no library was named and the path is on another ref", path: "only-on-release.go",
			want: []string{"not on the default branch of", "/kcb/alpha", "pass ref"},
		},
		{
			name: "the library is right and the path is not indexed", path: "nothing.go", lib: "/kcb/alpha",
			want: []string{"not indexed in /kcb/alpha", "find-file"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := s.noFileReason(ctx, p, c.path, c.lib, c.ref)
			if err == nil {
				t.Fatal("an empty lookup answered with no reason at all")
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the reason does not mention %q: %s", want, err)
				}
			}
			// The advice an agent has already followed is not advice.
			if c.lib != "" && strings.Contains(err.Error(), "pass libraryId") {
				t.Errorf("libraryId was passed and the answer asks for it again: %s", err)
			}
		})
	}
}

// The same reasons have to reach the tools an agent actually calls, not only the
// helper. read-file is the one that fails this way most often.
func TestReadFileReportsTheConstraintThatEmptiedIt(t *testing.T) {
	ctx, s, p := probeFixture(t)
	if _, err := s.ReadFile(ctx, p, "/kcb/alpha", "", "shared.go", "", 0, 0); err == nil {
		t.Fatal("reading a file from the wrong library succeeded")
	} else if !strings.Contains(err.Error(), "/kcb/beta") {
		t.Errorf("read-file does not say where the file actually is: %s", err)
	}
	if _, err := s.ReadFile(ctx, p, "/kcb/secret", "", "main.go", "", 0, 0); err == nil {
		t.Fatal("reading a file from an unreadable library succeeded")
	} else if !strings.Contains(err.Error(), "resolve-library-id") {
		t.Errorf("read-file blames the path for a library it cannot reach: %s", err)
	}
}
