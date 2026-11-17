package mcpserver

import (
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// addTool registers a tool like mcp.AddTool, but with an output schema that
// tolerates fields it doesn't list. Clients cache a tool's output schema for
// the whole conversation and validate every result against it; the inferred
// schemas forbid extra properties, so adding an output field (e.g.
// created_by) broke clients connected before the deploy. Input schemas stay
// strict.
func addTool[In, Out any](s *mcp.Server, t *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	if t.OutputSchema == nil {
		sc, err := jsonschema.For[Out](nil)
		if err != nil {
			panic("mcp output schema for " + t.Name + ": " + err.Error())
		}
		openSchema(sc)
		t.OutputSchema = sc
	}
	mcp.AddTool(s, t, h)
}

// openSchema lifts additionalProperties: false everywhere in sc.
func openSchema(sc *jsonschema.Schema) {
	if sc == nil {
		return
	}
	if sc.AdditionalProperties != nil && sc.AdditionalProperties.Not != nil {
		sc.AdditionalProperties = nil // the inferred "false" schema is {"not": {}}
	}
	for _, p := range sc.Properties {
		openSchema(p)
	}
	for _, d := range sc.Defs {
		openSchema(d)
	}
	openSchema(sc.Items)
	for _, x := range sc.AnyOf {
		openSchema(x)
	}
	for _, x := range sc.OneOf {
		openSchema(x)
	}
}
