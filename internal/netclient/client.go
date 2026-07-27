package netclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"time"
)

type Config struct {
	Timeout       time.Duration
	TLSVerify     *bool
	CACertificate string
	ProxyURL      string
}

func New(cfg Config) (*http.Client, error) {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	verify := true
	if cfg.TLSVerify != nil {
		verify = *cfg.TLSVerify
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if cfg.CACertificate != "" && !roots.AppendCertsFromPEM([]byte(cfg.CACertificate)) {
		return nil, errors.New("CA certificate does not contain a valid PEM certificate")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: roots, InsecureSkipVerify: !verify} // #nosec G402 -- explicit administrator-controlled on-prem option.
	if cfg.ProxyURL != "" {
		proxy, err := url.Parse(cfg.ProxyURL)
		if err != nil {
			return nil, errors.New("invalid proxy URL")
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	return &http.Client{Transport: transport, Timeout: cfg.Timeout}, nil
}
