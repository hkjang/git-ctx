package contentsecurity

import (
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
var credentialDSNRE = regexp.MustCompile(`(?i)\b(?:postgres(?:ql)?|mysql|mariadb)://[^/\s:@]+:[^@\s/]+@[^\s"'<>]+`)
var entropyCandidateRE = regexp.MustCompile(`[A-Za-z0-9+/=_-]{32,}`)
var commonHashRE = regexp.MustCompile(`(?i)^(?:[a-f0-9]{40}|[a-f0-9]{64})$`)

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
	masked = credentialDSNRE.ReplaceAllStringFunc(masked, func(string) string { finding = "credential_dsn"; return "[REDACTED_DSN]" })
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
