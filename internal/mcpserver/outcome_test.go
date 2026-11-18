package mcpserver

import (
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"controle-gastos/internal/app"
)

func TestToolOutcome(t *testing.T) {
	cases := []struct {
		res  *mcp.CallToolResult
		err  error
		want string
	}{
		{nil, nil, "ok"},
		{&mcp.CallToolResult{}, nil, "ok"},
		{&mcp.CallToolResult{IsError: true}, nil, "error"},
		{nil, &app.ValidationError{Msg: "data inválida"}, "invalid"},
		{nil, errInternal, "error"},
		{nil, errors.New("x"), "error"},
	}
	for i, c := range cases {
		if got := toolOutcome(c.res, c.err); got != c.want {
			t.Errorf("caso %d: %q, queria %q", i, got, c.want)
		}
	}
}
