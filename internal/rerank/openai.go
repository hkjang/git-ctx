package rerank

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
	endpoint, model, key string
	client               *http.Client
}

func NewOpenAI(cfg OpenAIConfig) (Provider, error) {
	base, err := url.Parse(cfg.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return nil, errors.New("valid reranker baseUrl is required")
	}
	if strings.TrimSpace(cfg.Model) == "" {
		return nil, errors.New("reranker model is required")
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &OpenAI{endpoint: strings.TrimSuffix(base.String(), "/") + "/v1/rerank", model: cfg.Model, key: cfg.APIKey, client: client}, nil
}

func (o *OpenAI) Rerank(ctx context.Context, query string, documents []string) ([]float64, error) {
	if len(documents) == 0 {
		return []float64{}, nil
	}
	raw, _ := json.Marshal(map[string]any{"model": o.model, "query": query, "documents": documents, "top_n": len(documents)})
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
		return nil, fmt.Errorf("reranker API %s: %s", resp.Status, string(body))
	}
	var out struct {
		Results []struct {
			Index          int     `json:"index"`
			RelevanceScore float64 `json:"relevance_score"`
		} `json:"results"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Results) != len(documents) {
		return nil, errors.New("reranker API returned an incomplete result set")
	}
	scores := make([]float64, len(documents))
	seen := make([]bool, len(documents))
	for _, result := range out.Results {
		if result.Index < 0 || result.Index >= len(documents) || seen[result.Index] {
			return nil, errors.New("reranker API returned an invalid document index")
		}
		seen[result.Index] = true
		scores[result.Index] = result.RelevanceScore
	}
	return scores, nil
}
