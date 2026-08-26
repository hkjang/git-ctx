package mcp

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
)

// The handlers below were one 365-line switch in call(). Each is now reachable
// only through its registry entry, so a tool's schema, its authorisation and its
// implementation sit together instead of drifting apart in three places.

func handleResolveLibraryId(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var items []search.Library
	items, err = s.search.Resolve(r.Context(), principalACLs(p), stringArg(args, "libraryName"), stringArg(args, "query"))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			items = filterLibraries(items, p.AllowedRepositories)
		}
		empty = len(items) == 0
		text = formatLibraries(items)
	}
	return text, empty, err
}

func handleQueryDocs(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		text, err = s.search.Query(r.Context(), principalACLs(p), libraryID, stringArg(args, "query"))
	}
	return text, empty, err
}

func handleSearchRepositories(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var items []search.RepositoryResult
	items, err = s.search.SearchRepositories(r.Context(), principalACLs(p), stringArg(args, "query"), stringArg(args, "sourceType"), intArg(args, "limit", 20))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			filtered := items[:0]
			for _, item := range items {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					filtered = append(filtered, item)
				}
			}
			items = filtered
		}
		empty = len(items) == 0
		text = formatRepositorySearch(items, stringArg(args, "query"))
	}
	return text, empty, err
}

func handleSearchSource(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var hits []search.SourceResult
	hits, err = s.search.SearchSource(r.Context(), principalACLs(p), stringArg(args, "query"), stringArg(args, "sourceType"), stringArg(args, "project"), stringArg(args, "repository"), stringArg(args, "ref"), intArg(args, "limit", 20))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			filtered := hits[:0]
			for _, hit := range hits {
				if libraryAllowed(hit.LibraryID, p.AllowedRepositories) {
					filtered = append(filtered, hit)
				}
			}
			hits = filtered
		}
		empty = len(hits) == 0
		text = formatSourceResults(hits)
	}
	return text, empty, err
}

func handleSearchCode(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var result search.CodeSearchResult
	result, err = s.search.SearchCode(r.Context(), principalACLs(p), stringArg(args, "query"), stringArg(args, "sourceType"), stringArg(args, "project"), stringArg(args, "repository"), stringArg(args, "ref"), intArg(args, "limit", 20))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			repositories := result.Repositories[:0]
			for _, item := range result.Repositories {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					repositories = append(repositories, item)
				}
			}
			result.Repositories = repositories
			hits := result.Hits[:0]
			for _, hit := range result.Hits {
				if libraryAllowed(hit.LibraryID, p.AllowedRepositories) {
					hits = append(hits, hit)
				}
			}
			result.Hits = hits
		}
		empty = len(result.Repositories) == 0 && len(result.Hits) == 0
		text = formatCodeSearch(result)
	}
	return text, empty, err
}

func handleFindFile(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var files search.FileSearchResult
	files, err = s.search.FindFiles(r.Context(), principalACLs(p), stringArg(args, "pattern"), stringArg(args, "libraryId"),
		stringArg(args, "sourceType"), stringArg(args, "project"), stringArg(args, "repository"),
		stringArg(args, "ref"), intArg(args, "limit", 50))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			allowed := files.Files[:0]
			for _, item := range files.Files {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					allowed = append(allowed, item)
				}
			}
			files.Files = allowed
		}
		empty = len(files.Files) == 0
		text = formatFileResults(files)
	}
	return text, empty, err
}

func handleReadFile(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var file search.FileContent
	file, err = s.search.ReadFile(r.Context(), principalACLs(p), stringArg(args, "libraryId"), stringArg(args, "repository"),
		stringArg(args, "path"), stringArg(args, "ref"), intArg(args, "startLine", 0), intArg(args, "endLine", 0))
	if err == nil && p.KeyID != "" && !libraryAllowed(file.LibraryID, p.AllowedRepositories) {
		err = errors.New("file is unavailable or access is denied")
	}
	if err == nil {
		text = formatFileContent(file)
	}
	return text, empty, err
}

func handleSearchSemantic(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var semantic search.SemanticSearch
	semantic, err = s.search.SemanticSearch(r.Context(), principalACLs(p), stringArg(args, "query"),
		stringArg(args, "libraryId"), stringArg(args, "sourceType"), intArg(args, "limit", 10))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			allowed := semantic.Hits[:0]
			for _, item := range semantic.Hits {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					allowed = append(allowed, item)
				}
			}
			semantic.Hits = allowed
		}
		empty = len(semantic.Hits) == 0
		text = formatSemanticSearch(semantic)
	}
	return text, empty, err
}

