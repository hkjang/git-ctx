package netclient

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/http"
	"net/url"
	"time"
)

// idleConnsPerHost keeps the pool wide enough for the fan-out these clients
// actually do. Every integration talks to one host -- one Bitbucket, one GitLab
// -- while search queries sources six at a time and resolves ACLs eight at a
// time, on top of the indexer. Go's default of two idle connections per host
// meant most of each batch was closed and re-handshaked on the next one, which
// over TLS costs a round trip per request that reuse avoids.
const idleConnsPerHost = 32

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
	transport.MaxIdleConnsPerHost = idleConnsPerHost
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
