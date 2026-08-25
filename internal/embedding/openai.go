package embedding

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
	"time"

	"git-ctx/internal/netclient"
)

type OpenAIConfig struct {
	BaseURL, Model, APIKey  string
	Timeout                 time.Duration
	TLSVerify               *bool
	CACertificate, ProxyURL string
}
type OpenAI struct {
	endpoint   string
	model, key string
	client     *http.Client
}

func NewOpenAI(cfg OpenAIConfig) (Provider, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("valid embedding baseUrl is required")
	}
	if cfg.Model == "" {
		return nil, errors.New("embedding model is required")
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &OpenAI{endpoint: strings.TrimSuffix(base.String(), "/") + "/v1/embeddings", model: cfg.Model, key: cfg.APIKey, client: client}, nil
}
func (o *OpenAI) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := o.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

// EmbedBatch sends several inputs in one request. Indexing a repository creates
// thousands of chunks, and one HTTP round trip per chunk turns a normal sized
// repository into an hours-long job that fails on the first hiccup.
func (o *OpenAI) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	var lastErr error
	// Embedding endpoints commonly rate limit or briefly fail under load. A few
	// bounded retries keep one transient response from failing a whole index job.
	for attempt := 0; attempt < embeddingAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(embeddingRetryDelay(lastErr, attempt)):
			}
		}
		vectors, err := o.embedOnce(ctx, texts)
		if err == nil {
			return vectors, nil
		}
		lastErr = err
		if !retryableEmbeddingError(err) {
			return nil, err
		}
	}
	return nil, lastErr
}

const (
	embeddingAttempts  = 3
	embeddingRetryBase = 500 * time.Millisecond
)

// embeddingRetryDelay waits out the window the endpoint named, falling back to
// the local backoff when it named none. Retrying inside a window the server
// already published only earns another 429 and burns one of three attempts.
func embeddingRetryDelay(err error, attempt int) time.Duration {
	backoff := time.Duration(1<<attempt) * embeddingRetryBase
	var status *statusError
	if !errors.As(err, &status) || status.header == nil {
		return backoff
	}
	if hint := netclient.RetryDelay(&http.Response{Header: status.header}, attempt); hint > backoff {
		return hint
	}
	return backoff
}

func retryableEmbeddingError(err error) bool {
	var status *statusError
	if errors.As(err, &status) {
		code := status.Status()
		return code == http.StatusTooManyRequests || code >= 500
	}
	// Transport failures (timeout, reset connection) are always worth retrying.
	return true
}

type statusError struct {
	*netclient.HTTPStatusError
	// header is kept so the retry can honour a window the server named. A 429
	// answered on the client's own schedule is just another 429.
	header http.Header
}

func (o *OpenAI) embedOnce(ctx context.Context, texts []string) ([][]float32, error) {
	raw, _ := json.Marshal(map[string]any{"model": o.model, "input": texts})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if o.key != "" {
		req.Header.Set("Authorization", "Bearer "+o.key)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, &statusError{
			HTTPStatusError: netclient.NewHTTPStatusError(resp.StatusCode,
				fmt.Errorf("embedding API %d: %s", resp.StatusCode, string(body))),
			header: resp.Header.Clone(),
		}
	}
	var out struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) != len(texts) {
		return nil, fmt.Errorf("embedding API returned %d vectors for %d inputs", len(out.Data), len(texts))
	}
	vectors := make([][]float32, len(texts))
	for position, item := range out.Data {
		// The index field is authoritative when the server reorders results.
		target := item.Index
		if target < 0 || target >= len(texts) {
			target = position
		}
		if len(item.Embedding) == 0 {
			return nil, errors.New("embedding API returned an empty vector")
		}
		normalize(item.Embedding)
		vectors[target] = item.Embedding
	}
	for _, vector := range vectors {
		if len(vector) == 0 {
			return nil, errors.New("embedding API returned no vector for one input")
		}
	}
	return vectors, nil
}
func (o *OpenAI) EmbeddingMetadata() Metadata {
	return Metadata{Provider: "openai-compatible", Model: o.model, Revision: o.endpoint}
}
