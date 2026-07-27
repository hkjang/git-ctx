package secret

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"git-ctx/internal/store"
)

type Cipher func([]byte) ([]byte, error)
type VaultFactory func(context.Context) (*Vault, error)

type Service struct {
	store        *store.Store
	seal, open   Cipher
	vaultFactory VaultFactory
}

type Metadata struct {
	Name      string    `json:"name"`
	Backend   string    `json:"backend"`
	Status    string    `json:"status"`
	VaultPath string    `json:"vaultPath,omitempty"`
	UpdatedBy string    `json:"updatedBy"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

func New(s *store.Store, seal, open Cipher, vaultFactory VaultFactory) *Service {
	return &Service{store: s, seal: seal, open: open, vaultFactory: vaultFactory}
}

func (s *Service) List(ctx context.Context) ([]Metadata, error) {
	rows, err := s.store.DB.QueryContext(ctx, `SELECT name,backend,status,vault_path,version,updated_by,created_at,updated_at FROM managed_secrets ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Metadata
	for rows.Next() {
		var item Metadata
		if err := rows.Scan(&item.Name, &item.Backend, &item.Status, &item.VaultPath, &item.Version, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Service) Put(ctx context.Context, name, backend, value, actor, reason string) (Metadata, error) {
	name = strings.TrimSpace(name)
	if !safeName.MatchString(name) {
		return Metadata{}, errors.New("secret name must use 1..128 letters, numbers, dot, underscore, or hyphen")
	}
	if value == "" {
		return Metadata{}, errors.New("secret value is required")
	}
	var currentBackend string
	var currentVersion int
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT backend,version FROM managed_secrets WHERE name=?`), name).Scan(&currentBackend, &currentVersion)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Metadata{}, err
	}
	if backend == "" {
		if err == nil {
			backend = currentBackend
		} else {
			backend = "database"
		}
	}
	if backend != "database" && backend != "vault" {
		return Metadata{}, errors.New("secret backend must be database or vault")
	}
	if err == nil && currentBackend != backend {
		return Metadata{}, errors.New("secret backend cannot be changed; create a new secret name")
	}
	version := currentVersion + 1
	var sealed []byte
	vaultPath := ""
	if backend == "database" {
		sealed, err = s.seal([]byte(value))
		if err != nil {
			return Metadata{}, err
		}
	} else {
		vault, vaultErr := s.vault(ctx)
		if vaultErr != nil {
			return Metadata{}, vaultErr
		}
		vaultVersion, vaultErr := vault.Put(ctx, name, value)
		if vaultErr != nil {
			return Metadata{}, vaultErr
		}
		vaultPath = name + "#v" + fmt.Sprint(vaultVersion)
	}
	tx, err := s.store.DB.BeginTx(ctx, nil)
	if err != nil {
		return Metadata{}, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO managed_secret_versions(name,version,backend,value_encrypted,vault_path,changed_by,reason) VALUES(?,?,?,?,?,?,?)`), name, version, backend, nullableBytes(sealed), vaultPath, actor, reason); err == nil {
		_, err = tx.ExecContext(ctx, s.store.Rebind(`INSERT INTO managed_secrets(name,backend,value_encrypted,vault_path,version,status,updated_by,updated_at) VALUES(?,?,?,?,?,'active',?,?) ON CONFLICT(name) DO UPDATE SET value_encrypted=excluded.value_encrypted,vault_path=excluded.vault_path,version=excluded.version,status='active',updated_by=excluded.updated_by,updated_at=excluded.updated_at`), name, backend, nullableBytes(sealed), vaultPath, version, actor, time.Now().UTC())
	}
	if err != nil {
		return Metadata{}, err
	}
	if err = tx.Commit(); err != nil {
		return Metadata{}, err
	}
	return s.metadata(ctx, name)
}

func nullableBytes(value []byte) any {
	if len(value) == 0 {
		return nil
	}
	return value
}

func (s *Service) Disable(ctx context.Context, name, actor string) error {
	result, err := s.store.DB.ExecContext(ctx, s.store.Rebind(`UPDATE managed_secrets SET status='disabled',updated_by=?,updated_at=? WHERE name=? AND status='active'`), actor, time.Now().UTC(), name)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return errors.New("active secret not found")
	}
	return nil
}

func (s *Service) Get(ctx context.Context, name string) (string, error) {
	var backend, status string
	var sealed []byte
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT backend,status,value_encrypted FROM managed_secrets WHERE name=?`), name).Scan(&backend, &status, &sealed)
	if errors.Is(err, sql.ErrNoRows) || status != "active" {
		return "", errors.New("managed secret is unavailable")
	}
	if err != nil {
		return "", err
	}
	if backend == "database" {
		raw, err := s.open(sealed)
		if err != nil {
			return "", err
		}
		return string(raw), nil
	}
	vault, err := s.vault(ctx)
	if err != nil {
		return "", err
	}
	value, _, err := vault.Get(ctx, name)
	return value, err
}

func (s *Service) Resolve(ctx context.Context, value map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(value))
	for key, item := range value {
		resolved, err := s.resolveValue(ctx, item)
		if err != nil {
			return nil, fmt.Errorf("resolve setting %s: %w", key, err)
		}
		out[key] = resolved
	}
	return out, nil
}

func (s *Service) resolveValue(ctx context.Context, item any) (any, error) {
	switch value := item.(type) {
	case string:
		if strings.HasPrefix(value, "secret://") {
			name := strings.TrimPrefix(value, "secret://")
			if !safeName.MatchString(name) {
				return nil, errors.New("invalid secret reference")
			}
			return s.Get(ctx, name)
		}
		return value, nil
	case map[string]any:
		return s.Resolve(ctx, value)
	case []any:
		out := make([]any, len(value))
		for i := range value {
			resolved, err := s.resolveValue(ctx, value[i])
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return item, nil
	}
}

func (s *Service) metadata(ctx context.Context, name string) (Metadata, error) {
	var item Metadata
	err := s.store.DB.QueryRowContext(ctx, s.store.Rebind(`SELECT name,backend,status,vault_path,version,updated_by,created_at,updated_at FROM managed_secrets WHERE name=?`), name).Scan(&item.Name, &item.Backend, &item.Status, &item.VaultPath, &item.Version, &item.UpdatedBy, &item.CreatedAt, &item.UpdatedAt)
	return item, err
}

func (s *Service) vault(ctx context.Context) (*Vault, error) {
	if s.vaultFactory == nil {
		return nil, errors.New("vault backend is not configured")
	}
	return s.vaultFactory(ctx)
}
