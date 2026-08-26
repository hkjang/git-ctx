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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git-ctx/internal/apikey"
	"git-ctx/internal/config"
	"git-ctx/internal/testsupport"
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
	chainDriver, chainDSN := chainDatabase(t, directory, "chain")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM index_jobs WHERE repository_id=? AND status='completed' AND files_processed>0`), repository.ID).Scan(&completed)
		return completed > 0
	})

	// 4. What was indexed: the secret is masked, the manifests became an
	// inventory, and the lock file decided the version.
	var masked string
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT content FROM document_chunks WHERE repository_id=? AND file_path='config/app.yaml'`), repository.ID).Scan(&masked); err != nil {
		t.Fatalf("the configuration file was not indexed: %v", err)
	}
	if strings.Contains(masked, "super-secret-value") {
		t.Fatalf("a secret reached the index: %q", masked)
	}
	var reactVersion string
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT version FROM repository_packages WHERE repository_id=? AND name_lower='react' AND scope='resolved'`), repository.ID).Scan(&reactVersion); err != nil {
		t.Fatalf("the lock file was not inventoried: %v", err)
	}
	if reactVersion != "18.3.1" {
		t.Fatalf("the resolved version is %q, not the lock file's", reactVersion)
	}

	// 5. Embeddings were produced through the /v1 base URL, and the ref records
	// the revision they were made with.
	waitFor(t, 60*time.Second, "the chunks to be embedded", func() bool {
		var total, embedded int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*),COUNT(embedding) FROM document_chunks WHERE repository_id=?`), repository.ID).Scan(&total, &embedded)
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

// mcpCallWithKey drives one tool with an MCP API key, which is what the
// administrative tools require.
func mcpCallWithKey(t *testing.T, a *App, secret, tool, arguments string) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("X-API-Key", secret)
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

// newFakeGitLab serves the parts of the GitLab API the platform reads. When
// breakSearch is set, only the search endpoint fails — the shape of a server
// whose code search is not enabled, which is common and used to empty answers
// that the index could have filled.
func newFakeGitLab(files map[string]string) *httptest.Server {
	return newFakeGitLabWithSearch(files, nil)
}

func newFakeGitLabWithSearch(files map[string]string, breakSearch *bool) *httptest.Server {
	return newFakeGitLabRepository(&fakeRepository{files: files, commit: "c0ffee"}, breakSearch)
}

// fakeRepository is a source repository that can change between index runs, so
// the incremental path can be driven the way a push drives it.
type fakeRepository struct {
	mu      sync.Mutex
	files   map[string]string
	commit  string
	changes []map[string]any
}

func (f *fakeRepository) snapshot() (map[string]string, string, []map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	copied := make(map[string]string, len(f.files))
	for name, content := range f.files {
		copied[name] = content
	}
	return copied, f.commit, append([]map[string]any(nil), f.changes...)
}

// push replaces the content of one path and moves the commit, reporting the
// change the way GitLab's compare endpoint does.
func (f *fakeRepository) push(commit string, changed map[string]string, removed []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.commit = commit
	f.changes = nil
	for name, content := range changed {
		f.files[name] = content
		f.changes = append(f.changes, map[string]any{"new_path": name, "old_path": name, "new_file": false})
	}
	for _, name := range removed {
		delete(f.files, name)
		f.changes = append(f.changes, map[string]any{"new_path": name, "old_path": name, "deleted_file": true})
	}
}

