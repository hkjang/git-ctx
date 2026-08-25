package mcp

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"git-ctx/internal/search"
)

// Renderers that turn search results into the Markdown an agent reads.

func formatLibraries(items []search.Library) string {
	if len(items) == 0 {
		return "No accessible libraries matched. Check the name or use a broader query."
	}
	var b strings.Builder
	b.WriteString("Available Libraries:\n")
	for _, x := range items {
		fmt.Fprintf(&b, "\n- Name: %s\n- Library ID: %s\n- Description: %s\n- Code Snippets: %d\n- Source Reputation: %s\n- Versions: %s\n", x.Name, x.ID, x.Description, x.Snippets, x.Reputation, strings.Join(x.Versions, ", "))
		if x.Snippets == 0 {
			b.WriteString("- Note: not indexed yet; query-docs answers this library from the live source code search API.\n")
		}
	}
	return b.String()
}

func formatRepositories(items []search.RepositoryResult) string {
	if len(items) == 0 {
		return "No accessible Bitbucket or GitLab repositories matched the query."
	}
	var b strings.Builder
	b.WriteString("## Accessible Repositories\n")
	for _, item := range items {
		indexed := "not indexed"
		if item.IndexedAt.Valid {
			indexed = item.IndexedAt.Time.UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&b, "\n- Name: %s\n  Library ID: %s\n  Source: %s\n  Project: %s\n  Repository: %s\n  Default Branch: %s\n  Indexed At: %s\n  Description: %s\n", item.Name, item.LibraryID, item.SourceType, item.ProjectKey, item.Slug, item.DefaultBranch, indexed, item.Description)
	}
	return b.String()
}

func formatSourceResults(items []search.SourceResult) string {
	if len(items) == 0 {
		return "No source matches were found in accessible repositories. Broaden the query, check the source connection, or run an index."
	}
	var b strings.Builder
	b.WriteString("## Source Search Results\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s · %s\n\n%s\n\nSource: %s://%s/%s@%s/%s#L%d-L%d\n", item.LibraryID, item.Path, item.Snippet, item.SourceType, item.ProjectKey, item.RepositorySlug, item.CommitID, item.Path, item.LineStart, item.LineEnd)
	}
	return b.String()
}

// formatRepositorySearch appends the next step so a coding agent that asked for
// repositories does not stop before it has any code.
func formatRepositorySearch(items []search.RepositoryResult, query string) string {
	text := formatRepositories(items)
	if len(items) == 0 {
		return text
	}
	return text + fmt.Sprintf("\nThis tool matched repository names only. For file contents call `search-code {\"query\":%q}`.\n", query)
}

