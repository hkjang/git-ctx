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