func handleFindDependents(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var dependents search.DependentSearch
	dependents, err = s.search.FindDependents(r.Context(), principalACLs(p), stringArg(args, "target"),
		stringArg(args, "sourceType"), intArg(args, "limit", 100))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			allowed := dependents.Dependents[:0]
			for _, item := range dependents.Dependents {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					allowed = append(allowed, item)
				}
			}
			dependents.Dependents = allowed
		}
		empty = len(dependents.Dependents) == 0
		text = formatDependents(dependents)
	}
	return text, empty, err
}

func handleSearchMergeRequests(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var requests search.ChangeRequestSearch
	requests, err = s.search.SearchChangeRequests(r.Context(), principalACLs(p), stringArg(args, "query"),
		stringArg(args, "libraryId"), stringArg(args, "repository"), stringArg(args, "state"),
		intArg(args, "limit", 20))
	if err == nil {
		if len(p.AllowedRepositories) > 0 {
			allowed := requests.Requests[:0]
			for _, item := range requests.Requests {
				if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
					allowed = append(allowed, item)
				}
			}
			requests.Requests = allowed
		}
		empty = len(requests.Requests) == 0
		text = formatChangeRequests(requests)
	}
	return text, empty, err
}

func handleGetFileHistory(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var history search.FileHistory
	history, err = s.search.FileHistory(r.Context(), principalACLs(p), stringArg(args, "libraryId"), stringArg(args, "repository"),
		stringArg(args, "path"), stringArg(args, "ref"), intArg(args, "limit", 20))
	if err == nil && p.KeyID != "" && !libraryAllowed(history.LibraryID, p.AllowedRepositories) {
		err = errors.New("file is unavailable or access is denied")
	}
	if err == nil {
		empty = len(history.Commits) == 0
		text = formatFileHistory(history)
	}
	return text, empty, err
}

func handleListDirectory(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var listing search.DirectoryListing
	listing, err = s.search.ListDirectory(r.Context(), principalACLs(p), stringArg(args, "libraryId"), stringArg(args, "repository"),
		stringArg(args, "path"), stringArg(args, "ref"))
	if err == nil && p.KeyID != "" && !libraryAllowed(listing.LibraryID, p.AllowedRepositories) {
		err = errors.New("directory is unavailable or access is denied")
	}
	if err == nil {
		empty = len(listing.Entries) == 0
		text = formatDirectory(listing)
	}
	return text, empty, err
}

func handleGetRepositoryMap(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var item search.RepositoryMap
		item, err = s.search.RepositoryMap(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"))
		if err == nil {
			text = formatRepositoryMap(item)
		}
	}
	return text, empty, err
}

func handleFindSymbol(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var items []search.SymbolResult
		items, err = s.search.FindSymbols(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"), stringArg(args, "query"), stringArg(args, "kind"), intArg(args, "limit", 20))
		if err == nil {
			if len(p.AllowedRepositories) > 0 {
				filtered := items[:0]
				for _, item := range items {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						filtered = append(filtered, item)
					}
				}
				items = filtered
			}
			text = formatSymbols(items)
		}
	}
	return text, empty, err
}

func handleGetSymbolContext(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var item search.SymbolResult
		item, err = s.search.SymbolContext(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"), stringArg(args, "symbol"))
		if err == nil {
			text = formatSymbolContext(item)
		}
	}
	return text, empty, err
}

func handleTraceDependencies(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var items []search.DependencyResult
		items, err = s.search.TraceDependencies(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"), stringArg(args, "symbol"), intArg(args, "limit", 50))
		if err == nil {
			text = formatDependencies(items)
		}
	}
	return text, empty, err
}

func handleCompareRefs(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var item search.RefComparison
		item, err = s.search.CompareRefs(r.Context(), principalACLs(p), libraryID, stringArg(args, "baseRef"), stringArg(args, "headRef"))
		if err == nil {
			text = formatRefComparison(item)
		}
	}
	return text, empty, err
}

