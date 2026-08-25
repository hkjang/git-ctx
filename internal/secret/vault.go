package secret

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"git-ctx/internal/netclient"
)

var safeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

type VaultConfig struct {
	BaseURL, Token, Namespace, Mount, Prefix string
	Timeout                                  time.Duration
	TLSVerify                                *bool
	CACertificate, ProxyURL                  string
}

type Vault struct {
	cfg  VaultConfig
	http *http.Client
}

func NewVault(cfg VaultConfig) (*Vault, error) {
	cfg.BaseURL = strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/")
	cfg.Mount = strings.Trim(cfg.Mount, "/")
	cfg.Prefix = strings.Trim(cfg.Prefix, "/")
	if cfg.Mount == "" {
		cfg.Mount = "secret"
	}
	if cfg.Prefix == "" {
		cfg.Prefix = "git-ctx"
	}
	u, err := url.Parse(cfg.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("vault.baseUrl must be an absolute HTTP(S) URL")
	}
	if cfg.Token == "" {
		return nil, errors.New("vault.token is required")
	}
	if !safeName.MatchString(cfg.Mount) || !safePath(cfg.Prefix) {
		return nil, errors.New("vault mount and prefix must contain safe path segments")
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	return &Vault{cfg: cfg, http: client}, nil
}

func safePath(value string) bool {
	for _, segment := range strings.Split(value, "/") {
		if !safeName.MatchString(segment) {
			return false
		}
	}
	return value != ""
}

func (v *Vault) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, v.cfg.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", v.cfg.Token)
	if v.cfg.Namespace != "" {
		req.Header.Set("X-Vault-Namespace", v.cfg.Namespace)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return v.http.Do(req)
}

func vaultError(resp *http.Response) error {
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return netclient.NewHTTPStatusError(resp.StatusCode,
		fmt.Errorf("vault returned HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
}

func (v *Vault) Validate(ctx context.Context) error {
	resp, err := v.request(ctx, http.MethodGet, "/v1/auth/token/lookup-self", nil)
	if err != nil {
		return fmt.Errorf("vault connection test: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return vaultError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Data map[string]any `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Data == nil {
		return errors.New("vault token lookup returned an invalid response")
	}
	return nil
}

func (v *Vault) path(name string) (string, error) {
	if !safeName.MatchString(name) {
		return "", errors.New("secret name must use letters, numbers, dot, underscore, or hyphen")
	}
	segments := strings.Split(v.cfg.Prefix+"/"+name, "/")
	for i := range segments {
		segments[i] = url.PathEscape(segments[i])
	}
	return "/v1/" + url.PathEscape(v.cfg.Mount) + "/data/" + strings.Join(segments, "/"), nil
}

func (v *Vault) Put(ctx context.Context, name, value string) (int, error) {
	path, err := v.path(name)
	if err != nil {
		return 0, err
	}
	raw, _ := json.Marshal(map[string]any{"data": map[string]string{"value": value}})
	resp, err := v.request(ctx, http.MethodPost, path, bytes.NewReader(raw))
	if err != nil {
		return 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return 0, vaultError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Version int `json:"version"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil || result.Data.Version < 1 {
		return 0, errors.New("vault write returned no version")
	}
	return result.Data.Version, nil
}

func (v *Vault) Get(ctx context.Context, name string) (string, int, error) {
	path, err := v.path(name)
	if err != nil {
		return "", 0, err
	}
	resp, err := v.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return "", 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", 0, vaultError(resp)
	}
	defer resp.Body.Close()
	var result struct {
		Data struct {
			Data     map[string]any `json:"data"`
			Metadata struct {
				Version int `json:"version"`
			} `json:"metadata"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", 0, err
	}
	value, ok := result.Data.Data["value"].(string)
	if !ok {
		return "", 0, errors.New("vault secret has no string value field")
	}
	return value, result.Data.Metadata.Version, nil
}
