# api-first-kanban

An API-first kanban service designed for **AI-agent-first work**: many AI
agent sessions create, track, update, move, and hand off work items on shared
boards while a human follows along in a real-time web UI. Agents work through
the REST API or the native MCP endpoint; humans use a lightweight reactive
frontend that updates in real time (SSE) as agents work.

**Working on this repo with an agent? Start with [AGENTS.md](AGENTS.md).**

## Stack

| Layer      | Choice                                              |
|------------|-----------------------------------------------------|
| Language   | Go 1.26 (stdlib `net/http` routing — no router dep) |
| Database   | PostgreSQL 18 (dev via podman compose)              |
| DB access  | `pgx/v5` + `sqlc` (typed queries), `goose` migrations|
| Frontend   | Svelte 5 (runes) + Vite 8 + TypeScript + Tailwind 4 |
| Realtime   | Server-Sent Events + Postgres `LISTEN/NOTIFY`       |
| MCP        | official `modelcontextprotocol/go-sdk` v1.7 — native `/mcp` endpoint (Streamable HTTP, stateless) |

## Quickstart

```sh
make dev-db      # podman compose up postgres (port 54329)
make migrate     # goose up
make run         # API on :18080 (8080 occupied by an unrelated app)
pnpm -C web dev  # UI on :5173 (proxies /api to :18080)
```

A release binary serves everything (UI, API, SSE, `/mcp`) from one port —
see [Release build](#release-build-single-binary).

## Architecture principles

- **API-first**: one transport-agnostic domain layer, thin sibling adapters —
  REST (`httpapi`) and MCP (`/mcp`) over it; the web UI is another REST
  client. Tool descriptions and agent guidance live in one place, next to the
  domain.
- **KISS / SOLID / DRY**: typed store, sqlc-generated parameterized queries,
  no router dependency.
- **One dev Postgres, two databases**: dev *and* tests share one Postgres
  instance; tests use a dedicated `kanban_test` database and never write to
  the live `kanban` database.
- **Loopback-only, no auth in v1**: the server refuses to bind a non-loopback
  `KANBAN_ADDR`. Agent identity is a plain string field on writes.
- **Append-only events**: every mutation emits an event consumed by SSE
  subscribers; a DB trigger rejects UPDATE/DELETE on events.

## Development

```sh
make test        # runs against the dedicated kanban_test database
KANBAN_TEST_STRICT=1 make test   # FAIL (not skip) if the test DB is unreachable
go build ./... && go vet ./...
pnpm -C web check && pnpm -C web build
```

Tests connect to `kanban_test` on the shared Postgres instance and apply the
goose migrations idempotently before running; when the instance is down they
skip unless `KANBAN_TEST_STRICT=1`. The live database is never touched by
`go test`.

## Release build (single binary)

```sh
make release        # builds + embeds the frontend -> bin/kanban-server
make test-release   # black-box smoke suite against the release binary
```

- **One port serves everything**: `KANBAN_ADDR` (default `127.0.0.1:18080`,
  loopback hosts only) for the UI, REST API, SSE and `/mcp`. Goose migrations
  run automatically at startup; `KANBAN_DATABASE_URL` sets the Postgres DSN.
- **Version stamping**: `make release` stamps `git describe --tags --always`
  into the binary via ldflags; plain `go build` reports `dev`. Verify the
  running build with `make check-deployed` (compares `/api/healthz`'s
  `commit` with `git rev-parse HEAD`).
- **Dev vs release**: development uses `make run` (API on `:18080`) plus
  `pnpm -C web dev` (Vite on `:5173` with an `/api` proxy); a release build
  serves the embedded frontend from the same Go binary. `make build` produces
  `bin/api` without the frontend, for backend work.
- Releases are tagged; CI builds the binary and attaches it (with
  SHA256SUMS) to the GitHub Release.

## Docs

- [`AGENTS.md`](AGENTS.md) — agent-facing guide: discovery, envelopes,
  hierarchy, conventions, workflow rules.
- [`docs/api.md`](docs/api.md) — the authoritative REST + SSE + MCP contract.
- [`docs/search-query.md`](docs/search-query.md) — board search query language.
