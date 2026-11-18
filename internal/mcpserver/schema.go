package mcpserver

import (
	"context"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
	"controle-gastos/internal/metrics"
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
	name := t.Name
	mcp.AddTool(s, t, func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		res, out, err := h(ctx, req, in)
		metrics.ObserveTool(name, toolOutcome(res, err), time.Since(start))
		return res, out, err
	})
}

// toolOutcome classifies a call for metrics: "invalid" is a validation error
// the model can correct; "error" is anything else (internal failures).
func toolOutcome(res *mcp.CallToolResult, err error) string {
	switch {
	case err == nil && (res == nil || !res.IsError):
		return "ok"
	case app.IsValidation(err):
		return "invalid"
	}
	return "error"
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
