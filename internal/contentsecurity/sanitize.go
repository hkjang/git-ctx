package contentsecurity

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"regexp"
	"strings"
)

var privateKeyRE = regexp.MustCompile(`(?i)-----BEGIN(?: [A-Z0-9]+)* PRIVATE KEY-----`)

// Naming the field is the only signal available for values the entropy rule
// cannot reach -- notably hex, whose entropy tops out at 4.0 and so never
// clears the 4.2 gate. The bare "token" and "secret" alternatives catch names
// like MY_TOKEN, and trail the compound forms because a bare "secret" cannot
// span the underscore in SECRET_KEY.
var secretAssignmentRE = regexp.MustCompile(`(?i)(api[_-]?key|secret[_-]?key|client[_-]?secret|access[_-]?token|auth[_-]?token|refresh[_-]?token|private[_-]?token|password|passwd|passphrase|credential|token|secret)\s*[:=]\s*(?:"[^"\r\n]{4,}"|'[^'\r\n]{4,}'|[^\s,;#]{4,})`)
var awsKeyRE = regexp.MustCompile(`\bAKIA[A-Z0-9]{16}\b`)

// Vendor prefixes are matched explicitly rather than left to the entropy rule,
// which mislabels them and misses the segments shorter than 32 characters.
var knownTokenRE = regexp.MustCompile(`\b(?:glpat-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{20,}|hvs\.[A-Za-z0-9_-]{20,}|xox[baprs]-[A-Za-z0-9-]{10,}|[sr]k_(?:live|test)_[A-Za-z0-9]{16,}|sk-(?:proj-)?[A-Za-z0-9_-]{20,}|AIza[A-Za-z0-9_-]{35}|SG\.[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]{16,}|npm_[A-Za-z0-9]{36}|eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,})\b`)

// A credential in a URL is not a database problem, and listing schemes made it
// look like one. Three were listed -- postgres, mysql, mariadb -- so
// mongodb://, redis://, amqp://, sqlserver:// and, above all,
// https://user:token@host went through untouched. A clone URL carrying an
// access token is the single most common way a credential appears in the
// repositories this platform indexes: it sits in READMEs, CI files and setup
// notes. Any scheme counts now.
//
// Only the userinfo is replaced. The host is what makes the finding
// actionable -- "a token for bitbucket.company is in this file" -- and hiding
// it would take the useful half of the line with the dangerous half.
// The password half deliberately admits "@": P@ssw0rd is a password people
// actually choose, and stopping at the first "@" left "ssw0rd" in the answer.
// It still cannot cross a space or a slash, so it stays inside one URL.
var credentialURLRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^\s/:@"'<>]+:[^\s/"'<>]+@`)

// Oracle's thin driver puts the credentials before the @ with a slash rather
// than a colon, so no URL rule reaches it. It is too common in the
// installations this platform is built for to leave to the entropy rule, which
// never sees a password like "tiger" at all.
var oracleDSNRE = regexp.MustCompile(`(?i)\b(jdbc:oracle:[a-z]+:)[^\s/@"']+/[^\s@"']+@`)

// Java and Spring configuration states a credential as an element or a pair of
// attributes, neither of which is an assignment. <password>value</password> and
// <property name="password" value="value"/> were both returned in full.
var xmlSecretElementRE = regexp.MustCompile(`(?i)<(password|passwd|secret|token|api[_-]?key|client[_-]?secret|credential)\s*>([^<\r\n]{4,})</`)
var xmlSecretAttributeRE = regexp.MustCompile(`(?i)(name\s*=\s*"[^"\r\n]*(?:password|passwd|secret|token|credential)[^"\r\n]*"\s+value\s*=\s*")[^"\r\n]{4,}"`)

// A command line is where a credential is most often written down for someone
// else to copy, and curl's -u takes it as user:password. The curl anchor keeps
// this away from every other tool that happens to have a -u flag.
var curlUserRE = regexp.MustCompile(`(?i)\bcurl\b[^\n]{0,200}?(\s-u\s+|\s--user\s+)[^\s:"']+:[^\s"']+`)