func handleGetChangeImpact(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var item search.ChangeImpact
		item, err = s.search.ChangeImpact(r.Context(), principalACLs(p), libraryID, stringArg(args, "baseRef"), stringArg(args, "headRef"), intArg(args, "limit", 100))
		if err == nil {
			text = formatChangeImpact(item)
		}
	}
	return text, empty, err
}

func handleGetContextPack(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var item search.ContextPackResult
	item, err = s.search.ContextPackFor(r.Context(), principalACLs(p), p.AllowedRepositories, stringArg(args, "pack"), stringArg(args, "query"))
	if err == nil {
		text = fmt.Sprintf("# Context Pack: %s\n\n%s\n\n%s", item.Name, item.Description, item.Content)
	}
	return text, empty, err
}

func handleFindRunbook(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var items []search.RunbookResult
		items, err = s.search.FindRunbooks(r.Context(), principalACLs(p), libraryID, stringArg(args, "query"), intArg(args, "limit", 10))
		if err == nil {
			// libraryId is optional here, so checking it is not a filter: a key
			// restricted to one repository was reading the runbooks of every
			// repository its user can see simply by leaving it out.
			if len(p.AllowedRepositories) > 0 {
				kept := items[:0]
				for _, item := range items {
					if libraryAllowed(item.LibraryID, p.AllowedRepositories) {
						kept = append(kept, item)
					}
				}
				items = kept
			}
			empty = len(items) == 0
			text = formatRunbooks(items)
		}
	}
	return text, empty, err
}

func handleExportContext(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraries := stringSliceArg(args, "libraryIds")
	if p.KeyID != "" {
		for _, id := range libraries {
			if !libraryAllowed(id, p.AllowedRepositories) {
				err = errors.New("context is unavailable or access is denied")
				break
			}
		}
	}
	if err == nil {
		text, err = s.search.ExportContext(r.Context(), principalACLs(p), libraries, stringArg(args, "query"))
	}
	return text, empty, err
}

func handleExplainSearchResult(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		err = errors.New("library is unavailable or access is denied")
	} else {
		var item search.SearchExplanation
		item, err = s.search.ExplainSearch(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"), stringArg(args, "query"), intArg(args, "limit", 10))
		if err == nil {
			text = formatSearchExplanation(item)
		}
	}
	return text, empty, err
}

func handleGetPlatformStatus(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	text, err = s.platformStatus(r.Context())
	return text, empty, err
}

func handleListIndexJobs(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	text, err = s.indexJobs(r.Context(), p, stringArg(args, "status"), intArg(args, "limit", 20))
	return text, empty, err
}

func handleReindexRepository(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	text, err = s.reindexRepository(r.Context(), p, libraryID, stringArg(args, "ref"))
	return text, empty, err
}

func handleBuildContext(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", false, errors.New("library is unavailable or access is denied")
	}
	var bundle search.ContextBundle
	bundle, err = s.search.BuildChangeContext(r.Context(), principalACLs(p), p.AllowedRepositories,
		stringArg(args, "query"), libraryID, stringArg(args, "ref"), s.responseBudget(r.Context(), "build-context"))
	if err != nil {
		return "", false, err
	}
	// An unresolved target is a real answer -- the caller has to narrow it -- so
	// it is not reported as an empty result.
	empty = len(bundle.Sections) == 0 && len(bundle.Ambiguous) == 0
	return bundle.Render(), empty, nil
}

func handleFindCodeOwner(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", false, errors.New("library is unavailable or access is denied")
	}
	var result search.OwnershipResult
	result, err = s.search.FindOwners(r.Context(), principalACLs(p), libraryID, stringArg(args, "repository"),
		stringArg(args, "path"), stringArg(args, "ref"), intArg(args, "limit", 5))
	if err != nil {
		return "", false, err
	}
	if p.KeyID != "" && !libraryAllowed(result.LibraryID, p.AllowedRepositories) {
		return "", false, errors.New("path is unavailable or access is denied")
	}
	return search.FormatOwners(result), len(result.Owners) == 0, nil
}

func handleFindDependencyUsage(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var result search.DependencyUsage
	result, err = s.search.FindDependencyUsage(r.Context(), principalACLs(p), stringArg(args, "name"),
		stringArg(args, "ecosystem"), stringArg(args, "sourceType"), stringArg(args, "fixedIn"), intArg(args, "limit", 100))
	if err != nil {
		return "", false, err
	}
	// A key restricted to certain repositories must not learn the inventory of
	// the others, including through the version grouping.
	if len(p.AllowedRepositories) > 0 {
		users := result.Users[:0]
		for _, user := range result.Users {
			if libraryAllowed(user.LibraryID, p.AllowedRepositories) {
				users = append(users, user)
			}
		}
		result.Users = users
		result = rebuildDependencyVersions(result)
	}
	return search.FormatDependencyUsage(result), len(result.Users) == 0, nil
}

