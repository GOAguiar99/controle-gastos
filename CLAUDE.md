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
3. an MCP tool in `internal/mcpserver/tools.go` (register with `addTool`, which keeps output schemas open to new fields), with the right annotation (`readOnly`, `write`,
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
- The tenant is set when a pooled connection is acquired (`PrepareConn`), not per query. Inside a
  `pgx` transaction begun without a ledger in ctx, RLS hides every tenant row; set it with
  `set_config('app.ledger_id', ..., true)` if such a transaction must read tenant data
  (see `db.JoinViaInvite`).

Sharing: a ledger has members (`ledger_members.role` = `owner` | `member`, still one ledger per user).
The owner invites by email (`ledger_invites`, 7 days, max 5 people counting open invites). Signing
in with that exact email counts as approval and joins the ledger; someone who already has a ledger
accepts in Config, and their own ledger is dropped only if it is empty. Owners remove members and
members leave; removal revokes sessions and MCP links. Web-only (account-level), in
`internal/app/sharing.go`. Transactions record `created_by` from the acting user bound to the Store
(`Store.WithUser`, set in `App.For`), never from input; the UI shows it only in shared ledgers.

## Identity

Users sign in with Google (OIDC, PKCE, nonce; `internal/web/auth.go`); `app.App` turns the identity
into an `app.Actor` (user + ledger) and builds the per-request `app.Service` with `App.For`. Handlers
get it with `svcOf(r)`, MCP tools with their `ctx`. Sessions and MCP links are random tokens stored
only as SHA-256 hashes (`sessions`, `mcp_tokens`). Identity tables have no RLS and are reached only
through `db.DB` methods, never a ledger `Store`. Locally, `APP_INSECURE_NO_AUTH=1` adds a dev login
(loopback only); web tests use a fake OIDC provider (`internal/web/fakeidp_test.go`).

To try the MCP locally, create a link in Perfil → Agentes de IA (`/perfil/agentes`), then run
`claude mcp add --transport http cg-local http://localhost:8080/mcp/<token>`.

Access control (`users`): `approved_at` NULL = sign-up waiting for approval (`APP_SIGNUP=approval`,
default; `SignIn` returns `app.ErrPending`, creates no ledger and no session), `suspended_at`, and
`role` (`user` | `admin`). `App.actor` reloads the user on every request, so these apply at once.
Settings live in the profile menu (avatar, top-right): `/perfil` tabs Conta, Preferências,
Agentes de IA and Compartilhar (`profileData` loads per tab; `GET /config` redirects there; the POST
routes keep their `/config/...` paths). Admins use `/admin` (also in that menu) (approve, reject pending, suspend, reactivate; never financial data). Every admin
method in `internal/app/admin.go` checks the role server-side plus a fresh sign-in
(`Actor.AuthAt`, `AdminFreshness`); non-admins get 404. The admin role is granted only by the CLI
(`grant-admin`), never by email or env. Admin actions, account deletion and the JSON export are
web-only on purpose (an MCP link is a bearer URL driven by an LLM).

`APP_AUTH=password` replaces Google with email + password for self-hosting (`docker-compose.yml`):
accounts are `google_sub = "local:<email>"`, created only by the CLI `set-password`, argon2id hashes,
5 failures lock 15 min, one generic error for every failure, and `web.ValidateBaseURL` refuses plain
HTTP off localhost. Security events go through
`app.Audit` (JSON to stderr/journald): pass ids and categories, never tokens, emails or financial text.
See `docs/SECURITY.md` for controls, runbooks and the LGPD checklist.

The MCP endpoint is `/mcp/{token}`: each link belongs to one user and ledger and can be revoked from
the profile. That route skips the session and cross-origin middleware; the secret token is its only auth.

Pushing to `main` runs CI (gofmt, vet, test, govulncheck, gitleaks, build) and deploys to the VM when the `DEPLOY_*`
secrets are set.
