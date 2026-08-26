package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git-ctx/internal/store"
)

type writeObserver struct {
	total, largest int
}

func (w *writeObserver) Write(input []byte) (int, error) {
	w.total += len(input)
	w.largest = max(w.largest, len(input))
	return len(input), nil
}

func fixture(t *testing.T, retention int) (*Service, *store.Store, Config) {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "metadata.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.DB.Close() })
	block, err := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{Enabled: true, Directory: filepath.Join(t.TempDir(), "backups"), Interval: time.Hour, RetentionCount: retention, MaxBytes: 64 << 20}
	return New(db, aead, func(context.Context) Config { return cfg }), db, cfg
}

func TestSQLiteEncryptedBackupRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	service, db, cfg := fixture(t, 5)
	_, err := db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','subject','alice','alice@company')`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.DB.Exec(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,mapping_source,bitbucket_groups) VALUES('u1','alice','42','claim','engineering')`)
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('r1','KCB','demo','Demo','bitbucket','1','/kcb/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('r1','alice','read')`)
	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,size_bytes,content_indexed,commit_id) VALUES('r1','main','README.md','readme.md',11,1,'abc')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c1','r1','main','abc','README.md',1,2,'Demo','document','secret docs','hash',x'010203')`)
	_, _ = db.DB.Exec(`INSERT INTO mcp_calls(id,user_id,tool,outcome,duration_ms,client_ip) VALUES('call-1','u1','query-docs','success',12,'127.0.0.1')`)
	_, _ = db.DB.Exec(`INSERT INTO mcp_call_steps(call_id,sequence,stage,target,status,detail,candidates,results,duration_ms,offset_ms) VALUES('call-1',1,'keyword','r1','ok','matched index',3,1,7,2)`)
	_, _ = db.DB.Exec(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(x'01','u1',?,?)`, time.Now().Add(time.Hour), time.Now())
	_, _ = db.DB.Exec(`INSERT INTO managed_secrets(name,backend,value_encrypted,version,status,updated_by) VALUES('model-key','database',x'010203',2,'active','admin')`)
	_, _ = db.DB.Exec(`INSERT INTO managed_secret_versions(name,version,backend,value_encrypted,changed_by,reason) VALUES('model-key',1,'database',x'01','admin','create'),('model-key',2,'database',x'010203','admin','rotate')`)
	record, err := service.Create(ctx, "admin", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if record.Status != "completed" || record.SizeBytes == 0 || record.SHA256 == "" {
		t.Fatalf("record=%#v", record)
	}
	contents, err := os.ReadFile(filepath.Join(cfg.Directory, record.Filename))
	if err != nil {
		t.Fatal(err)
	}
	if string(contents[:len(magic)]) != magic || string(contents) == "secret docs" {
		t.Fatal("backup is not in the authenticated encrypted format")
	}
	_, _ = db.DB.Exec(`UPDATE users SET username='mutated' WHERE id='u1'`)
	_, _ = db.DB.Exec(`DELETE FROM repository_files`)
	_, _ = db.DB.Exec(`DELETE FROM document_chunks`)
	_, _ = db.DB.Exec(`DELETE FROM mcp_call_steps`)
	_, _ = db.DB.Exec(`DELETE FROM managed_secret_versions`)
	_, _ = db.DB.Exec(`DELETE FROM managed_secrets`)
	if err = service.Restore(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	var username, content string
	if err = db.DB.QueryRow(`SELECT username FROM users WHERE id='u1'`).Scan(&username); err != nil || username != "alice" {
		t.Fatalf("username=%q err=%v", username, err)
	}
	if err = db.DB.QueryRow(`SELECT content FROM document_chunks WHERE id='c1'`).Scan(&content); err != nil || content != "secret docs" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	var fileCommit string
	var indexed int
	if err = db.DB.QueryRow(`SELECT commit_id,content_indexed FROM repository_files WHERE repository_id='r1' AND ref_name='main' AND path='README.md'`).Scan(&fileCommit, &indexed); err != nil || fileCommit != "abc" || indexed != 1 {
		t.Fatalf("repository file commit=%q indexed=%d err=%v", fileCommit, indexed, err)
	}
	var stepDetail string
	var candidates, results int
	if err = db.DB.QueryRow(`SELECT detail,candidates,results FROM mcp_call_steps WHERE call_id='call-1' AND sequence=1`).Scan(&stepDetail, &candidates, &results); err != nil || stepDetail != "matched index" || candidates != 3 || results != 1 {
		t.Fatalf("MCP step detail=%q candidates=%d results=%d err=%v", stepDetail, candidates, results, err)
	}
	var sessions int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM user_sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("restored active sessions: %d", sessions)
	}
	var secretVersion, secretVersions int
	if err = db.DB.QueryRow(`SELECT version FROM managed_secrets WHERE name='model-key' AND status='active'`).Scan(&secretVersion); err != nil {
		t.Fatal(err)
	}
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM managed_secret_versions WHERE name='model-key'`).Scan(&secretVersions)
	if secretVersion != 2 || secretVersions != 2 {
		t.Fatalf("managed secret version=%d history=%d", secretVersion, secretVersions)
	}
}

