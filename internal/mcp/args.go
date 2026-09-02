package mcp

import (
	"math"
	"net"
	"strconv"
	"strings"

	"git-ctx/internal/auth"
	"git-ctx/internal/search"
	"net/http"
)

// Argument decoding and the small helpers shared across tools.
//
// An argument arrives as whatever JSON the client produced, and a client that
// quotes its numbers is common enough to plan for: gateways that form-encode
// every value, and models that write "limit": "5" because the tool call they
// are imitating quoted it. A value of the wrong JSON type used to be dropped
// in silence and the default used in its place, so read-file answered a request
// for lines 400 to 460 with the whole file, cut at the budget, and said nothing
// about the range it had ignored. A value that spells its declared type is now
// read as that type, and argumentTypeNote tells the caller which happened.

func stringArg(m map[string]any, k string) string {
	switch value := m[k].(type) {
	case string:
		return value
	case float64:
		return formatNumberArg(value)
	}
	return ""
}
func stringSliceArg(m map[string]any, k string) []string {
	switch value := m[k].(type) {
	case []any:
		out := make([]string, 0, len(value))
		for _, item := range value {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	case string:
		return splitListArg(value)
	}
	return nil
}
func intArg(m map[string]any, k string, fallback int) int {
	switch value := m[k].(type) {
	case float64:
		if whole, ok := wholeNumber(value); ok {
			return whole
		}
	case string:
		if whole, ok := parseWholeNumber(value); ok {
			return whole
		}
	}
	return fallback
}

// maxNumberArg bounds a numeric argument to the range every argument of this
// catalog lives in -- result limits, line numbers and byte budgets. It also
// keeps the conversion defined: Go leaves a float-to-int conversion outside the
// integer range to the implementation.
const maxNumberArg = 1 << 31

// wholeNumber is the integer a JSON number names, when it names one.
func wholeNumber(value float64) (int, bool) {
	if value != math.Trunc(value) || math.Abs(value) >= maxNumberArg {
		return 0, false
	}
	return int(value), true
}

// parseWholeNumber reads the integer a quoted number spells. "5.0" counts:
// a client that writes its numbers as text tends to write them as it received
// them, and a trailing zero is not a different value.
func parseWholeNumber(value string) (int, bool) {
	text := strings.TrimSpace(value)
	if text == "" {
		return 0, false
	}
	if number, err := strconv.Atoi(text); err == nil {
		return wholeNumber(float64(number))
	}
	if number, err := strconv.ParseFloat(text, 64); err == nil {
		return wholeNumber(number)
	}
	return 0, false
}

// formatNumberArg writes a JSON number the way the caller typed it, so a
// numeric query or ref reaches the search as its own text rather than as "".
func formatNumberArg(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// splitListArg reads a list sent as one string. A client that cannot express an
// array sends either the single element or the elements joined by commas;
// neither a library ID nor a ref contains one.
func splitListArg(value string) []string {
	out := make([]string, 0, 4)
	for _, item := range strings.Split(value, ",") {
		if text := strings.TrimSpace(item); text != "" {
			out = append(out, text)
		}
	}
	return out
}
func baseLibraryID(id string) string {
	parts := strings.Split(strings.TrimPrefix(strings.ToLower(strings.TrimSpace(id)), "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	return "/" + parts[0] + "/" + parts[1]
}
func truncate(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return runeSafeCut(value, limit) + "…"
}

// clientIP is the address to record against a call. The HTTP layer has already
// decided whether a forwarding header may be believed, so that answer is used
// as it stands; trusting X-Forwarded-For here would let any caller write its
// own address into the audit trail. The fallback strips the ephemeral port,
// which is noise in an audit row and makes the value unmatchable against the
// CIDR restrictions the same address is checked against.
func clientIP(r *http.Request) string {
	if ip := auth.ClientIP(r.Context()); ip != "" {
		return ip
	}
	address := r.RemoteAddr
	if host, _, err := net.SplitHostPort(address); err == nil {
		address = host
	}
	return strings.Trim(address, "[]")
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func libraryAllowed(libraryID string, allowed []string) bool {
	return search.LibraryAllowed(libraryID, allowed)
}

func filterLibraries(items []search.Library, allowed []string) []search.Library {
	out := items[:0]
	for _, item := range items {
		if libraryAllowed(item.ID, allowed) {
			out = append(out, item)
		}
	}
	return out
}

// principalACLs resolves the principals used for every MCP search. Platform,
// source and search administrators receive the catalog-wide principal so their
// tools work without a Bitbucket or GitLab account.
func principalACLs(p auth.Principal) []string {
	var principals []string
	switch {
	case len(p.ACLPrincipals) > 0:
		principals = p.ACLPrincipals
	case p.ACLPrincipal != "":
		principals = []string{p.ACLPrincipal}
	}
	return search.WithUnrestricted(principals, p.Roles)
}
