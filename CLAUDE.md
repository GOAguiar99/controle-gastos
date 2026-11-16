# controle-gastos

Personal finance web app (Go stdlib + PostgreSQL) that is also an MCP server, so Claude can read and
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
gofmt -l . && go vet ./... && TEST_DATABASE_URL=postgres://postgres:dev@localhost:55432/postgres?sslmode=disable go test ./...
APP_INSECURE_NO_AUTH=1 DATABASE_URL=postgres://cg:cg@localhost:55432/cg?sslmode=disable go run ./cmd/app serve   # http://localhost:8080
```

See the README for the local Postgres container. Tests get a fresh database each (`internal/db/dbtest`)
and skip without `TEST_DATABASE_URL` (CI sets it and fails instead).

## Tenancy

All user data belongs to a ledger. A `db.Store` is bound to one ledger (`DB.Ledger(id)`); the pool
sets `app.ledger_id` on every connection it hands out and Row-Level Security policies filter every
tenant table by it, so a query can't reach another ledger's rows even without a `WHERE`. Rules:
- New tenant tables need `ledger_id ... DEFAULT current_ledger()`, the RLS policy, and composite
  foreign keys `(ledger_id, x_id)` (FK checks bypass RLS).
- The app's DB role must not be superuser or BYPASSRLS (`db.Open` refuses it).
- Multi-statement writes go through `Store.InTx`.

## Identity

Users sign in with Google (OIDC, PKCE, nonce; `internal/web/auth.go`); `app.App` turns the identity
into an `app.Actor` (user + ledger) and builds the per-request `app.Service` with `App.For`. Handlers
get it with `svcOf(r)`, MCP tools with their `ctx`. Sessions and MCP links are random tokens stored
only as SHA-256 hashes (`sessions`, `mcp_tokens`). Identity tables have no RLS and are reached only
through `db.DB` methods, never a ledger `Store`. Locally, `APP_INSECURE_NO_AUTH=1` adds a dev login
(loopback only); web tests use a fake OIDC provider (`internal/web/fakeidp_test.go`).

To try the MCP locally, create a link in `/config` ("Conectar ao Claude"), then run
`claude mcp add --transport http cg-local http://localhost:8080/mcp/<token>`.

The MCP endpoint is `/mcp/{token}`: each link belongs to one user and ledger and can be revoked from
Config. That route skips the session and cross-origin middleware; the secret token is its only auth.

Pushing to `main` runs CI (gofmt, vet, test, build) and deploys to the VM when the `DEPLOY_*`
secrets are set.
