# controle-gastos

Personal finance web app (Go stdlib + SQLite) that is also an MCP server, so Claude can read and
update the same data the UI shows. UI text, validation messages and tool descriptions are pt-BR.

## Architecture rules

- `internal/app` (`app.Service`) is the app's only API. Every business rule and validation lives
  there and returns `*app.ValidationError` for bad input.
- `internal/web` handlers and `internal/mcpserver` tools are thin adapters: parse input → call the
  service → render, redirect or return. Never put a rule in a handler or a tool.
- All writes go through `app.Service`. Web views may read through `Store` for rendering. The
  `import` command writes through `Store` directly because it is a bulk loader.
- `internal/finance` holds pure calculations (months, recurrences, cash flow, loans), and
  `internal/db` holds persistence only.
- Money is int64 cents everywhere. MCP tools take reais as decimal numbers and return
  `amount_cents` plus `amount_brl`.

## Every feature ships on both surfaces

A change that adds or changes behavior includes:
1. a service method in `internal/app`, with tests in `internal/app/*_test.go`;
2. the UI for it, in `internal/web` (handler, template);
3. an MCP tool in `internal/mcpserver/tools.go`, with the right annotation (`readOnly`, `write`,
   `idempotent`, `destructive`) and a pt-BR description. Cover it in `mcp_test.go`, and update the
   server `instructions` if the domain model changes.

If something deliberately exists on only one side, say why in the PR.

## Commands

```sh
gofmt -l . && go vet ./... && go test ./...
APP_INSECURE_NO_AUTH=1 DB_PATH=/tmp/cg/app.db go run ./cmd/app serve   # http://localhost:8080
```

To try the MCP locally, copy the URL from `/config` ("Conectar ao Claude"), then run
`claude mcp add --transport http cg-local http://localhost:8080/mcp/<token>`.

The MCP endpoint is `/mcp/{token}`. The token is stored in the `settings` table (`mcp_token`) and
can be rotated from Config. That route skips the login and same-origin middleware; the secret token
is its only auth.

Pushing to `main` runs CI (gofmt, vet, test, build) and deploys to the VM when the `DEPLOY_*`
secrets are set.