func TestMigrateLogicalUsesStreamedV1Snapshot(t *testing.T) {
	ctx := context.Background()
	_, sourceStore, _ := fixture(t, 5)
	targetStore, err := store.Open(ctx, "sqlite", "file:"+filepath.Join(t.TempDir(), "target.db")+"?_foreign_keys=on&_busy_timeout=5000")
	if err != nil {
		t.Fatal(err)
	}
	defer targetStore.DB.Close()
	_, _ = sourceStore.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('migrate-user','subject','migrated','')`)
	_, _ = sourceStore.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('migrate-repo','KCB','migrate','Migrate','bitbucket','9','/kcb/migrate','main')`)
	_, _ = sourceStore.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name,content_indexed,commit_id) VALUES('migrate-repo','main','README.md','readme.md',1,'abc')`)

	if err = MigrateLogical(ctx, sourceStore, targetStore); err != nil {
		t.Fatal(err)
	}
	var username, commit string
	if err = targetStore.DB.QueryRow(`SELECT username FROM users WHERE id='migrate-user'`).Scan(&username); err != nil {
		t.Fatal(err)
	}
	if err = targetStore.DB.QueryRow(`SELECT commit_id FROM repository_files
WHERE repository_id='migrate-repo' AND ref_name='main' AND path='README.md'`).Scan(&commit); err != nil {
		t.Fatal(err)
	}
	if username != "migrated" || commit != "abc" {
		t.Fatalf("migrated username=%q commit=%q", username, commit)
	}
}

func TestCreateRejectsLogicalPayloadThatRestoreWouldReject(t *testing.T) {
	ctx := context.Background()
	service, db, cfg := fixture(t, 5)
	cfg.MaxBytes = 1 << 20
	service.load = func(context.Context) Config { return cfg }

	_, err := db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('large-r','KCB','large','Large','bitbucket','2','/kcb/large','main')`)
	if err != nil {
		t.Fatal(err)
	}
	// Repeated content compresses to far below 1 MiB, while the logical JSON
	// restored after decompression is over 2 MiB. Before the creation-side
	// expanded-size check this was marked completed and then Restore rejected it.
	largeContent := strings.Repeat("compressible-backup-content\n", 100000)
	_, err = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('large-c','large-r','main','abc','large.txt',1,100000,'Large','document',?,'hash')`, largeContent)
	if err != nil {
		t.Fatal(err)
	}
	compressed, expandedBytes, err := service.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if expandedBytes <= cfg.MaxBytes {
		t.Fatalf("logical payload=%d, want greater than max=%d", expandedBytes, cfg.MaxBytes)
	}
	if int64(len(compressed)) >= cfg.MaxBytes {
		t.Fatalf("test payload no longer demonstrates compression mismatch: compressed=%d max=%d", len(compressed), cfg.MaxBytes)
	}
	limitedPayload, written, limitErr := service.snapshotWithLimit(ctx, cfg.MaxBytes)
	if !errors.Is(limitErr, errLogicalPayloadTooLarge) {
		t.Fatalf("limited streaming snapshot error=%v", limitErr)
	}
	if limitedPayload != nil || written != cfg.MaxBytes {
		t.Fatalf("limited snapshot payload=%d logical bytes=%d want nil/%d", len(limitedPayload), written, cfg.MaxBytes)
	}

	record, err := service.Create(ctx, "admin", "manual")
	if err == nil || !strings.Contains(err.Error(), "logical payload exceeds") {
		t.Fatalf("record=%#v err=%v", record, err)
	}
	if record.Status != "failed" {
		t.Fatalf("oversized logical payload status=%q, want failed", record.Status)
	}
	if _, statErr := os.Stat(filepath.Join(cfg.Directory, record.Filename)); !os.IsNotExist(statErr) {
		t.Fatalf("oversized backup file was written: %v", statErr)
	}
	var storedStatus string
	if queryErr := db.DB.QueryRow(`SELECT status FROM backup_records WHERE id=?`, record.ID).Scan(&storedStatus); queryErr != nil || storedStatus != "failed" {
		t.Fatalf("stored status=%q err=%v", storedStatus, queryErr)
	}
}

func TestLogicalJSONStreamingBoundsWritesAndStopsAtLimit(t *testing.T) {
	large := strings.Repeat("plain text with no escaping ", 100000)
	observed := &writeObserver{}
	writer := &logicalLimitWriter{destination: observed}
	if err := writeJSONString(writer, large); err != nil {
		t.Fatal(err)
	}
	if observed.total != len(large)+2 || writer.written != int64(observed.total) {
		t.Fatalf("streamed=%d counted=%d want=%d", observed.total, writer.written, len(large)+2)
	}
	if observed.largest > snapshotStringChunkSize {
		t.Fatalf("largest downstream write=%d exceeds chunk=%d", observed.largest, snapshotStringChunkSize)
	}

	observed = &writeObserver{}
	writer = &logicalLimitWriter{destination: observed, limit: 4096}
	err := writeJSONString(writer, large)
	if !errors.Is(err, errLogicalPayloadTooLarge) {
		t.Fatalf("limit error=%v", err)
	}
	if writer.written != 4096 || observed.total != 4096 {
		t.Fatalf("limit forwarded=%d counted=%d want=4096", observed.total, writer.written)
	}
}

func TestStreamedJSONStringMatchesEncodingJSON(t *testing.T) {
	input := "quote=\" slash=\\ controls=\b\f\n\r\t html=<>& separators=\u2028\u2029 unicode=한글😀 invalid=" + string([]byte{0xff})
	var streamed bytes.Buffer
	writer := &logicalLimitWriter{destination: &streamed}
	if err := writeJSONString(writer, input); err != nil {
		t.Fatal(err)
	}
	expected, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(streamed.Bytes(), expected) {
		t.Fatalf("streamed JSON=%q\nencoding/json=%q", streamed.Bytes(), expected)
	}
}

func TestRestoreAcceptsLegacyV1WithoutNewlyCoveredTables(t *testing.T) {
	ctx := context.Background()
	service, db, _ := fixture(t, 5)
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('legacy-user','legacy','legacy','')`)
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('legacy-repo','KCB','legacy','Legacy','bitbucket','1','/kcb/legacy','main')`)
	_, _ = db.DB.Exec(`INSERT INTO mcp_calls(id,user_id,tool,outcome,duration_ms,client_ip) VALUES('legacy-call','legacy-user','query-docs','success',1,'127.0.0.1')`)
	compressed, _, err := service.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	var legacy archive
	if err = json.Unmarshal(raw, &legacy); err != nil {
		t.Fatal(err)
	}
	// A genuine V1 archive predates repository_files and mcp_call_steps, and
	// also the key tables, which arrived later still.
	filtered := legacy.Tables[:0]
	for _, data := range legacy.Tables {
		switch data.Name {
		case "repository_files", "mcp_call_steps", "platform_keys", "platform_bootstrap":
		default:
			filtered = append(filtered, data)
		}
	}
	legacy.Tables = filtered
	if len(legacy.Tables) != len(legacyV1Tables) {
		t.Fatalf("legacy table count=%d want=%d", len(legacy.Tables), len(legacyV1Tables))
	}

	_, _ = db.DB.Exec(`INSERT INTO repository_files(repository_id,ref_name,path,base_name) VALUES('legacy-repo','main','stale.md','stale.md')`)
	_, _ = db.DB.Exec(`INSERT INTO mcp_call_steps(call_id,sequence,stage,status) VALUES('legacy-call',1,'keyword','ok')`)
	if err = service.restoreArchive(ctx, legacy); err != nil {
		t.Fatal(err)
	}
	var users, files, steps int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM users WHERE id='legacy-user'`).Scan(&users)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM repository_files`).Scan(&files)
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM mcp_call_steps`).Scan(&steps)
	if users != 1 || files != 0 || steps != 0 {
		t.Fatalf("legacy restore users=%d repository_files=%d mcp_call_steps=%d", users, files, steps)
	}
}

