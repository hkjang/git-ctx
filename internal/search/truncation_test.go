package search

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"git-ctx/internal/source"
	"git-ctx/internal/store"
)

// bodySource hands ReadFile one prepared file body, the way a source server
// returns a file that was never indexed.
type bodySource struct {
	querySource
	body string
}

func (b *bodySource) GetFile(context.Context, source.RepositoryRef, string, string) ([]byte, error) {
	return []byte(b.body), nil
}

func (b *bodySource) ListFiles(context.Context, source.RepositoryRef, string) ([]source.File, error) {
	return nil, nil
}

// koreanBody builds a file whose text crosses the read-file byte budget, with
// pad ASCII bytes in front so the budget lands on each of the three offsets
// inside a three-byte character.
func koreanBody(pad int) string {
	line := strings.Repeat("한글", 60) // 360 bytes
	lines := make([]string, 600)
	for i := range lines {
		lines[i] = line
	}
	return strings.Repeat("x", pad) + strings.Join(lines, "\n")
}

// TestReadFileTruncatesOnACharacterBoundary reads a file larger than the byte
// budget through the real service and store. A plain byte slice at the budget
// splits a Korean character and the JSON encoder then replaces the broken tail
// with U+FFFD, which is what the caller of the REST playground receives.
func TestReadFileTruncatesOnACharacterBoundary(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:read-file-utf8?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r','main','docs/large.md','large.md',300000,0,'abc')`)

	remote := &bodySource{}
	service := New(db)
	service.SetSourceLoader(func(context.Context, string) (source.RepositorySource, error) { return remote, nil })

	for pad := 0; pad < 4; pad++ {
		body := koreanBody(pad)
		remote.body = body
		out, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/large.md", "", 0, 0)
		if err != nil {
			t.Fatalf("pad=%d read: %v", pad, err)
		}
		if !out.Truncated {
			t.Fatalf("pad=%d: a body of %d bytes was not reported as truncated", pad, len(body))
		}
		if !utf8.ValidString(out.Content) {
			t.Fatalf("pad=%d: the returned content is not valid UTF-8", pad)
		}
		if len(out.Content) > readFileByteBudget {
			t.Fatalf("pad=%d: content is %d bytes, over the %d byte budget", pad, len(out.Content), readFileByteBudget)
		}
		// Only the character the budget fell inside may be dropped.
		if len(out.Content) <= readFileByteBudget-4 {
			t.Fatalf("pad=%d: content is %d bytes, more than one character short of the %d byte budget", pad, len(out.Content), readFileByteBudget)
		}
		if !strings.HasPrefix(body, out.Content) {
			t.Fatalf("pad=%d: the returned content is not a prefix of the file", pad)
		}
		// encoding/json writes invalid UTF-8 as a \ufffd escape sequence, so
		// the raw character never appears in the encoded text — decode it back
		// to see what the caller of the REST playground actually receives.
		encoded, err := json.Marshal(out)
		if err != nil {
			t.Fatalf("pad=%d marshal: %v", pad, err)
		}
		if strings.Contains(string(encoded), "\\ufffd") {
			t.Fatalf("pad=%d: the JSON response escapes a replacement character", pad)
		}
		var decoded FileContent
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("pad=%d unmarshal: %v", pad, err)
		}
		if strings.ContainsRune(decoded.Content, utf8.RuneError) || !strings.HasPrefix(body, decoded.Content) {
			t.Fatalf("pad=%d: the JSON round trip did not return a prefix of the file", pad)
		}
		diagnostics := strings.Join(out.Diagnostics, " ")
		if !strings.Contains(diagnostics, "truncated: returned lines 1-600 of 600") {
			t.Fatalf("pad=%d: the truncation diagnostic changed: %v", pad, out.Diagnostics)
		}
	}

	// An emoji body puts a four-byte character across the budget.
	emoji := strings.Repeat("a", 3) + strings.Join(func() []string {
		lines := make([]string, 600)
		for i := range lines {
			lines[i] = strings.Repeat("🙂", 90) // 360 bytes
		}
		return lines
	}(), "\n")
	remote.body = emoji
	out, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/large.md", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.ValidString(out.Content) || !strings.HasPrefix(emoji, out.Content) {
		t.Fatalf("emoji body: valid=%v prefix=%v", utf8.ValidString(out.Content), strings.HasPrefix(emoji, out.Content))
	}

	// A body under the byte budget is returned whole and unmarked.
	remote.body = strings.Repeat("한글\n", 100)
	small, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/large.md", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if small.Truncated || small.Content != strings.TrimSuffix(remote.body, "\n")+"\n" {
		t.Fatalf("a small body was altered: truncated=%v len=%d", small.Truncated, len(small.Content))
	}

	// ASCII over the budget still cuts at exactly the budget.
	remote.body = strings.Repeat(strings.Repeat("a", 360)+"\n", 600)
	ascii, err := service.ReadFile(ctx, []string{"alice"}, "", "", "docs/large.md", "", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !ascii.Truncated || len(ascii.Content) != readFileByteBudget {
		t.Fatalf("ascii body: truncated=%v len=%d want %d", ascii.Truncated, len(ascii.Content), readFileByteBudget)
	}
}

// TestExportContextTruncatesOnACharacterBoundary drives the export over its
// 200000 byte limit with Korean chunk text and checks the same property, plus
// that the notice the limit adds is still there.
func TestExportContextTruncatesOnACharacterBoundary(t *testing.T) {
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:export-utf8?mode=memory&cache=shared&_foreign_keys=on")
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r','core','demo','Demo','gitlab','1','/core/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r','alice','read')`)
	for i := 0; i < 8; i++ {
		content := "gpu " + strings.Repeat("한글", 6000) // 36000 bytes of Korean
		_, err := db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES(?,'r','main','abc',?,1,2,?,'document',?,?)`,
			fmt.Sprintf("c%d", i), fmt.Sprintf("docs/d%d.md", i), fmt.Sprintf("GPU %d", i), content, fmt.Sprintf("h%d", i))
		if err != nil {
			t.Fatal(err)
		}
	}
	service := New(db)

	// A pad of ASCII in the query has no effect; shift the cut instead by
	// padding the first chunk, so the limit falls on each offset of a
	// three-byte character.
	for pad := 0; pad < 4; pad++ {
		_, err := db.DB.Exec(`UPDATE document_chunks SET content=? WHERE id='c0'`,
			"gpu "+strings.Repeat("y", pad)+strings.Repeat("한글", 6000))
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.ExportContext(ctx, []string{"alice"}, []string{"/core/demo"}, "gpu")
		if err != nil {
			t.Fatalf("pad=%d export: %v", pad, err)
		}
		const notice = "\n\n[Export truncated at the platform safety limit.]"
		if !strings.HasSuffix(result, notice) {
			t.Fatalf("pad=%d: the truncation notice is missing (len=%d)", pad, len(result))
		}
		body := strings.TrimSuffix(result, notice)
		if len(body) > 200000 {
			t.Fatalf("pad=%d: exported body is %d bytes, over the 200000 byte limit", pad, len(body))
		}
		if len(body) <= 200000-4 {
			t.Fatalf("pad=%d: exported body is %d bytes, more than one character short of the limit", pad, len(body))
		}
		if !utf8.ValidString(result) {
			t.Fatalf("pad=%d: the export is not valid UTF-8", pad)
		}
		encoded, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(encoded), "\\ufffd") {
			t.Fatalf("pad=%d: the JSON export escapes a replacement character", pad)
		}
		var decoded string
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded != result {
			t.Fatalf("pad=%d: the JSON round trip changed the export", pad)
		}
	}
}
