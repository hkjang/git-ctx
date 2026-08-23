package app

import (
	"context"
	"net/http"
	"time"

	"git-ctx/internal/auth"
	"git-ctx/internal/backup"
)

// Backup and restore endpoints.

func (a *App) backupConfig(ctx context.Context) backup.Config {
	settings, err := a.loadSettingMap(ctx, "backup")
	if err != nil {
		settings = map[string]any{}
	}
	return backupConfigFromMap(settings, a.cfg.BackupDirectory)
}
func backupConfigFromMap(settings map[string]any, defaultDirectory string) backup.Config {
	cfg := backup.Config{Directory: defaultDirectory, Interval: 24 * time.Hour, RetentionCount: 7, MaxBytes: 512 << 20}
	cfg.Enabled, _ = settings["enabled"].(bool)
	if value, ok := settings["directory"].(string); ok && value != "" {
		cfg.Directory = value
	}
	if value, ok := settings["intervalHours"].(float64); ok && value > 0 {
		cfg.Interval = time.Duration(value * float64(time.Hour))
	}
	if value, ok := settings["retentionCount"].(float64); ok {
		cfg.RetentionCount = int(value)
	}
	if value, ok := settings["maxBytes"].(float64); ok {
		cfg.MaxBytes = int64(value)
	}
	return cfg
}
func (a *App) listBackups(w http.ResponseWriter, r *http.Request) {
	records, err := a.backup.List(r.Context())
	if err != nil {
		problem(w, 500, "backup_list_failed", err.Error())
		return
	}
	jsonOut(w, http.StatusOK, records)
}
func (a *App) createBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	record, err := a.backup.Create(r.Context(), p.UserID, "manual")
	if err != nil {
		a.audit(r, p, "backup.create", "backup", "", "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 500, "backup_create_failed", err.Error())
		return
	}
	a.audit(r, p, "backup.create", "backup", record.ID, "success", map[string]any{"sizeBytes": record.SizeBytes, "sha256": record.SHA256})
	jsonOut(w, http.StatusCreated, record)
}
func (a *App) downloadBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	file, record, err := a.backup.Open(r.Context(), r.PathValue("id"))
	if err != nil {
		problem(w, 404, "backup_unavailable", "Backup does not exist or failed integrity verification")
		return
	}
	defer file.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+record.Filename+`"`)
	w.Header().Set("X-Content-SHA256", record.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	a.audit(r, p, "backup.download", "backup", record.ID, "success", map[string]any{"sizeBytes": record.SizeBytes})
	http.ServeContent(w, r, record.Filename, record.CreatedAt, file)
}
func (a *App) restoreBackup(w http.ResponseWriter, r *http.Request) {
	p, _ := auth.FromContext(r.Context())
	id := r.PathValue("id")
	if r.Header.Get("X-Restore-Confirmation") != "RESTORE "+id {
		problem(w, 400, "restore_confirmation_required", "Exact restore confirmation is required")
		return
	}
	a.requestGate.Lock()
	a.stopBackground()
	err := a.backup.Restore(r.Context(), id)
	a.startBackground()
	a.requestGate.Unlock()
	if err != nil {
		a.audit(r, p, "backup.restore", "backup", id, "failure", map[string]any{"error": truncateText(err.Error(), 500)})
		problem(w, 400, "backup_restore_failed", err.Error())
		return
	}
	a.audit(r, p, "backup.restore", "backup", id, "success", nil)
	jsonOut(w, http.StatusOK, map[string]any{"id": id, "status": "restored", "sessionsInvalidated": true})
}