func formatCodeSearch(result search.CodeSearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Code Search\n\nNormalized query: `%s`\n", result.Query)
	if result.Warning != "" {
		fmt.Fprintf(&b, "\n> %s\n", result.Warning)
	}
	b.WriteString("\n### Repository Matches\n")
	if len(result.Repositories) == 0 {
		b.WriteString("\nNo repository names or descriptions matched.\n")
	} else {
		for _, item := range result.Repositories {
			fmt.Fprintf(&b, "\n- **%s** — `%s` (%s, default ref `%s`)\n", item.Name, item.LibraryID, item.SourceType, item.DefaultBranch)
			if item.Description != "" {
				fmt.Fprintf(&b, "  %s\n", item.Description)
			}
		}
	}
	fmt.Fprintf(&b, "\n### Source Matches (%d)\n", len(result.Hits))
	if len(result.Hits) == 0 {
		b.WriteString("\nNo file contents matched. This is not the same as \"the code does not exist\": check the notes below.\n")
		if len(result.Repositories) > 0 {
			fmt.Fprintf(&b, "\nNext step: retry with a narrower scope, for example `search-code {\"query\":%q,\"repository\":%q}`, or use `find-symbol` for an exact identifier.\n",
				result.Query, result.Repositories[0].LibraryID)
		}
	} else {
		for _, item := range result.Hits {
			fmt.Fprintf(&b, "\n#### %s · %s\n\n%s\n\nSource: %s://%s/%s@%s/%s#L%d-L%d\n", item.LibraryID, item.Path, item.Snippet, item.SourceType, item.ProjectKey, item.RepositorySlug, item.CommitID, item.Path, item.LineStart, item.LineEnd)
		}
	}
	// Diagnostics always ship: an agent that knows the search ran a name-only
	// path, hit a timeout or was ACL-filtered can pick a better next call
	// instead of telling the user the code is missing.
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// formatFileResults groups paths by repository and states what can be read next
// for each hit, so an agent can chain straight into content tools.
func formatFileResults(result search.FileSearchResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## File Search\n\nPattern: `%s`\nMatches: %d\n", result.Pattern, len(result.Files))
	if len(result.Files) == 0 {
		b.WriteString("\nNo file name matched. Try a wildcard such as `*.sql`, a path fragment such as `migrations/`, or `search-code` for file contents.\n")
	} else {
		current := ""
		for _, item := range result.Files {
			if item.LibraryID != current {
				current = item.LibraryID
				fmt.Fprintf(&b, "\n### %s (%s, ref `%s`)\n", item.LibraryID, item.SourceType, item.Ref)
			}
			readable := "content not indexed; use search-code or the source UI"
			if item.ContentIndexed {
				readable = "content indexed; query-docs and get-symbol-context can read it"
			}
			// Bitbucket reports file sizes, GitLab tree listings do not; only print
			// a size when the source actually gave one.
			size := ""
			if item.SizeBytes > 0 {
				size = fmt.Sprintf("%d bytes, ", item.SizeBytes)
			}
			fmt.Fprintf(&b, "- `%s` (%s%s)\n", item.Path, size, readable)
		}
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// formatFileContent returns the file in a fenced block with a citation header,
// so an agent can quote it and link back to the exact ref and lines.
func formatFileContent(file search.FileContent) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## %s\n\n`%s` · ref `%s` · lines %d-%d of %d · %s\n\n",
		file.Path, file.LibraryID, file.Ref, file.StartLine, file.EndLine, file.TotalLines, file.Origin)
	fence := "```"
	if strings.Contains(file.Content, "```") {
		fence = "````"
	}
	fmt.Fprintf(&b, "%s%s\n%s\n%s\n", fence, languageHint(file.Path), file.Content, fence)
	fmt.Fprintf(&b, "\nSource: `%s://%s/%s@%s/%s#L%d-L%d`\n", file.SourceType, file.ProjectKey, file.RepositorySlug,
		firstNonEmpty(file.CommitID, file.Ref), file.Path, file.StartLine, file.EndLine)
	if len(file.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range file.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

// languageHint labels the fenced block so clients highlight it correctly.
func languageHint(path string) string {
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "":
		return ""
	case "yml":
		return "yaml"
	case "tf", "tfvars":
		return "hcl"
	case "mod", "sum":
		return ""
	default:
		return strings.ToLower(strings.TrimPrefix(filepath.Ext(path), "."))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func formatSemanticSearch(result search.SemanticSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Semantic Search\n\nQuery: `%s`\nMatches: %d · retrieval: %s\n", result.Query, len(result.Hits), result.Mode)
	scoreLabel := "similarity"
	if strings.Contains(result.Mode, "keyword") || strings.Contains(result.Mode, "source-query") {
		scoreLabel = "rank score"
	}
	for _, hit := range result.Hits {
		fmt.Fprintf(&b, "\n### %s · %s (%s %.2f)\n\n%s\n\nSource: `%s://%s@%s/%s#L%d-L%d`\n",
			hit.LibraryID, hit.FilePath, scoreLabel, hit.Score, strings.TrimSpace(hit.Content),
			hit.SourceType, strings.TrimPrefix(hit.LibraryID, "/"), firstNonEmpty(hit.CommitID, hit.Ref), hit.FilePath, hit.LineStart, hit.LineEnd)
	}
	if len(result.Hits) == 0 {
		b.WriteString("\nNo accessible code or documentation matched. Try another term, source type, or repository scope.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatDependents(result search.DependentSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Dependents of %s\n\n%d reference(s) in %d repository(ies)\n", result.Target, len(result.Dependents), len(result.Repositories))
	current := ""
	for _, item := range result.Dependents {
		if item.LibraryID != current {
			current = item.LibraryID
			fmt.Fprintf(&b, "\n### %s (ref `%s`)\n", item.LibraryID, item.Ref)
		}
		from := item.FromSymbol
		if from == "" {
			from = "(file scope)"
		}
		fmt.Fprintf(&b, "- `%s:%d` %s %s → `%s`\n", item.FilePath, item.LineNumber, from, item.Kind, item.Target)
	}
	if len(result.Dependents) == 0 {
		b.WriteString("\nNothing depends on it in the indexed refs, or the dependency is expressed in a way the parser does not capture. Confirm with search-code before assuming it is unused.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatChangeRequests(result search.ChangeRequestSearch) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Merge and Pull Requests\n\nQuery: `%s`\nMatches: %d\n", result.Query, len(result.Requests))
	for _, item := range result.Requests {
		fmt.Fprintf(&b, "\n### %s %s — %s\n\n%s · %s · `%s` → `%s`",
			item.ID, item.Title, item.State, item.LibraryID, item.Author, item.SourceRef, item.TargetRef)
		if !item.UpdatedAt.IsZero() {
			fmt.Fprintf(&b, " · updated %s", item.UpdatedAt.UTC().Format("2006-01-02"))
		}
		b.WriteString("\n")
		if description := strings.TrimSpace(item.Description); description != "" {
			fmt.Fprintf(&b, "\n%s\n", description)
		}
		if item.URL != "" {
			fmt.Fprintf(&b, "\n%s\n", item.URL)
		}
	}
	if len(result.Requests) == 0 {
		b.WriteString("\nNo merge or pull request matched. Try a broader term, state \"all\", or get-file-history for the commits themselves.\n")
	}
	if len(result.Diagnostics) > 0 {
		b.WriteString("\n### Notes\n")
		for _, diagnostic := range result.Diagnostics {
			fmt.Fprintf(&b, "- %s\n", diagnostic)
		}
	}
	return b.String()
}

func formatFileHistory(history search.FileHistory) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## History of %s\n\n`%s` · ref `%s` · %d commit(s)\n", history.Path, history.LibraryID, history.Ref, len(history.Commits))
	for _, commit := range history.Commits {
		when := ""
		if !commit.AuthoredAt.IsZero() {
			when = commit.AuthoredAt.UTC().Format("2006-01-02 15:04 MST")
		}
		subject := commit.Message
		if index := strings.Index(subject, "\n"); index > 0 {
			subject = subject[:index]
		}
		fmt.Fprintf(&b, "\n- `%s` %s — %s\n  %s\n", commit.DisplayID, when, commit.Author, subject)
		if commit.URL != "" {
			fmt.Fprintf(&b, "  %s\n", commit.URL)
		}
	}
	for _, diagnostic := range history.Diagnostics {
		fmt.Fprintf(&b, "\n- %s\n", diagnostic)
	}
	return b.String()
}

func formatDirectory(listing search.DirectoryListing) string {
	var b strings.Builder
	root := listing.Path
	if root == "" {
		root = "(repository root)"
	}
	fmt.Fprintf(&b, "## %s\n\n`%s` · ref `%s` · %d entries\n\n", root, listing.LibraryID, listing.Ref, len(listing.Entries))
	for _, entry := range listing.Entries {
		if entry.Directory {
			fmt.Fprintf(&b, "- %s/ (%d files)\n", entry.Name, entry.Files)
			continue
		}
		state := ""
		if !entry.ContentIndexed {
			state = " — content not indexed"
		}
		fmt.Fprintf(&b, "- %s%s\n", entry.Name, state)
	}
	return b.String()
}

func formatRepositoryMap(item search.RepositoryMap) string {
	var decoded any
	if json.Unmarshal([]byte(item.SummaryJSON), &decoded) != nil {
		decoded = map[string]any{}
	}
	pretty, _ := json.MarshalIndent(decoded, "", "  ")
	text := fmt.Sprintf("## Repository Map\n\n- Library ID: %s\n- Ref: %s\n- Commit: %s\n\n```json\n%s\n```\n", item.LibraryID, item.Ref, item.CommitID, pretty)
	if len(item.Stack) > 0 {
		// The libraries a project already uses decide how new code in it should be
		// written, so they belong in the orientation rather than a separate call.
		text += fmt.Sprintf("\n### Stack (%d direct dependencies)\n\nWrite new code with these; do not introduce an alternative without checking.\n", item.StackTotal)
		for _, entry := range item.Stack {
			version := entry.Version
			if version == "" {
				version = "unspecified"
			}
			text += fmt.Sprintf("- `%s` %s (%s)\n", entry.Name, version, entry.Ecosystem)
		}
		if item.StackTotal > len(item.Stack) {
			text += fmt.Sprintf("- …%d more; find-dependency-usage lists them in full.\n", item.StackTotal-len(item.Stack))
		}
	}
	if len(item.Conventions) > 0 {
		text += "\n### Project conventions\n\nRead these before writing code for this repository; use read-file.\n"
		for _, path := range item.Conventions {
			text += fmt.Sprintf("- `%s`\n", path)
		}
	}
	return text
}

func formatSymbols(items []search.SymbolResult) string {
	if len(items) == 0 {
		return "No accessible symbols matched the query. Reindex the repository or broaden the symbol name."
	}
	var b strings.Builder
	b.WriteString("## Symbol Search Results\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s\n\n- Kind: %s\n- Language: %s\n- Library ID: %s/%s\n- Signature: `%s`\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n",
			item.QualifiedName, item.Kind, item.Language, item.LibraryID, item.Ref, item.Signature, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd)
		if item.Documentation != "" {
			fmt.Fprintf(&b, "- Documentation: %s\n", item.Documentation)
		}
	}
	return b.String()
}

func formatSymbolContext(item search.SymbolResult) string {
	return fmt.Sprintf("## %s\n\n- Kind: %s\n- Language: %s\n- Signature: `%s`\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n\n%s\n\n```%s\n%s\n```\n",
		item.QualifiedName, item.Kind, item.Language, item.Signature, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd, item.Documentation, item.Language, item.Content)
}

func formatDependencies(items []search.DependencyResult) string {
	if len(items) == 0 {
		return "No indexed dependencies matched the symbol or module."
	}
	var b strings.Builder
	b.WriteString("## Dependency Trace\n")
	for _, item := range items {
		from := item.FromSymbol
		if from == "" {
			from = item.FilePath
		}
		fmt.Fprintf(&b, "\n- `%s` --%s--> `%s`\n  Source: bitcontext://%s@%s/%s#L%d\n", from, item.Kind, item.Target, item.LibraryID, item.CommitID, item.FilePath, item.LineNumber)
	}
	return b.String()
}

func formatRefComparison(item search.RefComparison) string {
	if len(item.Changes) == 0 {
		return fmt.Sprintf("## Ref Comparison\n\nNo indexed symbol changes between `%s` and `%s` in %s.", item.BaseRef, item.HeadRef, item.LibraryID)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## Ref Comparison\n\n- Library ID: %s\n- Base: %s\n- Head: %s\n", item.LibraryID, item.BaseRef, item.HeadRef)
	for _, change := range item.Changes {
		fmt.Fprintf(&b, "\n- %s `%s` (%s) · %s\n", strings.ToUpper(change.Type), change.Name, change.Kind, change.FilePath)
		if change.BeforeSignature != "" {
			fmt.Fprintf(&b, "  Before: `%s`\n", change.BeforeSignature)
		}
		if change.AfterSignature != "" {
			fmt.Fprintf(&b, "  After: `%s`\n", change.AfterSignature)
		}
	}
	return b.String()
}

func formatChangeImpact(item search.ChangeImpact) string {
	var b strings.Builder
	b.WriteString(formatRefComparison(item.Comparison))
	b.WriteString("\n\n## Potentially Affected Dependencies\n")
	if len(item.Dependents) == 0 {
		b.WriteString("\nNo incoming indexed dependencies were found.")
		return b.String()
	}
	for _, dependency := range item.Dependents {
		from := dependency.FromSymbol
		if from == "" {
			from = dependency.FilePath
		}
		fmt.Fprintf(&b, "\n- `%s` depends on `%s` (%s)\n  Source: bitcontext://%s@%s/%s#L%d\n", from, dependency.Target, dependency.Kind, dependency.LibraryID, dependency.CommitID, dependency.FilePath, dependency.LineNumber)
	}
	return b.String()
}

func formatRunbooks(items []search.RunbookResult) string {
	if len(items) == 0 {
		return "No accessible runbooks matched the query."
	}
	var b strings.Builder
	b.WriteString("## Runbooks\n")
	for _, item := range items {
		fmt.Fprintf(&b, "\n### %s\n\n%s\n\nSource: bitcontext://%s@%s/%s#L%d-L%d\n", item.Heading, item.Content, item.LibraryID, item.CommitID, item.FilePath, item.LineStart, item.LineEnd)
	}
	return b.String()
}

func formatSearchExplanation(item search.SearchExplanation) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Search Explanation\n\n- Library ID: %s\n- Ref: %s\n- Retrieval: %s\n", item.LibraryID, item.Ref, item.RetrievalMode)
	if len(item.Hits) == 0 {
		b.WriteString("\nNo lexical candidates matched the normalized query.")
		return b.String()
	}
	for _, hit := range item.Hits {
		fmt.Fprintf(&b, "\n### %s\n\n- Matched Terms: %d\n- Keyword Occurrences: %d\n- Reasons: %s\n- Embedding: %s / %s / %s\n- Source: bitcontext://%s@%s/%s#L%d-L%d\n",
			hit.Heading, hit.MatchedTerms, hit.KeywordOccurrences, strings.Join(hit.Reasons, "; "), hit.EmbeddingProvider, hit.EmbeddingModel, hit.EmbeddingRevision, item.LibraryID, hit.CommitID, hit.FilePath, hit.LineStart, hit.LineEnd)
	}
	return b.String()
}
