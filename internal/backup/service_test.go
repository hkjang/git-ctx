package backup

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"os"
	"path/filepath"
	"testing"
	"time"

	"git-ctx/internal/store"
)

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
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('c1','r1','main','abc','README.md',1,2,'Demo','document','secret docs','hash',x'010203')`)
	_, _ = db.DB.Exec(`INSERT INTO user_sessions(id_hash,user_id,expires_at,last_seen_at) VALUES(x'01','u1',?,?)`, time.Now().Add(time.Hour), time.Now())
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
	_, _ = db.DB.Exec(`DELETE FROM document_chunks`)
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
	var sessions int
	_ = db.DB.QueryRow(`SELECT COUNT(*) FROM user_sessions`).Scan(&sessions)
	if sessions != 0 {
		t.Fatalf("restored active sessions: %d", sessions)
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
