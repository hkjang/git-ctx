package observability

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"git-ctx/internal/netclient"
	"git-ctx/internal/version"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Config struct {
	Enabled                 bool
	Endpoint, ServiceName   string
	SampleRatio             float64
	Headers                 map[string]string
	Timeout                 time.Duration
	TLSVerify               *bool
	CACertificate, ProxyURL string
	AllowInsecureLocalhost  bool
}

type Manager struct {
	mu       sync.Mutex
	provider *sdktrace.TracerProvider
}

func New() *Manager {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetTracerProvider(trace.NewNoopTracerProvider())
	return &Manager{}
}

func Validate(ctx context.Context, cfg Config) error {
	if !cfg.Enabled {
		return nil
	}
	// A configured zero sampling ratio must not bypass the administrator's
	// endpoint/TLS/authentication connection test.
	configuredRatio := cfg.SampleRatio
	cfg.SampleRatio = 1
	provider, err := build(ctx, cfg)
	if err != nil {
		return err
	}
	_, span := provider.Tracer("git-ctx/config-test").Start(ctx, "observability.connection-test",
		trace.WithAttributes(attribute.Float64("git_ctx.configured_sample_ratio", configuredRatio)))
	span.End()
	if err = provider.ForceFlush(ctx); err != nil {
		_ = provider.Shutdown(context.Background())
		return fmt.Errorf("OTLP export test: %w", err)
	}
	return provider.Shutdown(ctx)
}

func (m *Manager) Apply(ctx context.Context, cfg Config) error {
	var next *sdktrace.TracerProvider
	if cfg.Enabled {
		provider, err := build(ctx, cfg)
		if err != nil {
			return err
		}
		next = provider
		otel.SetTracerProvider(provider)
	} else {
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
	}
	m.mu.Lock()
	previous := m.provider
	m.provider = next
	m.mu.Unlock()
	if previous != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return previous.Shutdown(shutdownCtx)
	}
	return nil
}

func (m *Manager) ForceFlush(ctx context.Context) error {
	m.mu.Lock()
	provider := m.provider
	m.mu.Unlock()
	if provider == nil {
		return nil
	}
	return provider.ForceFlush(ctx)
}

func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.provider != nil
}

func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	provider := m.provider
	m.provider = nil
	m.mu.Unlock()
	otel.SetTracerProvider(trace.NewNoopTracerProvider())
	if provider == nil {
		return nil
	}
	return provider.Shutdown(ctx)
}

func build(ctx context.Context, cfg Config) (*sdktrace.TracerProvider, error) {
	if err := validate(cfg); err != nil {
		return nil, err
	}
	client, err := netclient.New(netclient.Config{Timeout: cfg.Timeout, TLSVerify: cfg.TLSVerify, CACertificate: cfg.CACertificate, ProxyURL: cfg.ProxyURL})
	if err != nil {
		return nil, err
	}
	options := []otlptracehttp.Option{
		otlptracehttp.WithEndpointURL(cfg.Endpoint),
		otlptracehttp.WithHTTPClient(client),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	}
	if len(cfg.Headers) > 0 {
		options = append(options, otlptracehttp.WithHeaders(cfg.Headers))
	}
	exporter, err := otlptracehttp.New(ctx, options...)
	if err != nil {
		return nil, err
	}
	res := resource.NewSchemaless(attribute.String("service.name", cfg.ServiceName), attribute.String("service.version", version.Version))
	sampler := sdktrace.ParentBased(sdktrace.TraceIDRatioBased(cfg.SampleRatio))
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res), sdktrace.WithSampler(sampler)), nil
}

func validate(cfg Config) error {
	parsed, err := url.Parse(cfg.Endpoint)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return errors.New("observability.otlpEndpoint must be an absolute HTTP(S) URL")
	}
	if parsed.Scheme != "https" {
		host := strings.ToLower(parsed.Hostname())
		if !cfg.AllowInsecureLocalhost || (host != "localhost" && host != "127.0.0.1" && host != "::1") {
			return errors.New("observability.otlpEndpoint must use HTTPS outside an explicitly allowed localhost test")
		}
	}
	if cfg.ServiceName == "" || len(cfg.ServiceName) > 128 {
		return errors.New("observability.serviceName is required and must not exceed 128 characters")
	}
	if cfg.SampleRatio < 0 || cfg.SampleRatio > 1 {
		return errors.New("observability.sampleRatio must be between 0 and 1")
	}
	if cfg.Timeout <= 0 {
		return errors.New("observability.timeoutSeconds must be positive")
	}
	for key, value := range cfg.Headers {
		if key == "" || strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
			return errors.New("observability headers contain an invalid name or value")
		}
	}
	return nil
}
