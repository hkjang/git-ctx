package vectorstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"git-ctx/internal/netclient"
)

// milvusStore talks to the Milvus RESTful v2 API. The HTTP surface is used
// deliberately: the official SDK pulls in a large gRPC dependency tree, and this
// platform ships as one offline image where every module has to be vendored and
// audited.
type milvusStore struct {
	base       *url.URL
	client     *http.Client
	collection string
	database   string
	token      string
	dimensions int
}

func newMilvus(cfg Config) (Store, error) {
	if strings.TrimSpace(cfg.BaseURL) == "" {
		return nil, errors.New("milvus needs a base URL, for example http://milvus:19530")
	}
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("milvus baseUrl must be an absolute URL")
	}
	collection, err := identifier(cfg.Collection)
	if err != nil {
		return nil, err
	}
	client, err := netclient.New(netclient.Config{
		Timeout: cfg.timeout(), TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL,
	})
	if err != nil {
		return nil, err
	}
	token := cfg.Token
	if token == "" && cfg.Username != "" {
		token = cfg.Username + ":" + cfg.Password
	}
	database := cfg.Database
	if database == "" {
		database = "default"
	}
	return &milvusStore{base: base, client: client, collection: collection, database: database, token: token, dimensions: cfg.Dimensions}, nil
}

func (m *milvusStore) Name() string { return "milvus" }

// call posts one Milvus v2 request and reports the API level error, which is
// carried in the body rather than the HTTP status.
func (m *milvusStore) call(ctx context.Context, path string, payload map[string]any, out any) error {
	if payload == nil {
		payload = map[string]any{}
	}
	if _, ok := payload["dbName"]; !ok {
		payload["dbName"] = m.database
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	endpoint := *m.base
	endpoint.Path = strings.TrimSuffix(m.base.Path, "/") + path
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if m.token != "" {
		request.Header.Set("Authorization", "Bearer "+m.token)
	}
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 32<<20))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("milvus %s: %s", response.Status, truncate(string(raw), 400))
	}
	var envelope struct {
		Code    int             `json:"code"`
		Message string          `json:"message"`
		Data    json.RawMessage `json:"data"`
	}
	if err = json.Unmarshal(raw, &envelope); err != nil {
		return fmt.Errorf("milvus returned an unreadable response: %w", err)
	}
	if envelope.Code != 0 {
		return fmt.Errorf("milvus error %d: %s", envelope.Code, envelope.Message)
	}
	if out != nil && len(envelope.Data) > 0 {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func (m *milvusStore) Ensure(ctx context.Context, dimensions int) error {
	if dimensions <= 0 {
		dimensions = m.dimensions
	}
	if dimensions <= 0 {
		return errors.New("vector dimensions are unknown; index a repository first or set dimensions explicitly")
	}
	m.dimensions = dimensions
	var existing []string
	if err := m.call(ctx, "/v2/vectordb/collections/list", nil, &existing); err != nil {
		return err
	}
	for _, name := range existing {
		if name == m.collection {
			return nil
		}
	}
	// The quick-setup schema stores the id as the primary key and keeps the
	// scalar fields dynamic, which is enough for filtering by repository and ref.
	return m.call(ctx, "/v2/vectordb/collections/create", map[string]any{
		"collectionName":     m.collection,
		"dimension":          dimensions,
		"metricType":         "COSINE",
		"idType":             "VarChar",
		"primaryFieldName":   "id",
		"vectorFieldName":    "vector",
		"params":             map[string]any{"max_length": 512},
		"enableDynamicField": true,
	}, nil)
}

func (m *milvusStore) Upsert(ctx context.Context, chunks []Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	if err := m.Ensure(ctx, len(chunks[0].Vector)); err != nil {
		return err
	}
	for start := 0; start < len(chunks); start += upsertBatch {
		end := min(start+upsertBatch, len(chunks))
		data := make([]map[string]any, 0, end-start)
		for _, chunk := range chunks[start:end] {
			data = append(data, map[string]any{
				"id": chunk.ID, "vector": chunk.Vector, "repository_id": chunk.RepositoryID,
				"ref_name": chunk.Ref, "library_id": chunk.LibraryID, "file_path": chunk.FilePath,
			})
		}
		if err := m.call(ctx, "/v2/vectordb/entities/upsert", map[string]any{
			"collectionName": m.collection, "data": data,
		}, nil); err != nil {
			return err
		}
	}
	return nil
}

func (m *milvusStore) DeleteRef(ctx context.Context, repositoryID, ref string) error {
	return m.call(ctx, "/v2/vectordb/entities/delete", map[string]any{
		"collectionName": m.collection,
		"filter":         fmt.Sprintf("repository_id == %q and ref_name == %q", repositoryID, ref),
	}, nil)
}

func (m *milvusStore) Search(ctx context.Context, repositoryID, ref string, vector []float32, limit int) ([]Match, error) {
	if len(vector) == 0 {
		return nil, errors.New("query vector is empty")
	}
	if limit < 1 {
		limit = 20
	}
	var results []struct {
		ID       string  `json:"id"`
		Distance float64 `json:"distance"`
	}
	err := m.call(ctx, "/v2/vectordb/entities/search", map[string]any{
		"collectionName": m.collection,
		"data":           [][]float32{vector},
		"annsField":      "vector",
		"limit":          limit,
		"filter":         fmt.Sprintf("repository_id == %q and ref_name == %q", repositoryID, ref),
		"outputFields":   []string{"id"},
	}, &results)
	if err != nil {
		return nil, err
	}
	out := make([]Match, 0, len(results))
	for _, item := range results {
		// COSINE distance in Milvus is the similarity itself.
		out = append(out, Match{ID: item.ID, Score: item.Distance})
	}
	return out, nil
}

func (m *milvusStore) Status(ctx context.Context) (Status, error) {
	status := Status{Provider: "milvus", Target: m.base.String(), Collection: m.collection, Dimensions: m.dimensions}
	var collections []string
	if err := m.call(ctx, "/v2/vectordb/collections/list", nil, &collections); err != nil {
		status.Detail = err.Error()
		return status, err
	}
	status.Ready = true
	status.Detail = "connected"
	for _, name := range collections {
		if name != m.collection {
			continue
		}
		var stats struct {
			RowCount any `json:"rowCount"`
		}
		if err := m.call(ctx, "/v2/vectordb/collections/get_stats", map[string]any{"collectionName": m.collection}, &stats); err == nil {
			switch value := stats.RowCount.(type) {
			case float64:
				status.Vectors = int64(value)
			case string:
				var parsed int64
				_, _ = fmt.Sscanf(value, "%d", &parsed)
				status.Vectors = parsed
			}
		}
		return status, nil
	}
	status.Detail = "collection is not created yet; it is created on the first projection"
	return status, nil
}

func (m *milvusStore) Close() error { return nil }

func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit]
}
