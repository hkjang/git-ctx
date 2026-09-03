package contentsecurity

import (
	"regexp"
	"strings"
	"testing"
)

// The masking rule for credentials in a URL named three schemes — postgres,
// mysql, mariadb — which made a credential in a URL look like a database
// problem. It is not. A clone URL carrying an access token is the single most
// common way a credential appears in the repositories this platform indexes:
// it sits in READMEs, CI files and setup notes, and
// https://x-token-auth:ATBB...@bitbucket.company/scm/kcb/app.git went through
// untouched, into the index and back out in a snippet.
//
// The shapes below are the ones an on-premises installation actually holds.
// Each was checked against the sanitizer before the rules were written, and
// each was returned in full.
func TestCredentialShapesAnInstallationActuallyHolds(t *testing.T) {
	for _, c := range []struct {
		name, input string
		// leaked is the substring that must not survive; kept is what should,
		// because a finding nobody can act on is worth little.
		leaked, kept string
	}{
		{
			name:   "a clone URL with an access token",
			input:  `git clone https://x-token-auth:ATBBxyz123456789abc@bitbucket.company/scm/kcb/app.git`,
			leaked: "ATBBxyz123456789abc", kept: "bitbucket.company",
		},
		{
			name:   "a CI URL with a password",
			input:  `https://ci-user:hunter2@gitlab.company/group/project.git`,
			leaked: "hunter2", kept: "gitlab.company",
		},
		{
			// The password people actually choose contains the delimiter, and
			// stopping at the first "@" left most of it in the answer.
			name:   "a password containing an at sign",
			input:  `sqlserver://sa:P@ssw0rd1@db.company:1433/main`,
			leaked: "ssw0rd1", kept: "db.company",
		},
		{
			name:   "a scheme outside the three that were listed",
			input:  `mongodb://admin:letmein@mongo.company:27017/app`,
			leaked: "letmein", kept: "mongo.company",
		},
		{
			name:   "an Oracle thin descriptor, whose separator is a slash",
			input:  `jdbc:oracle:thin:scott/tiger@//db.company:1521/orcl`,
			leaked: "tiger", kept: "db.company",
		},
		{
			name:   "a Spring XML property pair",
			input:  `<property name="password" value="Sup3rSecret" />`,
			leaked: "Sup3rSecret", kept: "password",
		},
		{
			name:   "an XML element",
			input:  `<password>Sup3rSecret</password>`,
			leaked: "Sup3rSecret", kept: "password",
		},
		{
			name:   "an Authorization header",
			input:  `Authorization: Bearer abcdefghijklmnop1234`,
			leaked: "abcdefghijklmnop1234", kept: "Bearer",
		},
		{
			name:   "a curl command in a README",
			input:  `curl -u admin:Passw0rd https://api.company/v1/health`,
			leaked: "Passw0rd", kept: "api.company",
		},
		{
			name:   "a netrc entry, which separates with spaces",
			input:  `machine bitbucket.company login svc-ci password s3cr3tvalue`,
			leaked: "s3cr3tvalue", kept: "bitbucket.company",
		},
		{
			// The closing quote of the name sat between it and the colon, so the
			// assignment rule never matched a single JSON config file.
			name:   "a JSON configuration file, whose key is quoted",
			input:  `{"database": {"user": "app", "password": "hunter22"}}`,
			leaked: "hunter22", kept: `"password"`,
		},
		{
			name:   "a quoted key with spaces around the colon",
			input:  `  "client_secret" : "s3cr3tvalue",`,
			leaked: "s3cr3tvalue", kept: `"client_secret"`,
		},
		{
			name:   "a Terraform variable, whose quoted name is assigned with =",
			input:  `"api_key" = "abcd1234efgh"`,
			leaked: "abcd1234efgh", kept: `"api_key"`,
		},
		{
			// soapUI, WSDL and WebSphere descriptors all prefix the element, and
			// requiring the name to follow "<" directly missed every one of them.
			name:   "a namespaced XML element",
			input:  `<con:password>Sup3rSecret</con:password>`,
			leaked: "Sup3rSecret", kept: `</con:password>`,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			masked, finding := Sanitize(c.input)
			if strings.Contains(masked, c.leaked) {
				t.Errorf("the credential survived: %s", masked)
			}
			if finding == "" {
				t.Errorf("nothing was recorded as found for %q", c.input)
			}
			if c.kept != "" && !strings.Contains(masked, c.kept) {
				t.Errorf("the finding lost the context that makes it actionable (%q): %s", c.kept, masked)
			}
		})
	}
}

// Masking content nobody asked to hide is its own failure: it removes text from
// search results and from the files an agent reads. These are the shapes the
// new rules sit closest to.
func TestOrdinaryContentIsLeftAlone(t *testing.T) {
	for _, input := range []string{
		`https://bitbucket.company:7990/scm/kcb/app.git`,
		`see http://docs.company/guide#section for the password policy`,
		`connect to postgres://pg.company:5432/app with the operator account`,
		`ratio := completed / total`,
		`<property name="timeout" value="30" />`,
		`user@example.com is the contact for this service`,
		`git@bitbucket.company:kcb/app.git`,
		`curl https://api.company/v1/health`,
		// A quoted name is only a credential when something is assigned to it.
		`fields["password"] = lookup(name)`,
		`"password" is the field the login form posts`,
		`the "token" column is never null`,
	} {
		if masked, finding := Sanitize(input); masked != input {
			t.Errorf("ordinary content was masked as %q:\n  in:  %s\n  out: %s", finding, input, masked)
		}
	}
}

// The rules decide what a stored chunk contains, so improving them has to be a
// reason to read a ref again. Without this the credentials a new rule catches
// stay readable in every chunk indexed before it, and nothing says so.
func TestTheMaskingRevisionTracksTheRules(t *testing.T) {
	before := Revision()
	if before == "" || len(before) < 8 {
		t.Fatalf("the masking revision is not a fingerprint: %q", before)
	}
	if Revision() != before {
		t.Error("the masking revision is not stable across calls, so every ref would re-index on every run")
	}
	// A rule added or edited must move it. Swapping one pattern for another
	// stands in for that edit.
	original := netrcRE.String()
	defer func() { netrcRE = regexp.MustCompile(original) }()
	netrcRE = regexp.MustCompile(original + `x?`)
	if Revision() == before {
		t.Error("a changed rule left the masking revision alone, so no ref would be read again")
	}
}
