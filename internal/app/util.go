package app

import (
	"strings"
	"unicode/utf8"
)

// Small conversions shared across handlers.

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// truncateText cuts on a rune boundary; a byte cut would corrupt Korean text,
// which is most of what this platform stores.
func truncateText(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(value[cut]) {
		cut--
	}
	return value[:cut]
}
func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
func stringContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func stringArrayValue(value any) []string {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...)
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, strings.TrimSpace(text))
			}
		}
		return out
	case string:
		return splitCSV(typed)
	default:
		return nil
	}
}
func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	return strings.Split(value, ",")
}
