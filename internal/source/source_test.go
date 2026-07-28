package source

import "testing"

func TestCanonicalSourceAwareLibraryID(t *testing.T) {
	cases := map[string]string{
		LibraryID("bitbucket", "KCB", "Dify Service"): "/kcb/dify-service",
		LibraryID("gitlab", "AI/Apps", "Dify"):        "/gitlab~ai-apps/dify",
		LibraryID("confluence", "OPS", "Runbooks"):    "/confluence~ops/runbooks",
	}
	for got, want := range cases {
		if got != want {
			t.Fatalf("LibraryID=%q want=%q", got, want)
		}
	}
}
