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
	raw, _ := json.Marshal(map[string]any{"model": o.model, "input": text})
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
		return nil, fmt.Errorf("embedding API %s: %s", resp.Status, string(body))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 {
		return nil, errors.New("embedding API returned no vector")
	}
	normalize(out.Data[0].Embedding)
	return out.Data[0].Embedding, nil
}
