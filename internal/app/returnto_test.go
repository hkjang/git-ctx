package app

import "testing"

// Requiring a leading "/" and rejecting "//" is not enough: browsers fold a
// backslash into a slash for http and https URLs, so "/\evil.example" resolves
// to //evil.example and takes the user off-site right after they authenticated.
func TestSafeReturnToKeepsRedirectsOnSite(t *testing.T) {
	offSite := []string{
		`//evil.example`,
		`/\evil.example`,
		`/\/evil.example`,
		`/\\evil.example`,
		`https://evil.example`,
		`http://evil.example/path`,
		`javascript:alert(1)`,
		`evil.example`,
		``,
		"/admin\r\nSet-Cookie: x=1",
		"/admin\x00",
	}
	for _, raw := range offSite {
		if got := safeReturnTo(raw); got != "/" {
			t.Errorf("safeReturnTo(%q) = %q, want %q", raw, got, "/")
		}
	}

	onSite := []string{
		`/`,
		`/admin`,
		`/admin/settings?tab=sources`,
		`/admin#anchor`,
		`/a%20b`,
	}
	for _, raw := range onSite {
		if got := safeReturnTo(raw); got != raw {
			t.Errorf("safeReturnTo(%q) = %q, want it preserved", raw, got)
		}
	}
}