// .netrc and its imitators separate the value with spaces rather than a colon
// or an equals sign, which no assignment rule reaches. Requiring the login
// field before it keeps the rule off prose that merely says "password".
var netrcRE = regexp.MustCompile(`(?i)\blogin\s+\S+\s+password\s+\S{4,}`)

// An Authorization header carries a credential whose shape is the issuer's
// business, so no vendor prefix and no entropy floor will find it.
var authorizationHeaderRE = regexp.MustCompile(`(?i)\b(authorization\s*[:=]\s*)(bearer|basic|token)\s+[^\s"'<>]{8,}`)
var entropyCandidateRE = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
var commonHashRE = regexp.MustCompile(`(?i)^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

// Revision fingerprints the masking rules themselves.
//
// A stored chunk was masked by the rules in force when it was indexed, so
// improving the rules does nothing for content already in the database — the
// credentials a new rule catches stay readable until that ref is read again.
// The indexer already re-reads a ref whose policy fingerprint moved; this makes
// a rule change the same kind of event.
//
// It is derived from the patterns rather than written down, because a constant
// somebody has to remember to bump is a constant that will be forgotten, and
// the failure is silent.
func Revision() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		privateKeyRE.String(), secretAssignmentRE.String(), awsKeyRE.String(), knownTokenRE.String(),
		credentialURLRE.String(), oracleDSNRE.String(), xmlSecretElementRE.String(), xmlSecretAttributeRE.String(),
		curlUserRE.String(), netrcRE.String(), authorizationHeaderRE.String(), entropyCandidateRE.String(),
		commonHashRE.String(),
	}, "\x00")))
	return hex.EncodeToString(sum[:6])
}

// Sanitize masks secrets in untrusted repository content before indexing or
// returning a snippet received directly from a remote source search API.
func Sanitize(content string) (string, string) {
	if privateKeyRE.MatchString(content) {
		return "", "private_key"
	}
	finding := ""
	masked := secretAssignmentRE.ReplaceAllStringFunc(content, func(value string) string {
		finding = "credential_assignment"
		at := strings.IndexAny(value, ":=")
		if at < 0 {
			return "[REDACTED]"
		}
		return value[:at+1] + " [REDACTED]"
	})
	masked = awsKeyRE.ReplaceAllStringFunc(masked, func(string) string { finding = "cloud_access_key"; return "[REDACTED]" })
	masked = knownTokenRE.ReplaceAllStringFunc(masked, func(string) string { finding = "known_token"; return "[REDACTED]" })
	masked = credentialURLRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_dsn"
		return credentialURLRE.ReplaceAllString(value, "${1}[REDACTED]@")
	})
	masked = oracleDSNRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_dsn"
		return oracleDSNRE.ReplaceAllString(value, "${1}[REDACTED]@")
	})
	masked = xmlSecretElementRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_assignment"
		return xmlSecretElementRE.ReplaceAllString(value, "<${1}>[REDACTED]</")
	})
	masked = xmlSecretAttributeRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_assignment"
		return xmlSecretAttributeRE.ReplaceAllString(value, `${1}[REDACTED]"`)
	})
	masked = curlUserRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_assignment"
		return curlUserRE.ReplaceAllString(value, "${1}[REDACTED]")
	})
	masked = netrcRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_assignment"
		return netrcRE.ReplaceAllString(value, "login [REDACTED] password [REDACTED]")
	})
	masked = authorizationHeaderRE.ReplaceAllStringFunc(masked, func(value string) string {
		finding = "credential_assignment"
		return authorizationHeaderRE.ReplaceAllString(value, "${1}${2} [REDACTED]")
	})
	masked = entropyCandidateRE.ReplaceAllStringFunc(masked, func(value string) string {
		if commonHashRE.MatchString(value) || shannonEntropy(value) < 4.2 {
			return value
		}
		finding = "high_entropy_secret"
		return "[REDACTED_HIGH_ENTROPY]"
	})
	return masked, finding
}

func shannonEntropy(value string) float64 {
	if value == "" {
		return 0
	}
	counts := map[rune]int{}
	for _, char := range value {
		counts[char]++
	}
	length := float64(len([]rune(value)))
	var entropy float64
	for _, count := range counts {
		probability := float64(count) / length
		entropy -= probability * math.Log2(probability)
	}
	return entropy
}
