package mcp

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// The tool schema is how an agent learns to call this platform. A parameter
// with no description is a parameter it has to guess at, and forty-two of them
// had none — every argument of trace-dependencies, compare-refs,
// get-change-impact, find-runbook and explain-search-result among them. The
// gaps were not deliberate; they accumulated, which is what this test is for.
func TestEveryToolParameterIsDescribed(t *testing.T) {
	var problems []string
	for _, entry := range registry {
		schema, ok := entry.schema["properties"].(map[string]any)
		if !ok {
			if entry.schema["properties"] != nil {
				problems = append(problems, fmt.Sprintf("%s: properties is not an object", entry.name))
			}
			continue
		}
		for name, raw := range schema {
			described := false
			switch property := raw.(type) {
			case map[string]any:
				described = describedText(property["description"])
				if property["type"] == "array" && property["items"] == nil {
					problems = append(problems, fmt.Sprintf("%s.%s: an array without items", entry.name, name))
				}
				if enum, present := property["enum"]; present {
					if values, isSlice := enum.([]string); !isSlice || len(values) == 0 {
						problems = append(problems, fmt.Sprintf("%s.%s: an empty enum", entry.name, name))
					}
				}
			case map[string]string:
				described = property["description"] != ""
			default:
				problems = append(problems, fmt.Sprintf("%s.%s: not an object", entry.name, name))
				continue
			}
			if !described {
				problems = append(problems, fmt.Sprintf("%s.%s: no description", entry.name, name))
			}
		}
		if entry.description == "" {
			problems = append(problems, entry.name+": the tool itself has no description")
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%d tool parameters tell an agent nothing about themselves:\n  %s",
			len(problems), joinLines(problems))
	}
}

// Every name in required has to be a property, or a client validating the
// schema rejects every call before it is made.
func TestEveryRequiredParameterExists(t *testing.T) {
	var problems []string
	for _, entry := range registry {
		required, _ := entry.schema["required"].([]string)
		properties, _ := entry.schema["properties"].(map[string]any)
		for _, name := range required {
			if _, present := properties[name]; !present {
				problems = append(problems, fmt.Sprintf("%s: %q is required and is not a property", entry.name, name))
			}
		}
		if entry.schema["type"] != "object" {
			problems = append(problems, fmt.Sprintf("%s: schema type is %v, not object", entry.name, entry.schema["type"]))
		}
		if additional, present := entry.schema["additionalProperties"]; present {
			if _, isBool := additional.(bool); !isBool {
				problems = append(problems, fmt.Sprintf("%s: additionalProperties is not a boolean", entry.name))
			}
		}
	}
	if len(problems) > 0 {
		t.Fatalf("%d tool schemas would be rejected by a client that validates them:\n  %s",
			len(problems), joinLines(problems))
	}
}

func describedText(value any) bool {
	text, ok := value.(string)
	return ok && text != ""
}

func joinLines(values []string) string {
	out := ""
	for index, value := range values {
		if index > 0 {
			out += "\n  "
		}
		out += value
	}
	return out
}

// serverInstructions is the map an agent is handed before it asks anything, and
// a tool it does not name is a tool that will not be reached for. Thirteen of
// the twenty-nine were missing, including resolve-library-id — the one that
// produces the library id every other tool asks for, referred to throughout the
// document and never explained.
//
// Administrative tools are deliberately absent: they are answered only for a
// caller holding an API key, and an agent without one cannot use them.
func TestTheInstructionsNameEveryToolAnAgentCanCall(t *testing.T) {
	var missing []string
	for _, entry := range registry {
		if len(entry.adminRoles) > 0 {
			continue
		}
		if !strings.Contains(serverInstructions, entry.name) {
			missing = append(missing, entry.name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d tools an agent can call are not named in the instructions it is given:\n  %s",
			len(missing), joinLines(missing))
	}
}

// The reverse: a name in the prose that is not a tool sends an agent to
// something that does not exist.
func TestTheInstructionsNameNoToolThatIsGone(t *testing.T) {
	known := make(map[string]bool, len(registry))
	for _, entry := range registry {
		known[entry.name] = true
	}
	shaped := regexp.MustCompile(`\b(search|find|get|read|list|query|build|trace|compare|assess|resolve|export|reindex|explain)(-[a-z]+){1,3}\b`)
	var strangers []string
	for _, match := range shaped.FindAllString(serverInstructions, -1) {
		if !known[match] {
			strangers = append(strangers, match)
		}
	}
	if len(strangers) > 0 {
		t.Fatalf("the instructions name %d things that are not tools: %s", len(strangers), joinLines(strangers))
	}
}
