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

func TestPostgresBackupRestoreIntegration(t *testing.T) {
	dsn := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("GIT_CTX_TEST_POSTGRES_DSN is not set")
	}
	ctx := context.Background()
	db, err := store.Open(ctx, "postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.DB.Close()
	block, _ := aes.NewCipher([]byte("0123456789abcdef0123456789abcdef"))
	aead, _ := cipher.NewGCM(block)
	cfg := Config{Enabled: true, Directory: filepath.Join(t.TempDir(), "backups"), Interval: time.Hour, RetentionCount: 3, MaxBytes: 64 << 20}
	service := New(db, aead, func(context.Context) Config { return cfg })
	_, err = db.DB.Exec(`INSERT INTO users(id,subject,username,email) VALUES('pg-user','pg-subject','before','pg@company')`)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.DB.Exec(`INSERT INTO repositories(id,project_key,slug,name,source_type,source_external_id,library_id,default_branch) VALUES('pg-repo','PG','demo','Demo','gitlab','1','/pg/demo','main')`)
	_, _ = db.DB.Exec(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash,embedding) VALUES('pg-chunk','pg-repo','main','abc','README.md',1,2,'Demo','document','postgres original','hash',$1)`, []byte{1, 2, 3})
	_, _ = db.DB.Exec(`INSERT INTO managed_secrets(name,backend,value_encrypted,version,status,updated_by) VALUES('pg-secret','database',$1,1,'active','integration')`, []byte{4, 5, 6})
	_, _ = db.DB.Exec(`INSERT INTO managed_secret_versions(name,version,backend,value_encrypted,changed_by,reason) VALUES('pg-secret',1,'database',$1,'integration','create')`, []byte{4, 5, 6})
	record, err := service.Create(ctx, "integration", "manual")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = db.DB.Exec(`UPDATE users SET username='after' WHERE id='pg-user'`)
	_, _ = db.DB.Exec(`DELETE FROM document_chunks WHERE id='pg-chunk'`)
	_, _ = db.DB.Exec(`DELETE FROM managed_secret_versions WHERE name='pg-secret'`)
	_, _ = db.DB.Exec(`DELETE FROM managed_secrets WHERE name='pg-secret'`)
	if err = service.Restore(ctx, record.ID); err != nil {
		t.Fatal(err)
	}
	var username, content string
	if err = db.DB.QueryRow(`SELECT username FROM users WHERE id='pg-user'`).Scan(&username); err != nil || username != "before" {
		t.Fatalf("username=%q err=%v", username, err)
	}
	if err = db.DB.QueryRow(`SELECT content FROM document_chunks WHERE id='pg-chunk'`).Scan(&content); err != nil || content != "postgres original" {
		t.Fatalf("content=%q err=%v", content, err)
	}
	var secretVersion int
	if err = db.DB.QueryRow(`SELECT version FROM managed_secrets WHERE name='pg-secret'`).Scan(&secretVersion); err != nil || secretVersion != 1 {
		t.Fatalf("secretVersion=%d err=%v", secretVersion, err)
	}
}
