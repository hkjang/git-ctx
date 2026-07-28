package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/store"
)

const magic = "GCTXBACKUP1\n"

var tables = []string{
	"users", "roles", "user_identities", "user_roles",
	"api_keys", "api_key_restrictions", "api_key_usage_buckets",
	"repositories", "repository_permissions", "repository_index_policies", "document_chunks", "repository_ref_states", "repository_ref_changes", "search_projection_states",
	"code_symbols", "code_dependencies", "repository_maps",
	"quality_benchmark_cases", "quality_benchmark_runs", "quality_benchmark_results",
	"system_settings", "setting_versions", "audit_logs", "mcp_calls", "index_jobs",
	"webhook_events", "index_security_events", "mcp_tools", "notifications", "notification_deliveries",
	"managed_secrets", "managed_secret_versions",
}

type Config struct {
	Enabled        bool
	Directory      string
	Interval       time.Duration
	RetentionCount int
	MaxBytes       int64
}

type ConfigLoader func(context.Context) Config

type Record struct {
	ID           string     `json:"id"`
	Filename     string     `json:"filename"`
	TriggerType  string     `json:"triggerType"`
	Status       string     `json:"status"`
	SHA256       string     `json:"sha256"`
	CreatedBy    string     `json:"createdBy"`
	ErrorMessage string     `json:"errorMessage"`
	SizeBytes    int64      `json:"sizeBytes"`
	CreatedAt    time.Time  `json:"createdAt"`
	CompletedAt  *time.Time `json:"completedAt,omitempty"`
}

type Service struct {
	store *store.Store
	aead  cipher.AEAD
	load  ConfigLoader
	mu    sync.Mutex
}

type archive struct {
	Format, SourceDriver string
	CreatedAt            time.Time
	Migrations           []string
	Tables               []tableData
}
type tableData struct {
	Name, ColumnsCSV string
	Rows             [][]value
}
type value struct {
	Kind, Data string
}

func New(s *store.Store, aead cipher.AEAD, loader ConfigLoader) *Service {
	return &Service{store: s, aead: aead, load: loader}
}

var ErrAlreadyScheduled = errors.New("backup was already created for this schedule slot")

func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		_ = s.RunOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) RunOnce(ctx context.Context) error {
	cfg := s.load(ctx)
	if !cfg.Enabled {
		return nil
	}
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	var latest time.Time
	err := s.store.DB.QueryRowContext(ctx, `SELECT created_at FROM backup_records WHERE status='completed' ORDER BY created_at DESC LIMIT 1`).Scan(&latest)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err == nil && time.Since(latest) < cfg.Interval {
		return nil
	}
	_, err = s.Create(ctx, "scheduler", "scheduled")
	if errors.Is(err, ErrAlreadyScheduled) {
		return nil
	}
	return err
}

