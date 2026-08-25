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
	"unicode/utf8"

	"git-ctx/internal/store"
)

const (
	magic                   = "GCTXBACKUP1\n"
	snapshotStringChunkSize = 32 << 10
)

var errLogicalPayloadTooLarge = errors.New("backup logical payload exceeds configured maximum")

var tables = []string{
	"users", "roles", "user_identities", "user_roles",
	"api_keys", "api_key_restrictions", "api_key_usage_buckets",
	"repositories", "repository_permissions", "repository_index_policies", "repository_files", "document_chunks", "repository_ref_states", "repository_ref_changes", "search_projection_states",
	"code_symbols", "code_dependencies", "repository_maps", "repository_packages",
	"context_packs", "context_pack_items",
	"quality_benchmark_cases", "quality_benchmark_runs", "quality_benchmark_results",
	"system_settings", "setting_versions", "audit_logs", "mcp_calls", "mcp_call_steps", "index_jobs",
	"webhook_events", "index_security_events", "mcp_tools", "notifications", "notification_deliveries",
	"managed_secrets", "managed_secret_versions",
}

// legacyV1Tables is the exact table set written by releases before
// repository_files and mcp_call_steps were added to logical backups. Those
// tables already existed in the database, so restoring such an archive safely
// leaves them empty rather than rejecting an otherwise compatible backup.
var legacyV1Tables = withoutTables(tables, "repository_files", "mcp_call_steps")

