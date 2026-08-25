package mcp

import (
	"net/http"

	"git-ctx/internal/auth"
	"git-ctx/internal/toolcatalog"
)

// toolHandler runs one MCP tool after the dispatcher has authorised the call and
// applied the timeout. It returns the rendered answer and whether the catalog
// simply had nothing to say, which the usage view separates from a failure.
type toolHandler func(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error)

// tool is the single definition of one MCP tool. Advertising it, deciding who
// may call it and running it all read from this one entry, so adding a tool no
// longer means editing a catalog, a dispatch switch and three classification
// functions that the compiler never checks against each other.
type tool struct {
	name        string
	description string
	schema      map[string]any
	// core marks the tools that remain available in strict Context7
	// compatibility mode, where every extension is hidden.
	core bool
	// adminRoles restricts the tool to credentials holding one of these roles.
	// A non-empty list also marks the tool as administrative.
	adminRoles []string
	// usesLibraryID records that the libraryId argument names the repository
	// this call is audited against.
	usesLibraryID bool
	handler       toolHandler
}

// registry lists every tool in catalog order; the catalog served to clients is
// derived from it.
var registry = []tool{
	{
		name:        toolcatalog.ResolveLibraryID,
		description: "Resolves a repository or library name to a Context7-compatible library ID.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryName", "query"}, "properties": map[string]any{
			"libraryName": map[string]string{"type": "string", "description": "Library or repository name"},
			"query":       map[string]string{"type": "string", "description": "Task context used to rank candidates"}}},
		core:    true,
		handler: handleResolveLibraryId,
	},
	{
		name:        toolcatalog.QueryDocs,
		description: "Searches versioned documentation and code examples for a library ID.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "query"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string", "description": "Context7-compatible /organization/project[/version] ID"},
			"query":     map[string]string{"type": "string", "description": "Focused documentation question"}}},
		core:          true,
		usesLibraryID: true,
		handler:       handleQueryDocs,
	},
	{
		name:        toolcatalog.SearchRepositories,
		description: "Lists matching Bitbucket and GitLab repositories by name or description only. This tool never returns file contents; call search-code when the user asks about code, symbols, configuration, or any file text.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":      map[string]string{"type": "string", "description": "Project, repository, product, or description search text"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}, "description": "Optional source filter"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		handler: handleSearchRepositories,
	},
	{
		name:        toolcatalog.SearchSource,
		description: "Searches file contents through the connected Bitbucket or GitLab code search API across accessible repositories. Prefer search-code, which runs this search and repository discovery together.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":      map[string]string{"type": "string", "description": "Code, symbol, API, or text query"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
			"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		handler: handleSearchSource,
	},
	{
		name:        toolcatalog.SearchCode,
		description: "Primary code search. Finds repositories AND matching file contents in one call, without a library ID, and falls back to the live Bitbucket or GitLab code search API for repositories that are not indexed yet. Use this first for any question about source code, symbols, configuration, or where something is implemented.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":      map[string]string{"type": "string", "description": "Natural-language request, repository name, code symbol, API, or text query"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
			"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or /library/id to search inside one repository"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		handler: handleSearchCode,
	},
	{
		name:        toolcatalog.FindFile,
		description: "Finds files by name or path across accessible repositories. Use it for questions like where a Dockerfile, migration, config or module lives. Supports plain names (README), globs (*.tf, auth*.py) and path patterns (**/migrations/*.sql, internal/**/service.go).",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pattern"}, "properties": map[string]any{
			"pattern":    map[string]string{"type": "string", "description": "File name, glob or path pattern. Without a slash it matches the file name, with a slash the whole path."},
			"libraryId":  map[string]string{"type": "string", "description": "Optional Context7 library ID scope"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
			"project":    map[string]string{"type": "string", "description": "Optional project key or namespace"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}},
		handler: handleFindFile,
	},
	{
		name:        toolcatalog.ReadFile,
		description: "Reads one file from a repository, optionally a line range. Use it after find-file or search-code to see the whole file instead of a snippet. Files without indexed content are read live from the source server; credentials are masked and long files are truncated with the range stated.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{
			"path":       map[string]string{"type": "string", "description": "Repository-relative file path, for example charts/values.yaml"},
			"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when the path exists in more than one repository"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"startLine":  map[string]any{"type": "integer", "minimum": 1, "description": "Optional first line, 1-based"},
			"endLine":    map[string]any{"type": "integer", "minimum": 1, "description": "Optional last line, inclusive"}}},
		handler: handleReadFile,
	},
	{
		name:        toolcatalog.SearchSemantic,
		description: "Finds code and documentation across accessible repositories without requiring a library ID. In hybrid mode it searches by meaning; when embeddings are disabled or unavailable it transparently uses ACL-safe keyword and Bitbucket/GitLab query search.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":      map[string]string{"type": "string", "description": "Describe the behaviour or concept in natural language"},
			"libraryId":  map[string]string{"type": "string", "description": "Optional library scope"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		handler: handleSearchSemantic,
	},
	{
		name:        toolcatalog.FindDependents,
		description: "Finds every accessible repository that imports, calls or otherwise depends on a symbol, module, table or service. Use it before changing shared code to see who breaks; trace-dependencies only covers one repository.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"target"}, "properties": map[string]any{
			"target":     map[string]string{"type": "string", "description": "Imported module, called symbol, table or service name"},
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 300}}},
		handler: handleFindDependents,
	},
	{
		name:        toolcatalog.SearchMergeRequests,
		description: "Searches GitLab merge requests and Bitbucket pull requests. Use it for why-questions: the reasoning, trade-offs and rollout notes live in the request description, not in the code or the commit subject.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"query":      map[string]string{"type": "string", "description": "Text matched against title and description"},
			"libraryId":  map[string]string{"type": "string", "description": "Optional library scope"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"state":      map[string]any{"type": "string", "enum": []string{"all", "open", "merged", "closed"}},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}},
		handler: handleSearchMergeRequests,
	},
	{
		name:        toolcatalog.GetFileHistory,
		description: "Lists the commits that changed a file, newest first, with author, date and message. Use it to explain why code looks the way it does, when a behaviour changed, or who to ask.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{
			"path":       map[string]string{"type": "string", "description": "Repository-relative file path"},
			"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when the path exists in more than one repository"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}},
		handler: handleGetFileHistory,
	},
	{
		name:        toolcatalog.ListDirectory,
		description: "Lists the immediate contents of a repository directory, folders first. Use it to orient yourself before reading files.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"path":       map[string]string{"type": "string", "description": "Directory path; omit or use an empty string for the repository root"},
			"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when several repositories are accessible"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"}}},
		handler: handleListDirectory,
	},
	{
		name:        toolcatalog.GetRepositoryMap,
		description: "Returns the indexed languages, directories, key files, and entry points for a repository.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string", "description": "Context7-compatible library ID"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"}}},
		usesLibraryID: true,
		handler:       handleGetRepositoryMap,
	},
	{
		name:        toolcatalog.FindSymbol,
		description: "Finds functions, methods, classes, interfaces, and database objects in accessible repositories.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":     map[string]string{"type": "string", "description": "Symbol name or signature"},
			"libraryId": map[string]string{"type": "string", "description": "Optional library scope"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"},
			"kind":      map[string]string{"type": "string", "description": "Optional symbol kind"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}},
		usesLibraryID: true,
		handler:       handleFindSymbol,
	},
	{
		name:        toolcatalog.GetSymbolContext,
		description: "Returns an indexed symbol definition, documentation, source context, and location.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "symbol"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string"},
			"symbol":    map[string]string{"type": "string", "description": "Exact or partial qualified symbol name"},
			"ref":       map[string]string{"type": "string"}}},
		usesLibraryID: true,
		handler:       handleGetSymbolContext,
	},
	{
		name:        toolcatalog.TraceDependencies,
		description: "Traces imports, calls, and data dependencies for a symbol or module.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "symbol"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string"},
			"symbol":    map[string]string{"type": "string"},
			"ref":       map[string]string{"type": "string"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}},
		usesLibraryID: true,
		handler:       handleTraceDependencies,
	},
	{
		name:        toolcatalog.CompareRefs,
		description: "Compares indexed symbols between two branches or tags.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "baseRef", "headRef"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string"},
			"baseRef":   map[string]string{"type": "string"},
			"headRef":   map[string]string{"type": "string"}}},
		usesLibraryID: true,
		handler:       handleCompareRefs,
	},
	{
		name:        toolcatalog.GetChangeImpact,
		description: "Combines ref differences with incoming source dependencies to identify affected code.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "baseRef", "headRef"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string"},
			"baseRef":   map[string]string{"type": "string"},
			"headRef":   map[string]string{"type": "string"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}},
		usesLibraryID: true,
		handler:       handleGetChangeImpact,
	},
	{
		name:        toolcatalog.GetContextPack,
		description: "Searches a curated multi-repository context pack while enforcing ACL per repository.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"pack", "query"}, "properties": map[string]any{
			"pack":  map[string]string{"type": "string", "description": "Context pack slug"},
			"query": map[string]string{"type": "string"}}},
		handler: handleGetContextPack,
	},
	{
		name:        toolcatalog.FindRunbook,
		description: "Finds operational runbooks and playbooks in accessible indexed repositories.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":     map[string]string{"type": "string"},
			"libraryId": map[string]string{"type": "string"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		usesLibraryID: true,
		handler:       handleFindRunbook,
	},
	{
		name:        toolcatalog.ExportContext,
		description: "Exports ACL-filtered repository context as a bounded Markdown bundle with an untrusted-data safety label.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryIds", "query"}, "properties": map[string]any{
			"libraryIds": map[string]any{"type": "array", "minItems": 1, "maxItems": 20, "items": map[string]string{"type": "string"}},
			"query":      map[string]string{"type": "string"}}},
		handler: handleExportContext,
	},
	{
		name:        toolcatalog.ExplainSearchResult,
		description: "Explains keyword matches, retrieval mode, source lines, and embedding metadata for accessible search candidates.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "query"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string"},
			"query":     map[string]string{"type": "string"},
			"ref":       map[string]string{"type": "string"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 50}}},
		usesLibraryID: true,
		handler:       handleExplainSearchResult,
	},
	{
		name:        toolcatalog.BuildContext,
		description: "Assembles everything needed before changing a symbol: who calls it across accessible repositories, what it depends on, the tests that cover it, and its recent history. Ask this instead of running find-symbol, find-dependents, trace-dependencies and find-file yourself; it applies the repository ACL once and fits the result to a token budget, saying what did not fit.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"query"}, "properties": map[string]any{
			"query":     map[string]string{"type": "string", "description": "What you are about to change, naming the type, function, table or service, for example \"OrderService 수정하려는데 영향 범위\""},
			"libraryId": map[string]string{"type": "string", "description": "Optional library scope; use it when the same symbol name exists in more than one repository"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"}}},
		usesLibraryID: true,
		handler:       handleBuildContext,
	},
	{
		name:        toolcatalog.FindCodeOwner,
		description: "Ranks the people who have worked on a file or directory, weighted so recent work outranks old work. Use it to find who to ask before changing unfamiliar code. git blame names whoever touched a line last, which is often whoever ran a formatter; this counts sustained involvement and reports the commit count and dates behind each name.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"path"}, "properties": map[string]any{
			"path":       map[string]string{"type": "string", "description": "Repository-relative file or directory, for example internal/search or internal/search/service.go"},
			"libraryId":  map[string]string{"type": "string", "description": "Library ID; required when the path exists in more than one repository"},
			"repository": map[string]string{"type": "string", "description": "Optional repository slug or library ID"},
			"ref":        map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 20}}},
		usesLibraryID: true,
		handler:       handleFindCodeOwner,
	},
	{
		name:        toolcatalog.FindTests,
		description: "Finds the tests that exercise a symbol. Tests the dependency graph shows calling or importing it are reported separately from tests merely named after it or sitting beside it, because the first will fail if the change is wrong and the second might not touch the code at all. Use it to decide what to run before or after a change.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"symbol"}, "properties": map[string]any{
			"symbol":    map[string]string{"type": "string", "description": "The type, function or method being changed, for example OrderService"},
			"libraryId": map[string]string{"type": "string", "description": "Optional library scope; use it when the same symbol name exists in more than one repository"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"},
			"limit":     map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}},
		usesLibraryID: true,
		handler:       handleFindTests,
	},
	{
		name:        toolcatalog.FindDependencyUsage,
		description: "Reports which repositories declare a third-party package and at which versions, read from their manifests (go.mod, package.json, pom.xml, build.gradle, requirements.txt, pyproject.toml, Cargo.toml). Pass fixedIn to have each version judged against an advisory fix. Use it for an advisory (\"who is on the affected version\") or an upgrade plan; find-dependents answers the different question of who imports a symbol, and cannot see versions or transitive dependencies at all.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"name"}, "properties": map[string]any{
			"name":       map[string]string{"type": "string", "description": "Package name as its ecosystem writes it: github.com/gin-gonic/gin, lodash, org.apache.logging.log4j:log4j-core"},
			"ecosystem":  map[string]string{"type": "string", "description": "Optional filter: go, npm, maven, gradle, pypi, cargo"},
			"sourceType": map[string]string{"type": "string", "description": "Optional filter: bitbucket or gitlab"},
			"fixedIn":    map[string]string{"type": "string", "description": "Advisory fix version, for example 2.17.1. Each version group is then labelled affected, safe or undecidable; a range or floating version is never called safe"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 500}}},
		handler: handleFindDependencyUsage,
	},
	{
		name:        toolcatalog.GetArchitectureMap,
		description: "Reports what each accessible repository appears to be -- HTTP service, database user, message consumer, scheduler -- inferred from what it imports, and which repositories reference one another. Every claim carries the imports behind it. Endpoint paths, topic names and SQL embedded in application code are not indexed and are not reported.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"sourceType": map[string]any{"type": "string", "enum": []string{"bitbucket", "gitlab", "confluence", "jira"}, "description": "Optional source filter"},
			"limit":      map[string]any{"type": "integer", "minimum": 1, "maximum": 200}}},
		handler: handleArchitectureMap,
	},
	{
		name:        toolcatalog.AssessChangeRisk,
		description: "Assesses what a change between two refs puts at risk: symbols removed or resignatured, consumers in OTHER repositories, whether any test references what changed, schema files touched, and whether the index is fresh enough for the consumer list to be trusted. Reports named factors with their evidence rather than a single score, so a reader who disagrees with one can discount it and keep the rest.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId", "baseRef", "headRef"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string", "description": "Context7-compatible library ID of the repository being changed"},
			"baseRef":   map[string]string{"type": "string", "description": "The ref being compared from, for example main"},
			"headRef":   map[string]string{"type": "string", "description": "The ref carrying the change"}}},
		usesLibraryID: true,
		handler:       handleAssessChangeRisk,
	},
	{
		name:        toolcatalog.GetRepositoryHealth,
		description: "Counts what the index can support for one repository: symbols a test references, symbols nothing references, files that explain how to contribute, and how old the index is. Reports the counts with what they were counted from, plus what it deliberately did not measure, so a short report is not mistaken for a clean bill of health.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string", "description": "Context7-compatible library ID of the repository"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag"}}},
		usesLibraryID: true,
		handler:       handleRepositoryHealth,
	},
	{
		name:        toolcatalog.GetPlatformStatus,
		description: "Returns administrative MCP, source, index, database, and effective embedding retrieval status. Requires an administrator MCP API key.",
		schema:      map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{}},
		adminRoles:  []string{"platform-admin", "readonly-operator", "source-admin", "mcp-admin", "search-admin", "security-admin", "auditor"},
		handler:     handleGetPlatformStatus,
	},
	{
		name:        toolcatalog.ListIndexJobs,
		description: "Lists recent indexing jobs for source administrators and operators using an MCP API key.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "properties": map[string]any{
			"status": map[string]any{"type": "string", "enum": []string{"pending", "running", "completed", "failed"}},
			"limit":  map[string]any{"type": "integer", "minimum": 1, "maximum": 100}}},
		adminRoles: []string{"source-admin", "readonly-operator"},
		handler:    handleListIndexJobs,
	},
	{
		name:        toolcatalog.ReindexRepository,
		description: "Queues an idempotent repository reindex job. Requires a source administrator MCP API key.",
		schema: map[string]any{"type": "object", "additionalProperties": false, "required": []string{"libraryId"}, "properties": map[string]any{
			"libraryId": map[string]string{"type": "string", "description": "Repository library ID such as /project/repository"},
			"ref":       map[string]string{"type": "string", "description": "Optional branch or tag; defaults to the repository default branch"}}},
		adminRoles:    []string{"source-admin"},
		usesLibraryID: true,
		handler:       handleReindexRepository,
	},
}

// registryByName indexes the registry so a call resolves its tool in one lookup
// instead of scanning a freshly rebuilt catalog.
var registryByName = func() map[string]*tool {
	index := make(map[string]*tool, len(registry))
	for i := range registry {
		index[registry[i].name] = &registry[i]
	}
	return index
}()

func lookupTool(name string) (*tool, bool) {
	found, ok := registryByName[name]
	return found, ok
}

// allowed reports whether this credential may call an administrative tool.
func (t *tool) allowed(p auth.Principal) bool {
	if len(t.adminRoles) == 0 {
		return true
	}
	if p.KeyID == "" {
		return false
	}
	for _, role := range t.adminRoles {
		if p.HasRole(role) {
			return true
		}
	}
	return false
}
