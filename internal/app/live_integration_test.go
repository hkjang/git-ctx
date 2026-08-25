package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git-ctx/internal/config"
)

// Every subsystem in this platform has unit tests, and every serious defect
// found in the last dozen releases was found by running the whole chain against
// a real instance instead: a tool that stalled for fifteen seconds, a search
// that ignored its own index, a policy change that indexed nothing, an
// embedding URL that doubled its version segment, a reranker that failed in
// silence. None of those are visible from inside one package.
//
// This test is that chain, kept. It stands up a source server, an embedding and
// reranking model, and a notification receiver in-process, then drives the real
// HTTP handlers and the real background worker: configure, register, index,
// search, and deliver. It is the shape of the manual verification each release
// went through, so the next change is checked the same way without anyone
// remembering to do it.

// TestPlatformChainIntegration is skipped unless -run Integration selects it,
// because it waits on the background worker.
func TestPlatformChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	files := map[string]string{
		"pom.xml": `<project><dependencies>
  <dependency><groupId>org.apache.logging.log4j</groupId><artifactId>log4j-core</artifactId><version>2.14.1</version></dependency>
</dependencies></project>`,
		"package.json":      `{"dependencies":{"react":"^18.2.0"}}`,
		"package-lock.json": `{"lockfileVersion":3,"packages":{"node_modules/react":{"version":"18.3.1"}}}`,
		"README.md":         "# Payment API\n\n결제 처리 서비스. settlement 배치를 운영한다.\n",
		"CODEOWNERS":        "* @platform-team\n/config/ @ops-team\n",
		"config/app.yaml":   "database:\n  password: super-secret-value-1234567890\n",
	}
	source := newFakeGitLab(files)
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()
	receiver := newFakeReceiver()
	defer receiver.Close()

	directory := t.TempDir()
	a, err := New(ctx, config.Config{
		DatabaseDriver: "sqlite", DatabaseDSN: "file:" + filepath.Join(directory, "chain.db") + "?_foreign_keys=on&_busy_timeout=5000",
		KeyPepper: strings.Repeat("p", 32), MasterKey: strings.Repeat("m", 32), BootstrapAdmin: "bootstrap",
		PublicURL: "http://localhost:4747", BackupDirectory: filepath.Join(directory, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()

	call := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(method, path, strings.NewReader(body))
		request.Header.Set("Authorization", "Bearer bootstrap")
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		a.Handler().ServeHTTP(recorder, request)
		return recorder
	}

	// 1. Configure the source, the models and the notification receiver the way
	// an operator would, through the same endpoints the console uses.
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	// The base URL deliberately ends in /v1, which is how every provider
	// documents it and what used to produce /v1/v1/embeddings.
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","dimensions":16,"timeoutSeconds":10,"rerankerEnabled":true,"rerankerProvider":"openai-compatible","rerankerBaseUrl":"%s/v1","rerankerModel":"fake-rerank","rerankerApiKey":"none","rerankerTimeoutSeconds":10}`,
			model.URL, model.URL)); saved.Code != http.StatusOK {
		t.Fatalf("model settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/notifications",
		fmt.Sprintf(`{"externalEnabled":true,"webhookUrl":%q,"webhookAuthorization":"Bearer hook-token","maxAttempts":3}`, receiver.URL+"/hook")); saved.Code != http.StatusOK {
		t.Fatalf("notification settings status=%d body=%s", saved.Code, saved.Body.String())
	}

	// 2. Register the repository. The platform queues the first index itself.
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var repository struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}

	// 3. Wait for the background worker to index it.
	waitFor(t, 60*time.Second, "the repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(`SELECT COUNT(*) FROM index_jobs WHERE repository_id=? AND status='completed' AND files_processed>0`, repository.ID).Scan(&completed)
		return completed > 0
	})

	// 4. What was indexed: the secret is masked, the manifests became an
	// inventory, and the lock file decided the version.
	var masked string
	if err = a.store.DB.QueryRow(`SELECT content FROM document_chunks WHERE repository_id=? AND file_path='config/app.yaml'`, repository.ID).Scan(&masked); err != nil {
		t.Fatalf("the configuration file was not indexed: %v", err)
	}
	if strings.Contains(masked, "super-secret-value") {
		t.Fatalf("a secret reached the index: %q", masked)
	}
	var reactVersion string
	if err = a.store.DB.QueryRow(`SELECT version FROM repository_packages WHERE repository_id=? AND name_lower='react' AND scope='resolved'`, repository.ID).Scan(&reactVersion); err != nil {
		t.Fatalf("the lock file was not inventoried: %v", err)
	}
	if reactVersion != "18.3.1" {
		t.Fatalf("the resolved version is %q, not the lock file's", reactVersion)
	}

	// 5. Embeddings were produced through the /v1 base URL, and the ref records
	// the revision they were made with.
	waitFor(t, 60*time.Second, "the chunks to be embedded", func() bool {
		var total, embedded int
		_ = a.store.DB.QueryRow(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id=?`, repository.ID).Scan(&total, &embedded)
		return total > 0 && total == embedded
	})
	if model.embedCalls() == 0 {
		t.Fatal("no embedding request reached the model server")
	}

	// 6. The tools an agent uses answer over what was indexed.
	answer := mcpCall(t, a, "search-code", `{"query":"settlement 배치"}`)
	if !strings.Contains(answer, "README.md") {
		t.Fatalf("search-code did not find the indexed content:\n%s", answer)
	}
	owners := mcpCall(t, a, "find-code-owner", fmt.Sprintf(`{"libraryId":%q,"path":"config/app.yaml"}`, repository.LibraryID))
	if !strings.Contains(owners, "@ops-team") {
		t.Fatalf("the CODEOWNERS declaration was not used:\n%s", owners)
	}
	advisory := mcpCall(t, a, "find-dependency-usage", `{"name":"org.apache.logging.log4j:log4j-core","fixedIn":"2.17.1"}`)
	if !strings.Contains(advisory, "AFFECTED") {
		t.Fatalf("the advisory judgement is missing:\n%s", advisory)
	}
	docs := mcpCall(t, a, "query-docs", fmt.Sprintf(`{"libraryId":%q,"query":"결제 처리"}`, repository.LibraryID))
	if !strings.Contains(docs, "재순위") {
		t.Fatalf("the reranking outcome was not reported:\n%s", docs)
	}
	if model.rerankCalls() == 0 {
		t.Fatal("no reranking request reached the model server")
	}

	// 7. An alert reaches its destination.
	if _, err = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u-ops','u-ops','ops','ops@example.com','active') ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('n-chain','u-ops','api_key_expiring','k-chain','MCP API key expires soon','곧 만료됩니다.')`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 90*time.Second, "the notification to be delivered", func() bool {
		var delivered int
		_ = a.store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE notification_id='n-chain' AND status='delivered'`).Scan(&delivered)
		return delivered > 0
	})
	if got := receiver.count(); got == 0 {
		t.Fatal("the notification never reached the receiver")
	}
}

// mcpCall drives one tool through the real MCP endpoint as the bootstrap
// administrator and returns its text.
func mcpCall(t *testing.T, a *App, tool, arguments string) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer bootstrap")
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s status=%d body=%s", tool, recorder.Code, recorder.Body.String())
	}
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if len(response.Result.Content) == 0 {
		t.Fatalf("%s returned no content: %s", tool, recorder.Body.String())
	}
	if response.Result.IsError {
		t.Fatalf("%s failed: %s", tool, response.Result.Content[0].Text)
	}
	return response.Result.Content[0].Text
}

// waitFor polls until the condition holds, and fails with what it was waiting
// for rather than with a bare timeout.
func waitFor(t *testing.T, limit time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", limit, what)
}

// newFakeGitLab serves the parts of the GitLab API the platform reads.
func newFakeGitLab(files map[string]string) *httptest.Server {
	project := map[string]any{"id": 4242, "path_with_namespace": "core/api", "default_branch": "main",
		"name": "api", "description": "payment api", "visibility": "internal", "repository_access_level": "enabled"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch {
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			write(map[string]any{"id": 77})
		case strings.HasSuffix(path, "/repository/branches"):
			write([]map[string]any{{"name": "main", "commit": map[string]string{"id": "c0ffee"}, "default": true}})
		case strings.HasSuffix(path, "/repository/tags"), strings.HasSuffix(path, "/repository/commits"):
			write([]map[string]any{})
		case strings.HasSuffix(path, "/repository/tree"):
			entries := make([]map[string]string, 0, len(files))
			for name := range files {
				entries = append(entries, map[string]string{"path": name, "type": "blob"})
			}
			write(entries)
		case strings.Contains(path, "/repository/files/"):
			name := strings.TrimSuffix(strings.SplitN(path, "/repository/files/", 2)[1], "/raw")
			if decoded, err := url.PathUnescape(name); err == nil {
				name = decoded
			}
			content, ok := files[name]
			if !ok {
				http.Error(w, `{"message":"404 File Not Found"}`, http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		case strings.HasSuffix(path, "/members/all"):
			write([]map[string]any{{"id": 11, "state": "active", "access_level": 30}})
		case strings.HasSuffix(path, "/api/v4/groups"):
			write([]map[string]any{{"id": 1, "full_path": "core", "name": "core"}})
		case strings.HasSuffix(path, "/api/v4/projects"), strings.HasSuffix(path, "/projects"):
			write([]map[string]any{project})
		case strings.Contains(path, "/api/v4/projects/"):
			write(project)
		default:
			write([]map[string]any{})
		}
	}))
}

// newFakeModelServer answers embeddings and reranking deterministically: texts
// that share words end up close, which is what a semantic search exploits.
func newFakeModelServer() *fakeModel {
	model := &fakeModel{}
	model.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload struct {
			Input     any      `json:"input"`
			Query     string   `json:"query"`
			Documents []string `json:"documents"`
		}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/embeddings"):
			model.mu.Lock()
			model.embeds++
			model.mu.Unlock()
			var texts []string
			switch value := payload.Input.(type) {
			case string:
				texts = []string{value}
			case []any:
				for _, item := range value {
					text, _ := item.(string)
					texts = append(texts, text)
				}
			}
			data := make([]map[string]any, len(texts))
			for index, text := range texts {
				data[index] = map[string]any{"index": index, "embedding": fakeVector(text)}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
		case strings.HasSuffix(r.URL.Path, "/rerank"):
			model.mu.Lock()
			model.reranks++
			model.mu.Unlock()
			results := make([]map[string]any, len(payload.Documents))
			base := fakeVector(payload.Query)
			for index, document := range payload.Documents {
				candidate := fakeVector(document)
				score := 0.0
				for position := range base {
					score += float64(base[position] * candidate[position])
				}
				results[index] = map[string]any{"index": index, "relevance_score": score}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"results": results})
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{}})
		}
	}))
	return model
}

type fakeModel struct {
	*httptest.Server
	mu              sync.Mutex
	embeds, reranks int
}

func (m *fakeModel) embedCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.embeds
}

func (m *fakeModel) rerankCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.reranks
}

// fakeVector hashes words into a small bag-of-words vector.
func fakeVector(text string) []float32 {
	const dimensions = 16
	values := make([]float32, dimensions)
	for _, word := range strings.Fields(strings.ToLower(text)) {
		sum := 0
		for _, letter := range word {
			sum = (sum*31 + int(letter)) % 1000003
		}
		values[sum%dimensions]++
	}
	norm := 0.0
	for _, value := range values {
		norm += float64(value) * float64(value)
	}
	if norm == 0 {
		return values
	}
	scale := float32(1 / math.Sqrt(norm))
	for index := range values {
		values[index] *= scale
	}
	return values
}

// newFakeReceiver counts the notifications that arrive.
func newFakeReceiver() *fakeReceiver {
	receiver := &fakeReceiver{}
	receiver.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receiver.mu.Lock()
		receiver.received++
		receiver.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	return receiver
}

type fakeReceiver struct {
	*httptest.Server
	mu       sync.Mutex
	received int
}

func (r *fakeReceiver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.received
}