func withoutTables(input []string, omitted ...string) []string {
	out := make([]string, 0, len(input))
	for _, table := range input {
		skip := false
		for _, name := range omitted {
			if table == name {
				skip = true
				break
			}
		}
		if !skip {
			out = append(out, table)
		}
	}
	return out
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
	payload, expandedBytes, err := s.snapshotWithLimit(ctx, cfg.MaxBytes)
	if err != nil {
		return record, err
	}
	// MaxBytes is a restore-safety bound as well as an on-disk archive bound.
	// A highly compressible logical snapshot can be tiny on disk but expand far
	// beyond the amount Restore is allowed to read. Reject it before recording a
	// completed backup so every completed archive is restorable under the same
	// configuration.
	if expandedBytes > cfg.MaxBytes {
		return record, fmt.Errorf("backup logical payload exceeds configured maximum of %d bytes", cfg.MaxBytes)
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

func (s *Service) snapshot(ctx context.Context) ([]byte, int64, error) {
	return s.snapshotWithLimit(ctx, 0)
}

// snapshotWithLimit writes the v1 logical JSON directly into gzip. It keeps
// only the current SQL row and the compressed output in memory; maxLogicalBytes
// bounds the uncompressed JSON accepted by Restore. A zero limit is used by
// in-process logical migration, whose caller has no backup size configuration.
func (s *Service) snapshotWithLimit(ctx context.Context, maxLogicalBytes int64) ([]byte, int64, error) {
	options := &sql.TxOptions{ReadOnly: true}
	if s.store.Driver() == "postgres" {
		options.Isolation = sql.LevelRepeatableRead
	}
	tx, err := s.store.DB.BeginTx(ctx, options)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback()
	var compressed bytes.Buffer
	gzipWriter := gzip.NewWriter(&compressed)
	logicalWriter := &logicalLimitWriter{destination: gzipWriter, limit: maxLogicalBytes}
	if err = s.writeSnapshotJSON(ctx, tx, logicalWriter); err != nil {
		return nil, logicalWriter.written, err
	}
	if err = gzipWriter.Close(); err != nil {
		return nil, logicalWriter.written, err
	}
	if err = tx.Commit(); err != nil {
		return nil, logicalWriter.written, err
	}
	return compressed.Bytes(), logicalWriter.written, nil
}

type logicalLimitWriter struct {
	destination io.Writer
	limit       int64
	written     int64
	scratch     [snapshotStringChunkSize]byte
}

func (w *logicalLimitWriter) Write(input []byte) (int, error) {
	if len(input) == 0 {
		return 0, nil
	}
	allowed := len(input)
	exceeded := false
	if w.limit > 0 {
		remaining := w.limit - w.written
		if remaining <= 0 {
			return 0, w.limitError()
		}
		if int64(allowed) > remaining {
			allowed = int(remaining)
			exceeded = true
		}
	}
	n, err := w.destination.Write(input[:allowed])
	w.written += int64(n)
	if err != nil {
		return n, err
	}
	if n != allowed {
		return n, io.ErrShortWrite
	}
	if exceeded {
		return n, w.limitError()
	}
	return n, nil
}

// WriteString copies through a fixed-size scratch buffer so a large TEXT value
// is never converted into a second, equally large []byte before compression.
func (w *logicalLimitWriter) WriteString(input string) (int, error) {
	total := 0
	for len(input) > 0 {
		size := min(len(input), len(w.scratch))
		copy(w.scratch[:size], input[:size])
		n, err := w.Write(w.scratch[:size])
		total += n
		if err != nil {
			return total, err
		}
		input = input[size:]
	}
	return total, nil
}

func (w *logicalLimitWriter) limitError() error {
	return fmt.Errorf("%w of %d bytes", errLogicalPayloadTooLarge, w.limit)
}

func (s *Service) writeSnapshotJSON(ctx context.Context, tx *sql.Tx, writer *logicalLimitWriter) error {
	write := func(text string) error {
		_, err := writer.WriteString(text)
		return err
	}
	writeString := func(text string) error {
		return writeJSONString(writer, text)
	}
	if err := write(`{"Format":`); err != nil {
		return err
	}
	if err := writeString("git-ctx-logical-v1"); err != nil {
		return err
	}
	if err := write(`,"SourceDriver":`); err != nil {
		return err
	}
	if err := writeString(s.store.Driver()); err != nil {
		return err
	}
	if err := write(`,"CreatedAt":`); err != nil {
		return err
	}
	if err := writeString(time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
		return err
	}
	if err := write(`,"Migrations":[`); err != nil {
		return err
	}
	migrations, err := tx.QueryContext(ctx, `SELECT version FROM schema_migrations ORDER BY version`)
	if err != nil {
		return err
	}
	firstMigration := true
	for migrations.Next() {
		var version string
		if err = migrations.Scan(&version); err != nil {
			migrations.Close()
			return err
		}
		if !firstMigration {
			if err = write(","); err != nil {
				migrations.Close()
				return err
			}
		}
		firstMigration = false
		if err = writeString(version); err != nil {
			migrations.Close()
			return err
		}
	}
	if err = migrations.Err(); err != nil {
		migrations.Close()
		return err
	}
	if err = migrations.Close(); err != nil {
		return err
	}
	if err = write(`],"Tables":[`); err != nil {
		return err
	}
	for tableIndex, table := range tables {
		// The column list is read first so the derived ones can be left out of
		// the query itself, rather than exported and discarded.
		probe, queryErr := tx.QueryContext(ctx, `SELECT * FROM "`+table+`" WHERE 1=0`)
		if queryErr != nil {
			return fmt.Errorf("read %s: %w", table, queryErr)
		}
		all, queryErr := probe.Columns()
		probe.Close()
		if queryErr != nil {
			return queryErr
		}
		columns := carriedColumns(table, all)
		rows, queryErr := tx.QueryContext(ctx, `SELECT `+quotedColumns(columns)+` FROM "`+table+`"`)
		if queryErr != nil {
			return fmt.Errorf("read %s: %w", table, queryErr)
		}
		if tableIndex > 0 {
			if queryErr = write(","); queryErr != nil {
				rows.Close()
				return queryErr
			}
		}
		if queryErr = write(`{"Name":`); queryErr == nil {
			queryErr = writeString(table)
		}
		if queryErr == nil {
			queryErr = write(`,"ColumnsCSV":`)
		}
		if queryErr == nil {
			queryErr = writeString(strings.Join(columns, ","))
		}
		if queryErr == nil {
			queryErr = write(`,"Rows":[`)
		}
		if queryErr != nil {
			rows.Close()
			return queryErr
		}
		raw := make([]any, len(columns))
		pointers := make([]any, len(columns))
		for index := range raw {
			pointers[index] = &raw[index]
		}
		firstRow := true
		for rows.Next() {
			if queryErr = rows.Scan(pointers...); queryErr != nil {
				rows.Close()
				return queryErr
			}
			if !firstRow {
				if queryErr = write(","); queryErr != nil {
					rows.Close()
					return queryErr
				}
			}
			firstRow = false
			if queryErr = write("["); queryErr != nil {
				rows.Close()
				return queryErr
			}
			for columnIndex := range raw {
				if columnIndex > 0 {
					if queryErr = write(","); queryErr != nil {
						rows.Close()
						return queryErr
					}
				}
				encoded, encodeErr := encodeValue(raw[columnIndex])
				if encodeErr != nil {
					rows.Close()
					return fmt.Errorf("encode %s.%s: %w", table, columns[columnIndex], encodeErr)
				}
				if queryErr = writeSnapshotValue(writer, encoded); queryErr != nil {
					rows.Close()
					return fmt.Errorf("encode %s.%s: %w", table, columns[columnIndex], queryErr)
				}
			}
			if queryErr = write("]"); queryErr != nil {
				rows.Close()
				return queryErr
			}
		}
		if queryErr = rows.Err(); queryErr != nil {
			rows.Close()
			return queryErr
		}
		if queryErr = rows.Close(); queryErr != nil {
			return queryErr
		}
		if queryErr = write("]}"); queryErr != nil {
			return queryErr
		}
	}
	return write("]}")
}

func writeSnapshotValue(writer *logicalLimitWriter, item value) error {
	if _, err := writer.WriteString(`{"Kind":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, item.Kind); err != nil {
		return err
	}
	if _, err := writer.WriteString(`,"Data":`); err != nil {
		return err
	}
	if err := writeJSONString(writer, item.Data); err != nil {
		return err
	}
	_, err := writer.WriteString("}")
	return err
}

// writeJSONString emits the same decoded value as encoding/json without
// allocating an escaped copy of the complete SQL value.
func writeJSONString(writer *logicalLimitWriter, input string) error {
	if _, err := writer.WriteString(`"`); err != nil {
		return err
	}
	start := 0
	for index := 0; index < len(input); {
		current := input[index]
		if current < utf8.RuneSelf {
			escape := ""
			switch current {
			case '\\':
				escape = `\\`
			case '"':
				escape = `\"`
			case '\b':
				escape = `\b`
			case '\f':
				escape = `\f`
			case '\n':
				escape = `\n`
			case '\r':
				escape = `\r`
			case '\t':
				escape = `\t`
			case '<':
				escape = `\u003c`
			case '>':
				escape = `\u003e`
			case '&':
				escape = `\u0026`
			}
			if escape == "" && current >= 0x20 {
				index++
				continue
			}
			if start < index {
				if _, err := writer.WriteString(input[start:index]); err != nil {
					return err
				}
			}
			if escape != "" {
				if _, err := writer.WriteString(escape); err != nil {
					return err
				}
			} else {
				hex := "0123456789abcdef"
				encoded := []byte{'\\', 'u', '0', '0', hex[current>>4], hex[current&0x0f]}
				if _, err := writer.Write(encoded); err != nil {
					return err
				}
			}
			index++
			start = index
			continue
		}
		runeValue, size := utf8.DecodeRuneInString(input[index:])
		if runeValue == utf8.RuneError && size == 1 {
			if start < index {
				if _, err := writer.WriteString(input[start:index]); err != nil {
					return err
				}
			}
			if _, err := writer.WriteString(`\ufffd`); err != nil {
				return err
			}
			index++
			start = index
			continue
		}
		if runeValue == '\u2028' || runeValue == '\u2029' {
			if start < index {
				if _, err := writer.WriteString(input[start:index]); err != nil {
					return err
				}
			}
			if runeValue == '\u2028' {
				_, err := writer.WriteString(`\u2028`)
				if err != nil {
					return err
				}
			} else {
				_, err := writer.WriteString(`\u2029`)
				if err != nil {
					return err
				}
			}
			index += size
			start = index
			continue
		}
		index += size
	}
	if start < len(input) {
		if _, err := writer.WriteString(input[start:]); err != nil {
			return err
		}
	}
	_, err := writer.WriteString(`"`)
	return err
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
	payload, _, err := sourceService.snapshot(ctx)
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

// derivedColumns are values the database computes for itself. They are an index
// in disguise — a search vector, not a fact — so a backup neither carries them
// nor is judged against them: PostgreSQL refuses to be given a generated
// column, and a schema check that counted one would reject every archive taken
// on the other engine.
var derivedColumns = map[string]map[string]bool{
	"document_chunks": {"search_vector": true},
}

// carriedColumns drops the derived columns from a table's column list.
func carriedColumns(table string, columns []string) []string {
	derived := derivedColumns[table]
	if len(derived) == 0 {
		return columns
	}
	kept := make([]string, 0, len(columns))
	for _, column := range columns {
		if !derived[column] {
			kept = append(kept, column)
		}
	}
	return kept
}

func (s *Service) validateArchive(ctx context.Context, a archive) error {
	actualTables := make([]string, len(a.Tables))
	for index := range a.Tables {
		actualTables[index] = a.Tables[index].Name
	}
	actualSet := strings.Join(actualTables, "\n")
	if actualSet != strings.Join(tables, "\n") && actualSet != strings.Join(legacyV1Tables, "\n") {
		return errors.New("backup table set does not match this git-ctx version")
	}
	for _, data := range a.Tables {
		columns, err := s.store.DB.QueryContext(ctx, `SELECT * FROM "`+data.Name+`" WHERE 1=0`)
		if err != nil {
			return err
		}
		current, err := columns.Columns()
		columns.Close()
		if err == nil {
			current = carriedColumns(data.Name, current)
		}
		if err != nil || strings.Join(current, ",") != data.ColumnsCSV {
			return fmt.Errorf("backup schema for %s does not match", data.Name)
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
	nonceStart := len(magic)
	nonceEnd := nonceStart + s.aead.NonceSize()
	output := make([]byte, nonceEnd, nonceEnd+len(payload)+s.aead.Overhead())
	copy(output, magic)
	nonce := output[nonceStart:nonceEnd]
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return s.aead.Seal(output, nonce, payload, []byte(magic)), nil
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
	if len(text) <= limit {
		return text
	}
	// Cut on a rune boundary so a truncated message stays valid UTF-8.
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut]
}