func TestBackupTamperDetectionAndRetention(t *testing.T) {
	ctx := context.Background()
	service, _, cfg := fixture(t, 2)
	first, err := service.Create(ctx, "admin", "manual")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = service.Create(ctx, "admin", "manual"); err != nil {
		t.Fatal(err)
	}
	third, err := service.Create(ctx, "scheduler", "scheduled")
	if err != nil {
		t.Fatal(err)
	}
	records, err := service.List(ctx)
	if err != nil || len(records) != 2 {
		t.Fatalf("records=%d err=%v", len(records), err)
	}
	if _, err = os.Stat(filepath.Join(cfg.Directory, first.Filename)); !os.IsNotExist(err) {
		t.Fatalf("expired backup still exists: %v", err)
	}
	path := filepath.Join(cfg.Directory, third.Filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = file.Write([]byte("tamper"))
	file.Close()
	if err = service.Restore(ctx, third.ID); err == nil {
		t.Fatal("tampered backup restored")
	}
}

func TestValidateStorageRejectsUnsafeDirectory(t *testing.T) {
	cfg := Config{Directory: "/", Interval: time.Hour, RetentionCount: 1, MaxBytes: 1 << 20}
	if err := ValidateStorage(cfg); err == nil {
		t.Fatal("filesystem root accepted as backup directory")
	}
	cfg.Directory = filepath.Join(t.TempDir(), "valid")
	if err := ValidateStorage(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestRunOnceDoesNotDuplicateCurrentSchedule(t *testing.T) {
	ctx := context.Background()
	service, _, _ := fixture(t, 5)
	if err := service.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if err := service.RunOnce(ctx); err != nil {
		t.Fatal(err)
	}
	records, err := service.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].TriggerType != "scheduled" {
		t.Fatalf("records=%#v", records)
	}
}

// An archive written by a release up to v0.66.0 carries everything except the
// key material, because those releases had no key material to carry: the keys
// were derived from the connection string. Such an archive still restores.
func TestRestoreAcceptsAnArchiveWithoutTheKeyTables(t *testing.T) {
	ctx := context.Background()
	service, db, _ := fixture(t, 5)
	_, _ = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('u1','s','u','')`)

	compressed, _, err := service.snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(reader)
	if closeErr := reader.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		t.Fatal(err)
	}
	var older archive
	if err = json.Unmarshal(raw, &older); err != nil {
		t.Fatal(err)
	}
	filtered := older.Tables[:0]
	for _, data := range older.Tables {
		if data.Name != "platform_keys" && data.Name != "platform_bootstrap" {
			filtered = append(filtered, data)
		}
	}
	older.Tables = filtered
	if len(older.Tables) != len(legacyV2Tables) {
		t.Fatalf("table count=%d want=%d", len(older.Tables), len(legacyV2Tables))
	}
	if err = service.restoreArchive(ctx, older); err != nil {
		t.Fatalf("an archive from before the key tables was rejected: %v", err)
	}
	var users int
	if err = db.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&users); err != nil {
		t.Fatal(err)
	}
	if users != 1 {
		t.Fatalf("the restore did not carry its rows: users=%d", users)
	}
}
