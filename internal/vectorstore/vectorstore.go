// Package vectorstore keeps chunk embeddings in a dedicated vector database.
//
// git-ctx works without one: embeddings are stored next to the text in the
// metadata database and scored in process. That is exact and needs no extra
// infrastructure, but the candidate set comes from the keyword stage, so a
// purely semantic match that shares no term with the query is never scored, and
// the scan grows with the corpus. A vector store fixes both by returning
// approximate nearest neighbours over the whole ref.
//
// Every integration here is optional and best effort. A misconfigured or
// unreachable vector database must never make search fail: the caller falls
// back to the in-database path.
package vectorstore

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Chunk is one embedded document chunk.
type Chunk struct {
	ID           string
	RepositoryID string
	Ref          string
	LibraryID    string
	FilePath     string
	Revision     string
	Vector       []float32
}

// Match is one nearest neighbour with its similarity in [0,1].
type Match struct {
	ID    string
	Score float64
}

// Status describes a live connection for the administration screen.
type Status struct {
	Provider         string `json:"provider"`
	Target           string `json:"target"`
	Database         string `json:"database,omitempty"`
	User             string `json:"user,omitempty"`
	Collection       string `json:"collection"`
	Dimensions       int    `json:"dimensions"`
	Vectors          int64  `json:"vectors"`
	Ready            bool   `json:"ready"`
	Detail           string `json:"detail"`
	ExtensionVersion string `json:"extensionVersion,omitempty"`
	ExtensionSchema  string `json:"extensionSchema,omitempty"`
}

// Store is the minimal contract every vector database integration implements.
type Store interface {
	// Name is the provider identifier used in settings and diagnostics.
	Name() string
	// Ensure creates the table or collection when it does not exist yet.
	Ensure(ctx context.Context, dimensions int) error
	// Upsert replaces the vectors of the given chunks.
	Upsert(ctx context.Context, chunks []Chunk) error
	// DeleteRef removes every vector of one repository ref.
	DeleteRef(ctx context.Context, repositoryID, ref string) error
	// Search returns the nearest neighbours inside one repository ref.
	Search(ctx context.Context, repositoryID, ref, revision string, vector []float32, limit int) ([]Match, error)
	// SearchGlobal returns the nearest neighbours across every repository. The
	// caller still applies the repository ACL, so this must never be exposed
	// directly to a user.
	SearchGlobal(ctx context.Context, revision string, vector []float32, limit int) ([]Match, error)
	// Status reports connectivity and the stored vector count.
	Status(ctx context.Context) (Status, error)
	Close() error
}

// Config is the decoded `vector` administrator setting.
type Config struct {
	Provider       string
	DSN            string
	BaseURL        string
	Collection     string
	Database       string
	Token          string
	Username       string
	Password       string
	Dimensions     int
	TimeoutSeconds int
	TLSVerify      *bool
	CACertificate  string
	ProxyURL       string
}

// ErrNotConfigured reports that no vector database is selected, which is the
// default and a completely valid deployment.
var ErrNotConfigured = errors.New("no vector database is configured")

const (
	// defaultCollection is used when the administrator leaves the name empty.
	defaultCollection = "git_ctx_chunk_vectors"
	// defaultTimeout keeps a slow vector database from blocking a search.
	defaultTimeout = 10 * time.Second
	// upsertBatch bounds one write request.
	upsertBatch = 500
)

// FromMap decodes the stored administrator setting.
func FromMap(settings map[string]any) Config {
	text := func(key string) string {
		value, _ := settings[key].(string)
		return strings.TrimSpace(value)
	}
	number := func(key string) int {
		if value, ok := settings[key].(float64); ok {
			return int(value)
		}
		return 0
	}
	cfg := Config{
		Provider: strings.ToLower(text("provider")), DSN: text("dsn"), BaseURL: text("baseUrl"),
		Collection: text("collection"), Database: text("database"), Token: text("token"),
		Username: text("username"), Password: text("password"),
		Dimensions: number("dimensions"), TimeoutSeconds: number("timeoutSeconds"),
		CACertificate: text("caCertificate"), ProxyURL: text("proxyUrl"),
	}
	if value, ok := settings["tlsVerify"].(bool); ok {
		cfg.TLSVerify = &value
	}
	if cfg.Collection == "" {
		cfg.Collection = defaultCollection
	}
	return cfg
}

// Enabled reports whether the administrator selected a vector database.
func (c Config) Enabled() bool {
	return c.Provider != "" && c.Provider != "none" && c.Provider != "disabled"
}

func (c Config) timeout() time.Duration {
	if c.TimeoutSeconds > 0 {
		return time.Duration(c.TimeoutSeconds) * time.Second
	}
	return defaultTimeout
}

// Open builds the configured store. fallbackDSN is the platform PostgreSQL DSN,
// used by pgvector when the administrator did not provide a separate one, so a
// single-database deployment needs no extra credentials.
func Open(cfg Config, fallbackDSN string) (Store, error) {
	switch cfg.Provider {
	case "", "none", "disabled":
		return nil, ErrNotConfigured
	case "pgvector":
		return newPostgres(cfg, fallbackDSN)
	case "milvus":
		return newMilvus(cfg)
	default:
		return nil, fmt.Errorf("unsupported vector database provider %q", cfg.Provider)
	}
}

// TestConnection performs the setup that an administrator expects from the
// connection-test button. In particular, pgvector may be available on the
// server but not activated in the selected database yet; testing activates it
// before asking Status, rather than rejecting the setting prematurely.
func TestConnection(ctx context.Context, cfg Config, fallbackDSN string) (Status, error) {
	store, err := Open(cfg, fallbackDSN)
	if err != nil {
		return Status{}, err
	}
	defer store.Close()
	if postgres, ok := store.(*postgresStore); ok {
		if err = postgres.activateExtension(ctx); err != nil {
			return postgres.statusContext(ctx, err)
		}
	}
	status, err := store.Status(ctx)
	if err != nil {
		return status, err
	}
	if cfg.Dimensions > 0 {
		if err = store.Ensure(ctx, cfg.Dimensions); err != nil {
			return status, err
		}
		status, err = store.Status(ctx)
	}
	return status, err
}

// Providers lists the values accepted by the provider setting.
func Providers() []string { return []string{"none", "pgvector", "milvus"} }

// identifier validates a table or collection name. Both providers interpolate
// it into a statement or a path, so it may only contain safe characters.
func identifier(name string) (string, error) {
	if name == "" {
		name = defaultCollection
	}
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' {
			continue
		}
		return "", fmt.Errorf("collection name %q may only contain letters, digits and underscore", name)
	}
	return name, nil
}

// literal renders a vector as the textual form both PostgreSQL and JSON accept,
// which avoids depending on a driver specific vector type.
func literal(vector []float32) string {
	var b strings.Builder
	b.WriteByte('[')
	for index, value := range vector {
		if index > 0 {
			b.WriteByte(',')
		}
		b.WriteString(strconv.FormatFloat(float64(value), 'f', -1, 32))
	}
	b.WriteByte(']')
	return b.String()
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