func newFakeGitLabRepository(repository *fakeRepository, breakSearch *bool) *httptest.Server {
	project := map[string]any{"id": 4242, "path_with_namespace": "core/api", "default_branch": "main",
		"name": "api", "description": "payment api", "visibility": "internal", "repository_access_level": "enabled"}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		files, commit, changes := repository.snapshot()
		switch {
		case strings.HasSuffix(path, "/repository/compare"):
			write(map[string]any{"compare_timeout": false, "diffs": changes})
		case breakSearch != nil && *breakSearch && strings.HasSuffix(path, "/search"):
			http.Error(w, `{"message":"search is not enabled"}`, http.StatusForbidden)
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			write(map[string]any{"id": 77})
		case strings.HasSuffix(path, "/repository/branches"):
			write([]map[string]any{{"name": "main", "commit": map[string]string{"id": commit}, "default": true}})
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
			if model.broken("embeddings") {
				http.Error(w, `{"error":"model unavailable"}`, http.StatusServiceUnavailable)
				return
			}
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
			if model.broken("rerank") {
				http.Error(w, `{"error":"reranker unavailable"}`, http.StatusServiceUnavailable)
				return
			}
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
	// failEmbeddings and failReranking make the model unavailable without
	// stopping the server, which is what an outage looks like to this platform.
	failEmbeddings, failReranking bool
}

func (m *fakeModel) breakEmbeddings(broken bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failEmbeddings = broken
}

func (m *fakeModel) breakReranking(broken bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.failReranking = broken
}

func (m *fakeModel) broken(kind string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if kind == "embeddings" {
		return m.failEmbeddings
	}
	return m.failReranking
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

// The defects worth catching were never in the happy path. They were in what
// the platform does when a part of it is missing: a search that returned
// nothing while its own index held the answer, an embedding outage reported as
// a source error, a reranker that failed without saying so, alerts that gave up
// unseen. This drives the same chain with those parts taken away one at a time
// and checks the answer says what happened.
func TestPlatformDegradationIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the degradation chain waits on the background worker")
	}
	ctx := context.Background()

	searchBroken := false
	files := map[string]string{
		"README.md":       "# Payment API\n\n결제 처리 서비스. settlement 배치를 운영한다.\n",
		"service.go":      "package main\n\nfunc settleInvoice() error { return nil }\n",
		"config/app.yaml": "database:\n  host: db.internal\n",
	}
	source := newFakeGitLabWithSearch(files, &searchBroken)
	defer source.Close()
	model := newFakeModelServer()
	defer model.Close()
	receiver := newFailingReceiver()
	defer receiver.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "degraded")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/model",
		fmt.Sprintf(`{"provider":"openai-compatible","baseUrl":"%s/v1","model":"fake-embed","apiKey":"none","dimensions":16,"timeoutSeconds":5,"rerankerEnabled":true,"rerankerProvider":"openai-compatible","rerankerBaseUrl":"%s/v1","rerankerModel":"fake-rerank","rerankerApiKey":"none","rerankerTimeoutSeconds":5}`,
			model.URL, model.URL)); saved.Code != http.StatusOK {
		t.Fatalf("model settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/notifications",
		fmt.Sprintf(`{"externalEnabled":true,"webhookUrl":%q,"webhookAuthorization":"Bearer hook-token","maxAttempts":1}`, receiver.URL+"/hook")); saved.Code != http.StatusOK {
		t.Fatalf("notification settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var repository struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM index_jobs WHERE repository_id=? AND status='completed' AND files_processed>0`), repository.ID).Scan(&completed)
		return completed > 0
	})

	// The source's search API stops working. The index still holds the answer,
	// and an empty answer here is what an agent reads as "the code does not
	// exist" — the failure this platform exists to prevent.
	searchBroken = true
	answer := mcpCall(t, a, "search-code", `{"query":"settleInvoice"}`)
	if !strings.Contains(answer, "service.go") {
		t.Fatalf("the index did not answer while the source search was down:\n%s", answer)
	}
	if !strings.Contains(answer, "index:") {
		t.Fatalf("the answer must say it came from the index:\n%s", answer)
	}

	// The reranker stops answering. The order is no longer its own, and that has
	// to be visible rather than assumed.
	model.breakReranking(true)
	docs := mcpCall(t, a, "query-docs", fmt.Sprintf(`{"libraryId":%q,"query":"결제 처리"}`, repository.LibraryID))
	if !strings.Contains(docs, "재순위 모델을 호출하지 못해") {
		t.Fatalf("a failed reranking must be reported:\n%s", docs)
	}

	// The embedding model stops answering. A query the index can serve still
	// returns, and says which path produced it.
	model.breakEmbeddings(true)
	semantic := mcpCall(t, a, "search-semantic", `{"query":"settleInvoice"}`)
	if !strings.Contains(semantic, "service.go") {
		t.Fatalf("the lexical path did not answer without embeddings:\n%s", semantic)
	}

	// Alerts that give up must be visible in the platform status, because the
	// thing that would have reported them is the thing that broke.
	if _, err = a.store.DB.Exec(`INSERT INTO users(id,subject,username,email,status) VALUES('u-ops','u-ops','ops','ops@example.com','active') ON CONFLICT(id) DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err = a.store.DB.Exec(`INSERT INTO notifications(id,user_id,notification_type,resource_id,title,message) VALUES('n-dead','u-ops','api_key_expiring','k-dead','MCP API key expires soon','곧 만료됩니다.')`); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 90*time.Second, "the delivery to give up", func() bool {
		var dead int
		_ = a.store.DB.QueryRow(`SELECT COUNT(*) FROM notification_deliveries WHERE notification_id='n-dead' AND status='dead'`).Scan(&dead)
		return dead > 0
	})
	// Administrative tools require an MCP API key by design, so the status is
	// asked for the way an operator's agent would ask for it.
	created := call(http.MethodPost, "/api/v1/me/api-keys", `{"name":"ops","scopes":["get-platform-status"]}`)
	if created.Code != http.StatusCreated && created.Code != http.StatusOK {
		t.Fatalf("key creation status=%d body=%s", created.Code, created.Body.String())
	}
	var key struct {
		Secret string `json:"secret"`
	}
	if err = json.Unmarshal(created.Body.Bytes(), &key); err != nil || key.Secret == "" {
		t.Fatalf("no key was issued: %v body=%s", err, created.Body.String())
	}
	status := mcpCallWithKey(t, a, key.Secret, "get-platform-status", `{}`)
	if !strings.Contains(status, "gave up after their retries") {
		t.Fatalf("undelivered alerts must reach the platform status:\n%s", status)
	}
}

// newFailingReceiver refuses every delivery, which is how an outbox reaches its
// attempt limit.
func newFailingReceiver() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
	}))
}

// The platform's headline is Bitbucket Server and GitLab, and until now the
// whole chain had only ever been proven on GitLab. The two adapters share the
// indexer and the search layer but nothing else: their pagination, their path
// escaping, their permission model and their raw-file endpoints are all
// different, and a wiring defect on this side would empty a whole source
// without a single unit test noticing.
func TestBitbucketChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	files := map[string]string{
		"pom.xml": `<project><dependencies>
  <dependency><groupId>org.apache.logging.log4j</groupId><artifactId>log4j-core</artifactId><version>2.14.1</version></dependency>
</dependencies></project>`,
		"README.md":       "# Ledger\n\n원장 정산 서비스. reconciliation 배치를 운영한다.\n",
		"config/app.yaml": "database:\n  password: super-secret-value-1234567890\n",
	}
	source := newFakeBitbucket(files)
	defer source.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "bitbucket")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/bitbucket",
		fmt.Sprintf(`{"baseUrl":%q,"apiPrefix":"/rest/api/1.0","pat":"token","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("bitbucket settings status=%d body=%s", saved.Code, saved.Body.String())
	}

	// Discovery is how an operator finds repositories on a Bitbucket server.
	discovered := call(http.MethodPost, "/api/v1/admin/sources/bitbucket/discover", `{"projectKey":"CORE"}`)
	if discovered.Code != http.StatusOK {
		t.Fatalf("discover status=%d body=%s", discovered.Code, discovered.Body.String())
	}
	if !strings.Contains(discovered.Body.String(), "ledger") {
		t.Fatalf("discovery did not list the repository: %s", discovered.Body.String())
	}

	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"bitbucket","repository":{"id":7,"projectKey":"CORE","slug":"ledger","name":"Ledger","description":"원장","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var repository struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &repository); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the Bitbucket repository to finish indexing", func() bool {
		var completed int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM index_jobs WHERE repository_id=? AND status='completed' AND files_processed>0`), repository.ID).Scan(&completed)
		return completed > 0
	})

	// The same guarantees as the GitLab chain: content indexed through the raw
	// endpoint, secrets masked, manifests inventoried, permissions imported.
	var masked string
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT content FROM document_chunks WHERE repository_id=? AND file_path='config/app.yaml'`), repository.ID).Scan(&masked); err != nil {
		t.Fatalf("the configuration file was not indexed: %v", err)
	}
	if strings.Contains(masked, "super-secret-value") {
		t.Fatalf("a secret reached the index: %q", masked)
	}
	var packages int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM repository_packages WHERE repository_id=?`), repository.ID).Scan(&packages); err != nil || packages == 0 {
		t.Fatalf("the manifest was not inventoried: %d err=%v", packages, err)
	}
	var principals int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM repository_permissions WHERE repository_id=?`), repository.ID).Scan(&principals); err != nil || principals == 0 {
		t.Fatalf("no permission was imported: %d err=%v", principals, err)
	}

	answer := mcpCall(t, a, "search-code", `{"query":"reconciliation 배치"}`)
	if !strings.Contains(answer, "README.md") {
		t.Fatalf("search-code did not find the Bitbucket content:\n%s", answer)
	}
	advisory := mcpCall(t, a, "find-dependency-usage", `{"name":"org.apache.logging.log4j:log4j-core","fixedIn":"2.17.1"}`)
	if !strings.Contains(advisory, "AFFECTED") {
		t.Fatalf("the advisory judgement is missing for Bitbucket:\n%s", advisory)
	}
}

