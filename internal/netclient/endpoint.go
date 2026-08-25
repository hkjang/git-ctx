package netclient

import "strings"

// JoinAPIPath appends an API path to a configured base URL without repeating a
// segment the operator already typed.
//
// Every OpenAI-compatible provider documents its base URL as ending in /v1, so
// that is what an operator pastes into the settings field. Appending
// "/v1/embeddings" to it produced ".../v1/v1/embeddings", which answers 404 and
// reaches the operations screen as "the embedding endpoint is unavailable" —
// indistinguishable from an outage, and the one thing that would have explained
// it, the doubled path, was never shown. Both forms are now accepted, including
// a base URL that already names the resource itself.
func JoinAPIPath(base, path string) string {
	trimmed := strings.TrimSuffix(strings.TrimSpace(base), "/")
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for index := range segments {
		if strings.HasSuffix(trimmed, "/"+strings.Join(segments[:index+1], "/")) {
			rest := segments[index+1:]
			if len(rest) == 0 {
				return trimmed
			}
			return trimmed + "/" + strings.Join(rest, "/")
		}
	}
	return trimmed + "/" + strings.Join(segments, "/")
}
