package mcp

// The tool catalog served to clients, derived from the registry.

// Catalog is the tool list advertised to MCP clients. Every tool accepts
// maxBytes, so an agent that only wants a short answer can say so instead of
// spending a full page of its context window on one call.
func Catalog() []map[string]any {
	tools := make([]map[string]any, 0, len(registry))
	for i := range registry {
		tools = append(tools, describeTool(&registry[i]))
	}
	return tools
}

// describeTool renders one registry entry the way a client sees it, including
// the per-call maxBytes budget every tool accepts.
func describeTool(entry *tool) map[string]any {
	schema := cloneSchema(entry.schema)
	if properties, ok := schema["properties"].(map[string]any); ok {
		// Measured across the catalogue, this one property was 24.9% of every
		// tools/list response: the same sentence repeated on all thirty tools.
		// initialize already explains what it does and is sent once per session,
		// so the schema only has to name it.
		properties["maxBytes"] = map[string]any{
			"type": "integer", "minimum": MinResponseBytes, "maximum": MaxResponseBytes,
			"description": "Smaller response budget for this call; see initialize.",
		}
	}
	return map[string]any{"name": entry.name, "description": entry.description, "inputSchema": schema}
}

// cloneSchema copies a registry schema so Catalog can add the per-call maxBytes
// property to the served copy without mutating the shared definition.
func cloneSchema(schema map[string]any) map[string]any {
	out := make(map[string]any, len(schema))
	for key, value := range schema {
		if key == "properties" {
			if properties, ok := value.(map[string]any); ok {
				copied := make(map[string]any, len(properties)+1)
				for name, property := range properties {
					copied[name] = property
				}
				out[key] = copied
				continue
			}
		}
		out[key] = value
	}
	return out
}