// newFakeBitbucket serves the Bitbucket Server REST 1.0 endpoints the platform
// reads, including its page envelope and its raw-file endpoint.
func newFakeBitbucket(files map[string]string) *httptest.Server {
	repository := map[string]any{"id": 7, "slug": "ledger", "name": "Ledger", "description": "원장",
		"project": map[string]string{"key": "CORE"}, "defaultBranch": "refs/heads/main", "archived": false}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimSuffix(r.URL.EscapedPath(), "/")
		write := func(values any) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"values": values, "isLastPage": true, "size": 1})
		}
		switch {
		case r.Method == http.MethodPost || r.Method == http.MethodPut:
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":77}`)
		case strings.HasSuffix(path, "/rest/api/1.0/projects"):
			write([]map[string]any{{"key": "CORE", "name": "Core", "description": "core"}})
		case strings.HasSuffix(path, "/repos"):
			write([]map[string]any{repository})
		case strings.HasSuffix(path, "/branches"):
			write([]map[string]any{{"id": "refs/heads/main", "displayId": "main", "latestCommit": "abc123", "isDefault": true}})
		case strings.HasSuffix(path, "/tags"):
			write([]map[string]any{})
		case strings.HasSuffix(path, "/files"):
			paths := make([]string, 0, len(files))
			for name := range files {
				paths = append(paths, name)
			}
			write(paths)
		case strings.Contains(path, "/raw/"):
			name := strings.SplitN(path, "/raw/", 2)[1]
			if decoded, err := url.PathUnescape(name); err == nil {
				name = decoded
			}
			content, ok := files[name]
			if !ok {
				http.Error(w, `{"errors":[{"message":"not found"}]}`, http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, content)
		case strings.HasSuffix(path, "/permissions/users"):
			write([]map[string]any{{"user": map[string]any{"name": "alice", "slug": "alice", "active": true}, "permission": "REPO_READ"}})
		case strings.HasSuffix(path, "/permissions/groups"):
			write([]map[string]any{{"group": map[string]any{"name": "engineering"}, "permission": "REPO_READ"}})
		case strings.HasSuffix(path, "/commits"):
			write([]map[string]any{})
		default:
			write([]any{})
		}
	}))
}

