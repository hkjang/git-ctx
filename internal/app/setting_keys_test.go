package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"git-ctx/internal/config"
)

// Settings keep fields this build does not read, so a newer console can store
// something an older server has not learned about yet. That forward
// compatibility hid a plain mistake: sending tracingEnabled instead of enabled
// turns tracing off and answers 200 with "applied": true. Nothing in the
// response says the key that mattered was never read, and the traces an
// operator went looking for never arrive.
func TestASettingKeyThisBuildDoesNotReadIsNamed(t *testing.T) {
	ctx := context.Background()
	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, "settings.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	// Saving with tracing on runs a live export test against the endpoint, so
	// there has to be something listening.
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-protobuf")
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	save := func(body string) map[string]any {
		t.Helper()
		request := httptest.NewRequest(http.MethodPut, "/api/v1/admin/settings/observability", strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}

	mistyped := save(`{"tracingEnabled":true,"otlpEndpoint":"` + collector.URL + `","serviceName":"probe","allowInsecureLocalhost":true}`)
	ignored, _ := mistyped["ignoredFields"].([]any)
	if len(ignored) != 1 || ignored[0] != "tracingEnabled" {
		t.Fatalf("the misspelled key was accepted without a word: %#v", mistyped)
	}
	if _, present := mistyped["warning"]; !present {
		t.Errorf("the response carries no warning for a key that was not read: %#v", mistyped)
	}

	// The field is still stored: a newer console must be able to write one this
	// build has not learned yet.
	stored, err := a.loadSettingMap(ctx, "observability")
	if err != nil {
		t.Fatal(err)
	}
	if _, kept := stored["tracingEnabled"]; !kept {
		t.Error("the unknown field was dropped; forward compatibility is the reason it is kept")
	}

	// A payload this build understands says nothing extra.
	clean := save(`{"enabled":true,"otlpEndpoint":"` + collector.URL + `","serviceName":"probe","allowInsecureLocalhost":true}`)
	if _, present := clean["ignoredFields"]; present {
		t.Fatalf("a payload made only of known keys was reported as carrying unknown ones: %#v", clean)
	}
}