// rebuildDependencyVersions recomputes the version grouping after a key's
// repository restriction removed declarations, so the summary counts never
// describe repositories the caller may not see.
func rebuildDependencyVersions(result search.DependencyUsage) search.DependencyUsage {
	versions := map[string]map[string]bool{}
	repositories := map[string]bool{}
	for _, user := range result.Users {
		declared := user.Version
		if strings.TrimSpace(declared) == "" {
			declared = "(선언 없음)"
		}
		if versions[declared] == nil {
			versions[declared] = map[string]bool{}
		}
		versions[declared][user.LibraryID] = true
		repositories[user.LibraryID] = true
	}
	rebuilt := result
	rebuilt.Repositories = len(repositories)
	rebuilt.Versions = rebuilt.Versions[:0]
	for version, libraries := range versions {
		list := make([]string, 0, len(libraries))
		for library := range libraries {
			list = append(list, library)
		}
		sort.Strings(list)
		rebuilt.Versions = append(rebuilt.Versions, search.DependencyVersion{Version: version, Repositories: list})
	}
	sort.SliceStable(rebuilt.Versions, func(i, j int) bool {
		return len(rebuilt.Versions[i].Repositories) > len(rebuilt.Versions[j].Repositories)
	})
	return rebuilt
}

func handleFindTests(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && libraryID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", false, errors.New("library is unavailable or access is denied")
	}
	var result search.TestSearch
	result, err = s.search.FindTests(r.Context(), principalACLs(p), stringArg(args, "symbol"), libraryID,
		stringArg(args, "ref"), intArg(args, "limit", 20))
	if err != nil {
		return "", false, err
	}
	if len(p.AllowedRepositories) > 0 {
		allowed := result.Tests[:0]
		for _, test := range result.Tests {
			if libraryAllowed(test.LibraryID, p.AllowedRepositories) {
				allowed = append(allowed, test)
			}
		}
		result.Tests = allowed
	}
	return search.FormatTests(result), len(result.Tests) == 0, nil
}

func handleArchitectureMap(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	var result search.ArchitectureMap
	result, err = s.search.ArchitectureOverview(r.Context(), principalACLs(p), stringArg(args, "sourceType"), intArg(args, "limit", 50))
	if err != nil {
		return "", false, err
	}
	if len(p.AllowedRepositories) > 0 {
		components := result.Components[:0]
		for _, component := range result.Components {
			if libraryAllowed(component.LibraryID, p.AllowedRepositories) {
				components = append(components, component)
			}
		}
		result.Components = components
		edges := result.Edges[:0]
		for _, edge := range result.Edges {
			if libraryAllowed(edge.From, p.AllowedRepositories) && libraryAllowed(edge.To, p.AllowedRepositories) {
				edges = append(edges, edge)
			}
		}
		result.Edges = edges
	}
	return search.FormatArchitecture(result), len(result.Components) == 0, nil
}

func handleAssessChangeRisk(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", false, errors.New("library is unavailable or access is denied")
	}
	var result search.ChangeAssessment
	result, err = s.search.AssessChange(r.Context(), principalACLs(p), libraryID,
		stringArg(args, "baseRef"), stringArg(args, "headRef"))
	if err != nil {
		return "", false, err
	}
	return search.FormatAssessment(result), len(result.Comparison.Changes) == 0, nil
}

func handleRepositoryHealth(s *Server, r *http.Request, p auth.Principal, args map[string]any) (text string, empty bool, err error) {
	libraryID := stringArg(args, "libraryId")
	if p.KeyID != "" && !libraryAllowed(libraryID, p.AllowedRepositories) {
		return "", false, errors.New("library is unavailable or access is denied")
	}
	var result search.RepositoryHealth
	result, err = s.search.RepositoryHealthReport(r.Context(), principalACLs(p), libraryID, stringArg(args, "ref"))
	if err != nil {
		return "", false, err
	}
	return search.FormatHealth(result), false, nil
}