// The incremental path is where this platform did its worst damage: a sync that
// only touched one file used to replace the whole ref's dependency inventory
// with nothing, so an advisory search answered "no repository uses it" for
// repositories that did. A push arrives through a webhook, and everything after
// that — signature, deduplication, the diff, the partial rewrite — has to leave
// the parts that did not change alone.
func TestIncrementalPushChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	repository := &fakeRepository{commit: "commit-one", files: map[string]string{
		"pom.xml": `<project><dependencies>
  <dependency><groupId>org.apache.logging.log4j</groupId><artifactId>log4j-core</artifactId><version>2.14.1</version></dependency>
</dependencies></project>`,
		"README.md":  "# Payment API\n\n결제 처리 서비스.\n",
		"service.go": "package main\n\nfunc settleInvoice() error { return nil }\n",
		"legacy.go":  "package main\n\nfunc removedLater() {}\n",
	}}
	source := newFakeGitLabRepository(repository, nil)
	defer source.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "incremental")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var target struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the first index", func() bool {
		var chunks int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), target.ID).Scan(&chunks)
		return chunks >= 4
	})
	var inventoryBefore int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM repository_packages WHERE repository_id=?`), target.ID).Scan(&inventoryBefore); err != nil || inventoryBefore == 0 {
		t.Fatalf("the manifest was not inventoried: %d err=%v", inventoryBefore, err)
	}

	// A push that touches one file and deletes another. The manifest is not in
	// the diff, which is exactly the case that used to wipe the inventory.
	repository.push("commit-two", map[string]string{
		"service.go": "package main\n\nfunc settleRefund() error { return nil }\n",
	}, []string{"legacy.go"})

	hook := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab",
		strings.NewReader(`{"project":{"id":4242},"ref":"refs/heads/main"}`))
	hook.Header.Set("Content-Type", "application/json")
	hook.Header.Set("X-Gitlab-Token", "s3cret")
	hook.Header.Set("X-Gitlab-Event", "Push Hook")
	hook.Header.Set("X-Gitlab-Event-UUID", "push-1")
	accepted := httptest.NewRecorder()
	a.Handler().ServeHTTP(accepted, hook)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("webhook status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	// The same event delivered twice must not queue the work twice.
	duplicate := httptest.NewRecorder()
	replay := hook.Clone(ctx)
	replay.Body = io.NopCloser(strings.NewReader(`{"project":{"id":4242},"ref":"refs/heads/main"}`))
	a.Handler().ServeHTTP(duplicate, replay)
	if duplicate.Code != http.StatusOK || !strings.Contains(duplicate.Body.String(), `"duplicate":true`) {
		t.Fatalf("a replayed event must be recognised: status=%d body=%s", duplicate.Code, duplicate.Body.String())
	}

	waitFor(t, 60*time.Second, "the pushed change to be indexed", func() bool {
		var updated int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=? AND file_path='service.go' AND content LIKE '%settleRefund%'`), target.ID).Scan(&updated)
		return updated > 0
	})

	// The file that was deleted is gone, the file nobody touched is intact, and
	// the inventory the diff never mentioned survived.
	var removed int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=? AND file_path='legacy.go'`), target.ID).Scan(&removed); err != nil || removed != 0 {
		t.Fatalf("a deleted file stayed in the index: %d err=%v", removed, err)
	}
	var untouched int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=? AND file_path='README.md'`), target.ID).Scan(&untouched); err != nil || untouched == 0 {
		t.Fatalf("an untouched file was dropped: %d err=%v", untouched, err)
	}
	var inventoryAfter int
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM repository_packages WHERE repository_id=?`), target.ID).Scan(&inventoryAfter); err != nil {
		t.Fatal(err)
	}
	if inventoryAfter != inventoryBefore {
		t.Fatalf("the incremental sync changed the inventory from %d to %d rows", inventoryBefore, inventoryAfter)
	}

	// The tools answer over the new content, and the advisory still finds the
	// manifest that was never in the diff.
	answer := mcpCall(t, a, "search-code", `{"query":"settleRefund"}`)
	if !strings.Contains(answer, "service.go") {
		t.Fatalf("the pushed change is not searchable:\n%s", answer)
	}
	stale := mcpCall(t, a, "search-code", `{"query":"settleInvoice"}`)
	if strings.Contains(stale, "service.go") {
		t.Fatalf("the replaced content is still searchable:\n%s", stale)
	}
	advisory := mcpCall(t, a, "find-dependency-usage", `{"name":"org.apache.logging.log4j:log4j-core","fixedIn":"2.17.1"}`)
	if !strings.Contains(advisory, "AFFECTED") {
		t.Fatalf("the inventory did not survive the push:\n%s", advisory)
	}
}

// The platform's core promise is that an answer never contains a repository the
// caller cannot read. Every chain test so far ran as the bootstrap
// administrator, whose ACL is bypassed, so the promise itself was never
// exercised through the real path: identity to principals to the SQL predicate
// to the tool output. This drives two indexed repositories and two developers
// who can each read one of them.
func TestAccessControlChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	payments := newFakeGitLabRepository(&fakeRepository{commit: "pay-1", files: map[string]string{
		"README.md":  "# Payments\n\n결제 정산 서비스.\n",
		"service.go": "package main\n\nfunc settleInvoice() error { return nil }\n",
	}}, nil)
	defer payments.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "acl")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, payments.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var indexed struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &indexed); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 60*time.Second, "the repository to finish indexing", func() bool {
		var chunks int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), indexed.ID).Scan(&chunks)
		return chunks > 0
	})

	// A second repository nobody indexed from this source, readable only by the
	// other team, so a leak is visible as a name rather than as a count.
	must := func(query string, args ...any) {
		t.Helper()
		if _, err := a.store.DB.Exec(a.store.Rebind(query), args...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO repositories(id,project_key,slug,name,description,source_type,source_external_id,library_id,default_branch,enabled) VALUES('gitlab:9','core','ledger','ledger','원장','gitlab','9','/core/ledger','main',1)`)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES('gitlab:9','group:ledger-team','read')`)
	must(`INSERT INTO repository_ref_states(repository_id,ref_name,commit_id) VALUES('gitlab:9','main','led-1')`)
	must(`INSERT INTO document_chunks(id,repository_id,ref_name,commit_id,file_path,line_start,line_end,heading,content_type,content,content_hash) VALUES('led1','gitlab:9','main','led-1','ledger.go',1,9,'Ledger','code','func settleInvoice() error { return nil }','ledh1')`)

	// The indexed repository is readable by the payments team only.
	must(`DELETE FROM repository_permissions WHERE repository_id=?`, indexed.ID)
	must(`INSERT INTO repository_permissions(repository_id,principal,permission) VALUES(?,'group:payments-team','read')`, indexed.ID)

	// Two developers, each mapped to one team, each with their own key.
	newDeveloper := func(id, group string) string {
		t.Helper()
		must(`INSERT INTO users(id,subject,username,email,status) VALUES(?,?,?,'','active')`, id, id, id)
		must(`INSERT INTO user_identities(user_id,bitbucket_user_slug,gitlab_user_id,mapping_source,bitbucket_groups) VALUES(?,'',?,'manual',?)`, id, id, group)
		_, secret, err := a.keys.Create(ctx, id, "agent", []string{"search-code", "search-repositories", "query-docs", "read-file"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return secret
	}
	paymentsKey := newDeveloper("dev-payments", "group:payments-team")
	ledgerKey := newDeveloper("dev-ledger", "group:ledger-team")

	// Each developer sees their own repository and never the other's.
	paymentsRepositories := mcpCallWithKey(t, a, paymentsKey, "search-repositories", `{"query":"payment"}`)
	if !strings.Contains(paymentsRepositories, indexed.LibraryID) || strings.Contains(paymentsRepositories, "/core/ledger") {
		t.Fatalf("the payments developer saw the wrong catalogue:\n%s", paymentsRepositories)
	}
	ledgerRepositories := mcpCallWithKey(t, a, ledgerKey, "search-repositories", `{"query":"원장"}`)
	if !strings.Contains(ledgerRepositories, "/core/ledger") || strings.Contains(ledgerRepositories, indexed.LibraryID) {
		t.Fatalf("the ledger developer saw the wrong catalogue:\n%s", ledgerRepositories)
	}

	// The same query matches content in both repositories; each caller may only
	// see their own, which is where a predicate mistake would show.
	paymentsCode := mcpCallWithKey(t, a, paymentsKey, "search-code", `{"query":"settleInvoice"}`)
	if strings.Contains(paymentsCode, "ledger.go") {
		t.Fatalf("code search leaked the other team's file:\n%s", paymentsCode)
	}
	ledgerCode := mcpCallWithKey(t, a, ledgerKey, "search-code", `{"query":"settleInvoice"}`)
	if strings.Contains(ledgerCode, "service.go") {
		t.Fatalf("code search leaked the other team's file:\n%s", ledgerCode)
	}

	// Naming the repository directly is refused, and the refusal must not
	// confirm that it exists.
	denied := mcpDeniedWithKey(t, a, ledgerKey, "read-file", fmt.Sprintf(`{"libraryId":%q,"path":"service.go"}`, indexed.LibraryID))
	if strings.Contains(denied, "payment") {
		t.Fatalf("the refusal described the repository: %q", denied)
	}

	// A key may be narrowed further than its owner's access. The repository is
	// readable by this developer, but not through this key.
	_, restricted, err := a.keys.CreateWithRestrictions(ctx, "dev-payments", "narrow",
		[]string{"search-repositories", "search-code"}, nil, apikey.Restrictions{AllowedRepositories: []string{"/core/nothing"}})
	if err != nil {
		t.Fatal(err)
	}
	narrow := mcpCallWithKey(t, a, restricted, "search-repositories", `{"query":"payment"}`)
	if strings.Contains(narrow, indexed.LibraryID) {
		t.Fatalf("a key restricted to another repository still returned this one:\n%s", narrow)
	}
}

// mcpDeniedWithKey calls a tool expecting a refusal and returns its text.
func mcpDeniedWithKey(t *testing.T, a *App, secret, tool, arguments string) string {
	t.Helper()
	body := fmt.Sprintf(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":%q,"arguments":%s}}`, tool, arguments)
	request := httptest.NewRequest(http.MethodPost, "/mcp", strings.NewReader(body))
	request.Header.Set("X-API-Key", secret)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	a.Handler().ServeHTTP(recorder, request)
	var response struct {
		Result struct {
			Content []struct{ Text string } `json:"content"`
			IsError bool                    `json:"isError"`
		} `json:"result"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	if len(response.Result.Content) == 0 || !response.Result.IsError {
		t.Fatalf("%s was not refused: %s", tool, recorder.Body.String())
	}
	return response.Result.Content[0].Text
}

// Confluence and Jira are content sources like the two Git servers, and they
// fail the same way if their wiring is wrong: nothing arrives, the catalogue
// looks normal, and the pages or issues an agent was supposed to find simply do
// not exist as far as the platform is concerned. Only contract tests covered
// them, so the path from the settings screen to an answer was never run.
func TestDocumentSourceChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	confluence := newFakeConfluence()
	defer confluence.Close()
	jira := newFakeJira()
	defer jira.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "documents")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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

	// Both sources are read with a token and hand out their ACL explicitly:
	// there is no per-page permission model to import, so the operator names
	// who may read what this connector brings in.
	for _, item := range []struct{ category, url string }{
		{"confluence", confluence.URL},
		{"jira", jira.URL},
	} {
		saved := call(http.MethodPut, "/api/v1/admin/settings/"+item.category,
			fmt.Sprintf(`{"baseUrl":%q,"authType":"bearer","token":"t","allowedPrincipals":["group:platform"]}`, item.url))
		if saved.Code != http.StatusOK {
			t.Fatalf("%s settings status=%d body=%s", item.category, saved.Code, saved.Body.String())
		}
	}

	registered := map[string]string{}
	for _, item := range []struct{ sourceType, project, slug, name string }{
		{"confluence", "OPS", "OPS", "Operations space"},
		{"jira", "PAY", "PAY", "Payments project"},
	} {
		created := call(http.MethodPost, "/api/v1/admin/repositories",
			fmt.Sprintf(`{"sourceType":%q,"repository":{"id":1,"projectKey":%q,"slug":%q,"name":%q,"description":"","defaultBranch":"current"}}`,
				item.sourceType, item.project, item.slug, item.name))
		if created.Code != http.StatusCreated {
			t.Fatalf("%s register status=%d body=%s", item.sourceType, created.Code, created.Body.String())
		}
		var repository struct{ ID, LibraryID string }
		if err = json.Unmarshal(created.Body.Bytes(), &repository); err != nil {
			t.Fatal(err)
		}
		registered[item.sourceType] = repository.ID
	}

	for sourceType, id := range registered {
		waitFor(t, 60*time.Second, sourceType+" to finish indexing", func() bool {
			var chunks int
			_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM document_chunks WHERE repository_id=?`), id).Scan(&chunks)
			return chunks > 0
		})
		// The connector's declared principals become the ACL, which is the only
		// thing standing between these documents and everyone.
		var principal string
		if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT principal FROM repository_permissions WHERE repository_id=? LIMIT 1`), id).Scan(&principal); err != nil {
			t.Fatalf("%s imported no principal: %v", sourceType, err)
		}
		if principal != "group:platform" {
			t.Fatalf("%s imported the wrong principal: %q", sourceType, principal)
		}
	}

	// A Confluence page arrives as text, not as the storage-format markup it is
	// stored in — an agent handed raw markup would quote tags at the reader.
	var page string
	if err = a.store.DB.QueryRow(a.store.Rebind(`SELECT content FROM document_chunks WHERE repository_id=? LIMIT 1`), registered["confluence"]).Scan(&page); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "<p>") || strings.Contains(page, "<ac:") {
		t.Fatalf("the page kept its markup: %q", page)
	}
	if !strings.Contains(page, "장애 대응") {
		t.Fatalf("the page body did not arrive: %q", page)
	}

	// The library identifiers are derived from the space and project keys, so
	// the answer is checked against those rather than against a title.
	// A wiki and an issue tracker answer no live query, so their content exists
	// only in the index. An answer that skipped the index because some other
	// source replied would never contain them.
	answer := mcpCall(t, a, "search-code", `{"query":"컨슈머를 재시작"}`)
	if !strings.Contains(strings.ToLower(answer), "/confluence~") {
		t.Fatalf("the Confluence page is not searchable:\n%s", answer)
	}
	issues := mcpCall(t, a, "search-code", `{"query":"정산 배치 실패"}`)
	if !strings.Contains(strings.ToLower(issues), "/jira~") {
		t.Fatalf("the Jira issue is not searchable:\n%s", issues)
	}
}

// newFakeConfluence serves the space and page endpoints the connector reads.
func newFakeConfluence() *httptest.Server {
	pages := map[string]map[string]string{
		"101": {"title": "장애 대응 런북", "body": "<p>장애 대응 절차: 컨슈머를 재시작한다.</p>"},
		"102": {"title": "배포 절차", "body": "<p>배포는 카나리로 시작한다.</p>"},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch {
		case strings.HasSuffix(path, "/rest/api/space"):
			write(map[string]any{"results": []map[string]any{{"key": "OPS", "name": "Operations"}}})
		case strings.Contains(path, "/rest/api/space/"):
			write(map[string]any{"id": "1", "key": "OPS", "name": "Operations", "description": "운영 공간"})
		case strings.HasSuffix(path, "/rest/api/content"):
			results := make([]map[string]any, 0, len(pages))
			for id, page := range pages {
				results = append(results, map[string]any{"id": id, "title": page["title"],
					"version": map[string]any{"number": 3}})
			}
			write(map[string]any{"results": results})
		case strings.Contains(path, "/rest/api/content/"):
			id := path[strings.LastIndex(path, "/")+1:]
			page, ok := pages[id]
			if !ok {
				http.Error(w, `{"message":"not found"}`, http.StatusNotFound)
				return
			}
			write(map[string]any{"id": id, "title": page["title"],
				"body": map[string]any{"storage": map[string]any{"value": page["body"]}}})
		default:
			write(map[string]any{"results": []any{}})
		}
	}))
}

// newFakeJira serves the project and issue endpoints the connector reads.
func newFakeJira() *httptest.Server {
	issues := []map[string]any{
		{"key": "PAY-1", "fields": map[string]any{"summary": "정산 배치 실패", "updated": "2026-08-01T00:00:00.000+0900",
			"description": "야간 정산 배치가 타임아웃으로 실패한다."}},
		{"key": "PAY-2", "fields": map[string]any{"summary": "환불 지연", "updated": "2026-08-02T00:00:00.000+0900",
			"description": "환불 처리가 지연된다."}},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		write := func(value any) { _ = json.NewEncoder(w).Encode(value) }
		switch {
		case strings.HasSuffix(path, "/rest/api/2/project"):
			write([]map[string]any{{"key": "PAY", "name": "Payments"}})
		case strings.Contains(path, "/rest/api/2/project/"):
			write(map[string]any{"key": "PAY", "name": "Payments", "description": "결제"})
		case strings.HasSuffix(path, "/rest/api/2/search"):
			write(map[string]any{"issues": issues})
		case strings.Contains(path, "/rest/api/2/issue/"):
			key := path[strings.LastIndex(path, "/")+1:]
			for _, issue := range issues {
				if issue["key"] == key {
					write(issue)
					return
				}
			}
			http.Error(w, `{"errorMessages":["not found"]}`, http.StatusNotFound)
		default:
			write(map[string]any{"issues": []any{}})
		}
	}))
}

// OpenSearch is the last optional backend whose whole path was never run: the
// indexer projects each finished ref into it, and searches read candidates back
// from it. A projection that silently stops leaves the index believing it is
// current while the cluster answers from content that no longer exists — the
// kind of divergence that only shows as a stale hit weeks later.
func TestOpenSearchProjectionChainIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("the platform chain waits on the background worker")
	}
	ctx := context.Background()

	cluster := newFakeOpenSearch()
	defer cluster.Close()
	repository := &fakeRepository{commit: "os-1", files: map[string]string{
		"README.md":  "# Payments\n\n정산 서비스.\n",
		"service.go": "package main\n\nfunc settleInvoice() error { return nil }\n",
		"legacy.go":  "package main\n\nfunc removedLater() {}\n",
	}}
	source := newFakeGitLabRepository(repository, nil)
	defer source.Close()

	directory := t.TempDir()
	chainDriver, chainDSN := chainDatabase(t, directory, "opensearch")
	a, err := New(ctx, config.Config{
		DatabaseDriver: chainDriver, DatabaseDSN: chainDSN,
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
	if saved := call(http.MethodPut, "/api/v1/admin/settings/gitlab",
		fmt.Sprintf(`{"baseUrl":%q,"token":"t","webhookSecret":"s3cret"}`, source.URL)); saved.Code != http.StatusOK {
		t.Fatalf("gitlab settings status=%d body=%s", saved.Code, saved.Body.String())
	}
	if saved := call(http.MethodPut, "/api/v1/admin/settings/opensearch",
		fmt.Sprintf(`{"enabled":true,"baseUrl":%q,"index":"git-ctx-chunks","authType":"none","timeoutSeconds":10}`, cluster.URL)); saved.Code != http.StatusOK {
		t.Fatalf("opensearch settings status=%d body=%s", saved.Code, saved.Body.String())
	}

	registered := call(http.MethodPost, "/api/v1/admin/repositories",
		`{"sourceType":"gitlab","repository":{"id":4242,"projectKey":"core","slug":"api","name":"api","description":"payment api","defaultBranch":"main"}}`)
	if registered.Code != http.StatusCreated {
		t.Fatalf("register status=%d body=%s", registered.Code, registered.Body.String())
	}
	var target struct{ ID, LibraryID string }
	if err = json.Unmarshal(registered.Body.Bytes(), &target); err != nil {
		t.Fatal(err)
	}

	// Every indexed chunk is projected, and the projection is recorded so a
	// later run knows it does not have to repeat itself.
	waitFor(t, 60*time.Second, "the ref to be projected", func() bool {
		return cluster.documents(target.ID) >= 3
	})
	waitFor(t, 30*time.Second, "the projection to be recorded", func() bool {
		var projected int
		_ = a.store.DB.QueryRow(a.store.Rebind(`SELECT COUNT(*) FROM search_projection_states WHERE repository_id=?`), target.ID).Scan(&projected)
		return projected > 0
	})
	if cluster.indexCreated() == 0 {
		t.Fatal("the index was never created")
	}

	// A push removes a file. The projection has to follow, or the cluster keeps
	// answering with a file the repository no longer has.
	repository.push("os-2", map[string]string{
		"service.go": "package main\n\nfunc settleRefund() error { return nil }\n",
	}, []string{"legacy.go"})
	hook := httptest.NewRequest(http.MethodPost, "/webhooks/gitlab",
		strings.NewReader(`{"project":{"id":4242},"ref":"refs/heads/main"}`))
	hook.Header.Set("Content-Type", "application/json")
	hook.Header.Set("X-Gitlab-Token", "s3cret")
	hook.Header.Set("X-Gitlab-Event", "Push Hook")
	hook.Header.Set("X-Gitlab-Event-UUID", "os-push-1")
	accepted := httptest.NewRecorder()
	a.Handler().ServeHTTP(accepted, hook)
	if accepted.Code != http.StatusAccepted {
		t.Fatalf("webhook status=%d body=%s", accepted.Code, accepted.Body.String())
	}
	waitFor(t, 60*time.Second, "the deleted file to leave the projection", func() bool {
		return !cluster.hasPath("legacy.go") && cluster.hasContent("settleRefund")
	})
	if cluster.hasContent("removedLater") {
		t.Fatal("the projection kept the content of a deleted file")
	}
}

// newFakeOpenSearch keeps the documents the platform pushes so the test can ask
// what the cluster would answer with.
func newFakeOpenSearch() *fakeCluster {
	cluster := &fakeCluster{documents_: map[string]map[string]any{}}
	cluster.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		path := r.URL.EscapedPath()
		switch {
		case path == "/" || path == "":
			_, _ = io.WriteString(w, `{"version":{"number":"2.13.0","distribution":"opensearch"}}`)
		case r.Method == http.MethodHead:
			cluster.mu.Lock()
			exists := cluster.created > 0
			cluster.mu.Unlock()
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			cluster.mu.Lock()
			cluster.created++
			cluster.mu.Unlock()
			_, _ = io.WriteString(w, `{"acknowledged":true}`)
		case strings.HasSuffix(path, "/_bulk"):
			body, _ := io.ReadAll(r.Body)
			cluster.applyBulk(string(body))
			_, _ = io.WriteString(w, `{"errors":false,"items":[]}`)
		case strings.HasSuffix(path, "/_delete_by_query"):
			body, _ := io.ReadAll(r.Body)
			cluster.deleteByQuery(string(body))
			_, _ = io.WriteString(w, `{"deleted":0}`)
		case strings.HasSuffix(path, "/_search"):
			_, _ = io.WriteString(w, `{"hits":{"hits":[]}}`)
		default:
			_, _ = io.WriteString(w, `{"acknowledged":true}`)
		}
	}))
	return cluster
}

type fakeCluster struct {
	*httptest.Server
	mu         sync.Mutex
	created    int
	documents_ map[string]map[string]any
}

// applyBulk reads the ndjson the platform sends: an action line followed, for
// an index action, by the document itself.
func (c *fakeCluster) applyBulk(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lines := strings.Split(strings.TrimSpace(body), "\n")
	for index := 0; index < len(lines); index++ {
		var action map[string]map[string]any
		if json.Unmarshal([]byte(lines[index]), &action) != nil {
			continue
		}
		if meta, ok := action["delete"]; ok {
			id, _ := meta["_id"].(string)
			delete(c.documents_, id)
			continue
		}
		meta, ok := action["index"]
		if !ok || index+1 >= len(lines) {
			continue
		}
		id, _ := meta["_id"].(string)
		var document map[string]any
		if json.Unmarshal([]byte(lines[index+1]), &document) == nil {
			c.documents_[id] = document
		}
		index++
	}
}

func (c *fakeCluster) deleteByQuery(body string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, document := range c.documents_ {
		repository, _ := document["repository_id"].(string)
		if repository != "" && strings.Contains(body, repository) {
			delete(c.documents_, id)
		}
	}
}

func (c *fakeCluster) documents(repositoryID string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	count := 0
	for _, document := range c.documents_ {
		if value, _ := document["repository_id"].(string); value == repositoryID {
			count++
		}
	}
	return count
}

func (c *fakeCluster) indexCreated() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.created
}

func (c *fakeCluster) hasPath(path string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, document := range c.documents_ {
		if value, _ := document["file_path"].(string); value == path {
			return true
		}
	}
	return false
}

func (c *fakeCluster) hasContent(fragment string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, document := range c.documents_ {
		if value, _ := document["content"].(string); strings.Contains(value, fragment) {
			return true
		}
	}
	return false
}

// chainDatabase picks the database these chain tests run against.
//
// They were written against SQLite and only ever run there, which left the
// driver an installation with more than one node actually uses — PostgreSQL —
// untested for everything above a single query: indexing a repository, an
// incremental push, the access checks, the document sources. Setting
// GIT_CTX_TEST_POSTGRES_DSN runs the same chains again on a throwaway
// PostgreSQL database, so the two drivers are held to one set of expectations
// rather than two.
func chainDatabase(t *testing.T, directory, name string) (driver, dsn string) {
	t.Helper()
	base := os.Getenv("GIT_CTX_TEST_POSTGRES_DSN")
	if strings.TrimSpace(base) == "" {
		return "sqlite", "file:" + filepath.Join(directory, name+".db") + "?_foreign_keys=on&_busy_timeout=5000"
	}
	created, cleanup, err := testsupport.NewPostgresDatabase(context.Background(), base)
	if err != nil {
		t.Fatalf("create test database: %v", err)
	}
	t.Cleanup(cleanup)
	return "postgres", created
}
