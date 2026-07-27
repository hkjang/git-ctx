package observability

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
)

func TestValidateAndApplyExportOTLPHTTP(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Path != "/v1/traces" || r.Header.Get("Authorization") != "Bearer test" {
			t.Errorf("unexpected request path=%q authorization=%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/x-protobuf" || r.Header.Get("Content-Encoding") != "gzip" {
			t.Errorf("unexpected OTLP headers: %#v", r.Header)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || len(body) == 0 {
			t.Errorf("empty OTLP request: bytes=%d err=%v", len(body), err)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := Config{Enabled: true, Endpoint: server.URL + "/v1/traces", ServiceName: "git-ctx-test", SampleRatio: 1, Timeout: time.Second, AllowInsecureLocalhost: true, Headers: map[string]string{"Authorization": "Bearer test"}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := Validate(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	manager := New()
	if err := manager.Apply(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if !manager.Enabled() {
		t.Fatal("manager did not report enabled provider")
	}
	_, span := otel.Tracer("test").Start(ctx, "exported-span")
	span.End()
	if err := manager.ForceFlush(ctx); err != nil {
		t.Fatal(err)
	}
	if err := manager.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if manager.Enabled() {
		t.Fatal("manager remained enabled after shutdown")
	}
	if requests.Load() < 2 {
		t.Fatalf("expected validation and runtime exports, got %d", requests.Load())
	}
}

func TestConfigRejectsUnsafeEndpointsAndHeaders(t *testing.T) {
	base := Config{Enabled: true, Endpoint: "http://collector.company/v1/traces", ServiceName: "git-ctx", SampleRatio: 1, Timeout: time.Second}
	if err := validate(base); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("unsafe endpoint accepted: %v", err)
	}
	base.Endpoint = "https://collector.company/v1/traces"
	base.Headers = map[string]string{"Authorization\nInjected": "value"}
	if err := validate(base); err == nil {
		t.Fatal("header injection accepted")
	}
	base.Headers = nil
	base.SampleRatio = 1.1
	if err := validate(base); err == nil {
		t.Fatal("invalid sample ratio accepted")
	}
}
