package contentsecurity

import (
	"strings"
	"testing"
)

func TestSanitizeMasksPunctuationRichCredentialAssignments(t *testing.T) {
	input := strings.Join([]string{
		"password = Sup3r$ecret!",
		`client_secret: "p@ss:w0rd/with?symbols"`,
		"refresh-token='abc$123!xyz'",
	}, "\n")
	safe, finding := Sanitize(input)
	if finding != "credential_assignment" {
		t.Fatalf("finding=%q", finding)
	}
	for _, secret := range []string{"Sup3r$ecret!", "p@ss:w0rd/with?symbols", "abc$123!xyz"} {
		if strings.Contains(safe, secret) {
			t.Fatalf("secret %q remained in %q", secret, safe)
		}
	}
	if count := strings.Count(safe, "[REDACTED]"); count != 3 {
		t.Fatalf("redactions=%d content=%q", count, safe)
	}
}

func TestSanitizeBlocksAllCommonPrivateKeyPEMHeaders(t *testing.T) {
	for _, header := range []string{
		"-----BEGIN PRIVATE KEY-----",
		"-----BEGIN RSA PRIVATE KEY-----",
		"-----BEGIN EC PRIVATE KEY-----",
		"-----BEGIN DSA PRIVATE KEY-----",
		"-----BEGIN OPENSSH PRIVATE KEY-----",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----",
	} {
		if safe, finding := Sanitize(header + "\nsecret material"); safe != "" || finding != "private_key" {
			t.Errorf("header=%q safe=%q finding=%q", header, safe, finding)
		}
	}
}

func TestSanitizeMasksPrefixedEnvironmentCredentialAssignments(t *testing.T) {
	input := strings.Join([]string{
		"DB_PASSWORD=short!pass",
		"MY_API_KEY=key-1234",
		"GITLAB_PRIVATE_TOKEN=token!5678",
		"AWS_SECRET_KEY=secret/9012",
	}, "\n")
	safe, finding := Sanitize(input)
	if finding != "credential_assignment" {
		t.Fatalf("finding=%q content=%q", finding, safe)
	}
	for _, secret := range []string{"short!pass", "key-1234", "token!5678", "secret/9012"} {
		if strings.Contains(safe, secret) {
			t.Fatalf("prefixed secret %q remained in %q", secret, safe)
		}
	}
	if count := strings.Count(safe, "[REDACTED]"); count != 4 {
		t.Fatalf("redactions=%d content=%q", count, safe)
	}
}

// Hex tops out at 4.0 bits of entropy, below the 4.2 gate, so a hex secret can
// only ever be caught by the name it is assigned to.
func TestSanitizeMasksHexSecretsTheEntropyRuleCannotReach(t *testing.T) {
	for _, line := range []string{
		"MY_TOKEN = 5f4dcc3b5aa765d61d8327deb882cf99aa11",
		`APP_SECRET: "8f14e45fceea167a5a36dedd4bea2543"`,
		"DB_CREDENTIAL=1b6453892473a467d07372d45eb05abc2031647a",
	} {
		safe, finding := Sanitize(line)
		if finding != "credential_assignment" {
			t.Errorf("line=%q finding=%q", line, finding)
		}
		if !strings.Contains(safe, "[REDACTED]") {
			t.Errorf("line=%q was not redacted: %q", line, safe)
		}
	}
}

func TestSanitizeMasksWellKnownVendorTokens(t *testing.T) {
	// Assembled at run time rather than written out. These are shaped like the
	// real thing on purpose -- that is what the patterns match -- and a literal
	// of that shape is rejected by push protection even when, as here, every
	// value is invented. Joining the parts keeps the fixture honest without
	// putting a scannable string in the tree.
	body := strings.Repeat("AbCdEfGh", 3)
	for name, token := range map[string]string{
		"slack":    join("xoxb", "-123456789012-1234567890123-"+body),
		"stripe":   join("sk", "_live_"+strings.Repeat("St1p3", 5)),
		"openai":   join("sk", "-proj-"+body+"0123456789"),
		"google":   join("AIza", "Sy"+strings.Repeat("G0og1e", 5)+"abc"), // the pattern wants exactly 35 characters
		"sendgrid": join("SG", "."+strings.Repeat("Send", 5)+"."+strings.Repeat("Gr1d", 11)),
	} {
		safe, finding := Sanitize("value " + token)
		if finding != "known_token" {
			t.Errorf("%s: finding=%q", name, finding)
		}
		if strings.Contains(safe, token) {
			t.Errorf("%s token survived: %q", name, safe)
		}
	}
}

// Widening the keyword list must not shred ordinary source, which would degrade
// every indexed snippet.
func TestSanitizeLeavesOrdinaryCodeIntact(t *testing.T) {
	for _, line := range []string{
		"func refreshToken(ctx context.Context) error {",
		"const tokenTTL = 15 * time.Minute",
		"SELECT token FROM sessions WHERE id = ?",
		"// The token is refreshed automatically before it expires.",
		`sha256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"`,
		"commit da39a3ee5e6b4b0d3255bfef95601890afd80709",
	} {
		if safe, finding := Sanitize(line); safe != line {
			t.Errorf("benign line altered (finding=%q):\n  in:  %s\n  out: %s", finding, line, safe)
		}
	}
}

// join builds a token from a vendor prefix and a body so the full string never
// appears as a literal in the source tree.
func join(prefix, body string) string { return prefix + body }