func (s *Service) Create(ctx context.Context, actor, trigger string) (record Record, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.load(ctx)
	if err = ValidateConfig(cfg); err != nil {
		return Record{}, err
	}
	if trigger == "scheduled" && !cfg.Enabled {
		return Record{}, errors.New("scheduled backups are disabled")
	}
	id, err := newID()
	if err != nil {
		return Record{}, err
	}
	record = Record{ID: id, Filename: id + ".gctxbak", TriggerType: trigger, Status: "running", CreatedBy: actor, CreatedAt: time.Now().UTC()}
	var scheduleSlot any
	if trigger == "scheduled" {
		scheduleSlot = record.CreatedAt.Truncate(cfg.Interval).Format(time.RFC3339)
	}
	result, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`INSERT INTO backup_records(id,filename,schedule_slot,trigger_type,status,created_by,created_at) VALUES(?,?,?,?,?,?,?) ON CONFLICT(schedule_slot) DO NOTHING`), record.ID, record.Filename, scheduleSlot, record.TriggerType, record.Status, record.CreatedBy, record.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Record{}, ErrAlreadyScheduled
	}
	defer func() {
		if err != nil {
			record.Status, record.ErrorMessage = "failed", truncate(err.Error(), 1000)
			_, _ = s.store.DB.ExecContext(context.Background(), s.store.Rebind(`UPDATE backup_records SET status='failed',error_message=?,completed_at=? WHERE id=?`), record.ErrorMessage, time.Now().UTC(), record.ID)
		}
	}()
	payload, err := s.snapshot(ctx)
	if err != nil {
		return record, err
	}
	sealed, err := s.seal(payload)
	if err != nil {
		return record, err
	}
	if int64(len(sealed)) > cfg.MaxBytes {
		return record, fmt.Errorf("backup exceeds configured maximum of %d bytes", cfg.MaxBytes)
	}
	if err = writeAtomic(cfg.Directory, record.Filename, sealed); err != nil {
		return record, err
	}
	hash := sha256.Sum256(sealed)
	record.SizeBytes, record.SHA256, record.Status = int64(len(sealed)), hex.EncodeToString(hash[:]), "completed"
	completed := time.Now().UTC()
	record.CompletedAt = &completed
	_, err = s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE backup_records SET status='completed',size_bytes=?,sha256=?,completed_at=?,error_message='' WHERE id=?`), record.SizeBytes, record.SHA256, completed, record.ID)
	if err != nil {
		_ = os.Remove(filepath.Join(cfg.Directory, record.Filename))
		return record, err
	}
	s.enforceRetention(ctx, cfg)
	return record, nil
}

func (s *Service) snapshot(ctx context.Context) ([]byte, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if s.store.Driver() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.store.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	a := archive{Format: "git-ctx-logical-v1", SourceDriver: s.store.Driver(), CreatedAt: time.Now().UTC()}
	migrations, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, err
	}
	for migrations.Next() {
		var version string
		if err = migrations.Scan(&version); err != nil {
			migrations.Close()
			return nil, err
		}
		a.Migrations = append(a.Migrations, version)
	}
	migrations.Close()
	for _, table := range tables {
		rows, queryErr := tx.QueryContext(ctx, `SELECT * FROM "`+table+`"`)
		if queryErr != nil {
			return nil, fmt.Errorf("read %s: %w", table, queryErr)
		}
		columns, queryErr := rows.Columns()
		if queryErr != nil {
			rows.Close()
			return nil, queryErr
		}
		data := tableData{Name: table, ColumnsCSV: strings.Join(columns, ",")}
		for rows.Next() {
			raw := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range raw {
				pointers[i] = &raw[i]
			}
			if queryErr = rows.Scan(pointers...); queryErr != nil {
				rows.Close()
				return nil, queryErr
			}
			encoded := make([]value, len(raw))
			for i := range raw {
				encoded[i], queryErr = encodeValue(raw[i])
				if queryErr != nil {
					rows.Close()
					return nil, fmt.Errorf("encode %s.%s: %w", table, columns[i], queryErr)
				}
			}
			data.Rows = append(data.Rows, encoded)
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return nil, queryErr
		}
		rows.Close()
		a.Tables = append(a.Tables, data)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err = writer.Write(raw); err != nil {
		return nil, err
	}
	if err = writer.Close(); err != nil {
		return nil, err
	}
	return compressed.Bytes(), nil
}

func (s *Service) Restore(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cfg := s.load(ctx)
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	sealed, _, err := s.readVerified(ctx, cfg, id)
	if err != nil {
		return err
	}
	payload, err := s.open(sealed)
	if err != nil {
		return errors.New("backup authentication or decryption failed")
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return errors.New("backup compression stream is invalid")
	}
	raw, err := io.ReadAll(io.LimitReader(reader, cfg.MaxBytes+1))
	reader.Close()
	if err != nil || int64(len(raw)) > cfg.MaxBytes {
		return errors.New("backup payload is invalid or exceeds the configured maximum")
	}
	var a archive
	if err = json.Unmarshal(raw, &a); err != nil || a.Format != "git-ctx-logical-v1" {
		return errors.New("unsupported backup format")
	}
	return s.restoreArchive(ctx, a)
}

// MigrateLogical copies durable application data between already-migrated
// stores. Sessions, auth flows and bootstrap credentials are deliberately not
// copied, matching the restore security boundary.
func MigrateLogical(ctx context.Context, source, target *store.Store) error {
	sourceService := &Service{store: source}
	payload, err := sourceService.snapshot(ctx)
	if err != nil {
		return err
	}
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return err
	}
	raw, err := io.ReadAll(reader)
	reader.Close()
	if err != nil {
		return err
	}
	var a archive
	if err = json.Unmarshal(raw, &a); err != nil || a.Format != "git-ctx-logical-v1" {
		return errors.New("unsupported migration snapshot")
	}
	return (&Service{store: target}).restoreArchive(ctx, a)
}

func (s *Service) restoreArchive(ctx context.Context, a archive) error {
	var err error
	if err = s.validateArchive(ctx, a); err != nil {
		return err
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Authentication flows and sessions are deliberately not restored. This
	// both prevents replay and ensures restored identity state starts closed.
	for _, ephemeral := range []string{"user_sessions", "auth_flows", "platform_bootstrap"} {
		if _, err = tx.ExecContext(ctx, `DELETE FROM "`+ephemeral+`"`); err != nil {
			return fmt.Errorf("clear %s: %w", ephemeral, err)
		}
	}
	for i := len(tables) - 1; i >= 0; i-- {
		if _, err = tx.ExecContext(ctx, `DELETE FROM "`+tables[i]+`"`); err != nil {
			return fmt.Errorf("clear %s: %w", tables[i], err)
		}
	}
	for _, data := range a.Tables {
		columns := strings.Split(data.ColumnsCSV, ",")
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(columns)), ",")
		query := `INSERT INTO "` + data.Name + `" (` + quotedColumns(columns) + `) VALUES (` + placeholders + `)`
		query = s.store.Rebind(query)
		for _, row := range data.Rows {
			args := make([]any, len(row))
			for i := range row {
				args[i], err = decodeValue(row[i])
				if err != nil {
					return fmt.Errorf("decode %s row: %w", data.Name, err)
				}
			}
			if _, err = tx.ExecContext(ctx, query, args...); err != nil {
				return fmt.Errorf("restore %s: %w", data.Name, err)
			}
		}
	}
	return tx.Commit()
}

func (s *Service) validateArchive(ctx context.Context, a archive) error {
	if len(a.Tables) != len(tables) {
		return errors.New("backup table set does not match this git-ctx version")
	}
	for i := range tables {
		if a.Tables[i].Name != tables[i] {
			return errors.New("backup table order or identity is invalid")
		}
		columns, err := s.store.DB.QueryContext(ctx, `SELECT * FROM "`+tables[i]+`" WHERE 1=0`)
		if err != nil {
			return err
		}
		current, err := columns.Columns()
		columns.Close()
		if err != nil || strings.Join(current, ",") != a.Tables[i].ColumnsCSV {
			return fmt.Errorf("backup schema for %s does not match", tables[i])
		}
	}
	rows, err := s.store.DB.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var current []string
	for rows.Next() {
		var version string
		if err = rows.Scan(&version); err != nil {
			return err
		}
		current = append(current, version)
	}
	if strings.Join(current, "\n") != strings.Join(a.Migrations, "\n") {
		return errors.New("backup migration set does not match this git-ctx version")
	}
	return rows.Err()
}

func (s *Service) List(ctx context.Context) ([]Record, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT id,filename,trigger_type,status,size_bytes,sha256,created_by,error_message,created_at,completed_at FROM backup_records ORDER BY created_at DESC LIMIT 200`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Record
	for rows.Next() {
		var r Record
		var completed sql.NullTime
		if err = rows.Scan(&r.ID, &r.Filename, &r.TriggerType, &r.Status, &r.SizeBytes, &r.SHA256, &r.CreatedBy, &r.ErrorMessage, &r.CreatedAt, &completed); err != nil {
			return nil, err
		}
		if completed.Valid {
			r.CompletedAt = &completed.Time
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Service) Open(ctx context.Context, id string) (*os.File, Record, error) {
	cfg := s.load(ctx)
	if err := ValidateConfig(cfg); err != nil {
		return nil, Record{}, err
	}
	_, record, err := s.readVerified(ctx, cfg, id)
	if err != nil {
		return nil, Record{}, err
	}
	file, err := os.Open(filepath.Join(cfg.Directory, record.Filename))
	return file, record, err
}

func (s *Service) readVerified(ctx context.Context, cfg Config, id string) ([]byte, Record, error) {
	var r Record
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT id,filename,trigger_type,status,size_bytes,sha256,created_by,error_message,created_at,completed_at FROM backup_records WHERE id=? AND status='completed'`), id).Scan(&r.ID, &r.Filename, &r.TriggerType, &r.Status, &r.SizeBytes, &r.SHA256, &r.CreatedBy, &r.ErrorMessage, &r.CreatedAt, &r.CompletedAt)
	if err != nil {
		return nil, Record{}, err
	}
	path := filepath.Join(cfg.Directory, r.Filename)
	if filepath.Dir(path) != filepath.Clean(cfg.Directory) {
		return nil, Record{}, errors.New("invalid backup path")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, Record{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, cfg.MaxBytes+1))
	if err != nil || int64(len(data)) > cfg.MaxBytes || int64(len(data)) != r.SizeBytes {
		return nil, Record{}, errors.New("backup size verification failed")
	}
	hash := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(hash[:]), r.SHA256) {
		return nil, Record{}, errors.New("backup SHA-256 verification failed")
	}
	return data, r, nil
}

func (s *Service) seal(payload []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := s.aead.Seal(nil, nonce, payload, []byte(magic))
	return append(append([]byte{}, []byte(magic)...), append(nonce, sealed...)...), nil
}

func (s *Service) open(data []byte) ([]byte, error) {
	if len(data) < len(magic)+s.aead.NonceSize() || string(data[:len(magic)]) != magic {
		return nil, errors.New("invalid backup header")
	}
	nonceStart := len(magic)
	nonceEnd := nonceStart + s.aead.NonceSize()
	return s.aead.Open(nil, data[nonceStart:nonceEnd], data[nonceEnd:], []byte(magic))
}

func (s *Service) enforceRetention(ctx context.Context, cfg Config) {
	rows, err := s.store.DB.QueryContext(ctx, s.store.Rebind(`SELECT id,filename FROM backup_records WHERE status='completed' ORDER BY created_at DESC LIMIT 1000 OFFSET ?`), cfg.RetentionCount)
	if err != nil {
		return
	}
	type expired struct{ id, filename string }
	var records []expired
	for rows.Next() {
		var record expired
		if rows.Scan(&record.id, &record.filename) == nil {
			records = append(records, record)
		}
	}
	rows.Close()
	for _, record := range records {
		_ = os.Remove(filepath.Join(cfg.Directory, filepath.Base(record.filename)))
		_, _ = s.store.DB.ExecContext(ctx, s.store.Rebind(`DELETE FROM backup_records WHERE id=?`), record.id)
	}
}

func ValidateConfig(cfg Config) error {
	clean := filepath.Clean(cfg.Directory)
	if cfg.Directory == "" || clean == "." || clean == string(filepath.Separator) || strings.Contains(cfg.Directory, "\x00") {
		return errors.New("backup.directory must identify a dedicated directory")
	}
	if cfg.Interval < time.Hour {
		return errors.New("backup.intervalHours must be at least 1")
	}
	if cfg.RetentionCount < 1 || cfg.RetentionCount > 100 {
		return errors.New("backup.retentionCount must be 1..100")
	}
	if cfg.MaxBytes < 1<<20 || cfg.MaxBytes > 10<<30 {
		return errors.New("backup.maxBytes must be between 1 MiB and 10 GiB")
	}
	return nil
}

func ValidateStorage(cfg Config) error {
	if err := ValidateConfig(cfg); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Directory, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	probe, err := os.CreateTemp(cfg.Directory, ".git-ctx-write-test-*")
	if err != nil {
		return fmt.Errorf("backup directory is not writable: %w", err)
	}
	name := probe.Name()
	if chmodErr := probe.Chmod(0o600); chmodErr != nil {
		probe.Close()
		os.Remove(name)
		return chmodErr
	}
	closeErr := probe.Close()
	removeErr := os.Remove(name)
	if closeErr != nil {
		return closeErr
	}
	return removeErr
}

func writeAtomic(directory, filename string, data []byte) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(directory, ".git-ctx-backup-*")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err = temp.Chmod(0o600); err == nil {
		_, err = temp.Write(data)
	}
	if err == nil {
		err = temp.Sync()
	}
	closeErr := temp.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tempName, filepath.Join(directory, filename))
}

func encodeValue(input any) (value, error) {
	switch item := input.(type) {
	case nil:
		return value{Kind: "null"}, nil
	case []byte:
		return value{Kind: "bytes", Data: base64.StdEncoding.EncodeToString(item)}, nil
	case string:
		return value{Kind: "string", Data: item}, nil
	case time.Time:
		return value{Kind: "time", Data: item.UTC().Format(time.RFC3339Nano)}, nil
	case int64:
		return value{Kind: "integer", Data: strconv.FormatInt(item, 10)}, nil
	case float64:
		return value{Kind: "float", Data: strconv.FormatFloat(item, 'g', -1, 64)}, nil
	case bool:
		return value{Kind: "bool", Data: strconv.FormatBool(item)}, nil
	default:
		return value{}, fmt.Errorf("unsupported SQL value %T", input)
	}
}

func decodeValue(input value) (any, error) {
	switch input.Kind {
	case "null":
		return nil, nil
	case "bytes":
		return base64.StdEncoding.DecodeString(input.Data)
	case "string":
		return input.Data, nil
	case "time":
		return time.Parse(time.RFC3339Nano, input.Data)
	case "integer":
		return strconv.ParseInt(input.Data, 10, 64)
	case "float":
		return strconv.ParseFloat(input.Data, 64)
	case "bool":
		return strconv.ParseBool(input.Data)
	default:
		return nil, errors.New("unknown backup value type")
	}
}

func quotedColumns(columns []string) string {
	quoted := make([]string, len(columns))
	for i := range columns {
		quoted[i] = `"` + strings.ReplaceAll(columns[i], `"`, `""`) + `"`
	}
	return strings.Join(quoted, ",")
}

func newID() (string, error) {
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return "backup-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + hex.EncodeToString(random), nil
}

func truncate(text string, limit int) string {
	if len(text) > limit {
		return text[:limit]
	}
	return text
}
