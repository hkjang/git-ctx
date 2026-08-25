// Package toolcatalog defines the MCP tool names that may be granted to an
// API key. It deliberately contains names only: protocol schemas, handlers and
// role policy remain in the MCP package, while packages such as apikey can
// depend on this small catalogue without creating an import cycle.
package toolcatalog

const (
	ResolveLibraryID    = "resolve-library-id"
	QueryDocs           = "query-docs"
	SearchRepositories  = "search-repositories"
	SearchSource        = "search-source"
	SearchCode          = "search-code"
	FindFile            = "find-file"
	ReadFile            = "read-file"
	SearchSemantic      = "search-semantic"
	FindDependents      = "find-dependents"
	SearchMergeRequests = "search-merge-requests"
	GetFileHistory      = "get-file-history"
	ListDirectory       = "list-directory"
	GetRepositoryMap    = "get-repository-map"
	FindSymbol          = "find-symbol"
	GetSymbolContext    = "get-symbol-context"
	TraceDependencies   = "trace-dependencies"
	CompareRefs         = "compare-refs"
	GetChangeImpact     = "get-change-impact"
	GetContextPack      = "get-context-pack"
	FindRunbook         = "find-runbook"
	ExportContext       = "export-context"
	ExplainSearchResult = "explain-search-result"
	BuildContext        = "build-context"
	FindCodeOwner       = "find-code-owner"
	FindTests           = "find-tests"
	GetArchitectureMap  = "get-architecture-map"
	AssessChangeRisk    = "assess-change-risk"
	GetRepositoryHealth = "get-repository-health"
	GetPlatformStatus   = "get-platform-status"
	ListIndexJobs       = "list-index-jobs"
	ReindexRepository   = "reindex-repository"
)

var names = []string{
	ResolveLibraryID,
	QueryDocs,
	SearchRepositories,
	SearchSource,
	SearchCode,
	FindFile,
	ReadFile,
	SearchSemantic,
	FindDependents,
	SearchMergeRequests,
	GetFileHistory,
	ListDirectory,
	GetRepositoryMap,
	FindSymbol,
	GetSymbolContext,
	TraceDependencies,
	CompareRefs,
	GetChangeImpact,
	GetContextPack,
	FindRunbook,
	ExportContext,
	ExplainSearchResult,
	BuildContext,
	FindCodeOwner,
	FindTests,
	GetArchitectureMap,
	AssessChangeRisk,
	GetRepositoryHealth,
	GetPlatformStatus,
	ListIndexJobs,
	ReindexRepository,
}

var supported = func() map[string]struct{} {
	result := make(map[string]struct{}, len(names))
	for _, name := range names {
		result[name] = struct{}{}
	}
	return result
}()

// Names returns a copy in the order tools are presented by the MCP catalogue.
func Names() []string {
	return append([]string(nil), names...)
}

// Supports reports whether name can be stored as an API-key scope.
func Supports(name string) bool {
	_, ok := supported[name]
	return ok
}
