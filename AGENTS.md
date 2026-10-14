# AGENTS.md — working on and with api-first-kanban

This repository builds the kanban service that AI agents run on. If you are an
agent driving the service as a client (creating items, transferring work,
reading boards), start with **Discovery first** below; the authoritative wire
contract is [`docs/api.md`](docs/api.md) and the search query language is
[`docs/search-query.md`](docs/search-query.md). If you are an agent working ON
this repo's code, see **Repo conventions** and **Workflow rules**.

## Contact points

- **REST base URL:** `http://127.0.0.1:18080/api` (JSON bodies;
  `curl -H 'Content-Type: application/json'`)
- **MCP endpoint:** `http://127.0.0.1:18080/mcp` — stateless Streamable HTTP,
  protocol `2026-07-28` (the legacy `initialize` handshake negotiates down to
  `2025-11-25` and stays fully functional). No session state between requests;
  mutations emit the same events as REST.
- **Live updates:** SSE at `GET /api/events?board_id=…|workspace_id=…`
  (`: ping` heartbeat every 15s). On an unknown event `type`, refetch the
  snapshot (`GET /api/boards/{id}`) instead of guessing.
- Start the service with `make dev-db && make migrate && make run`
  (migrations also run automatically at startup).

Typical MCP client configuration:

```json
{
  "mcpServers": {
    "api-first-kanban": {
      "type": "streamable-http",
      "url": "http://127.0.0.1:18080/mcp"
    }
  }
}
```

Minimal stateless `tools/call` — no handshake needed:

```sh
curl -s -H 'Content-Type: application/json' \
  -H 'Accept: application/json, text/event-stream' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call",
       "params":{"name":"list_workspaces","arguments":{}}}' \
  http://127.0.0.1:18080/mcp
```

The response is SSE-framed; the JSON-RPC payload is the `data:` line.
Tool errors come back with `"isError":true` and the REST error envelope in
`content[0].text`, so a model can read the code and self-correct. The tools
mirror REST 1:1 — full table in [`docs/api.md`](docs/api.md).

## Response & error conventions

- **Single-resource mutations are wrapped:** `{"workspace":…}`, `{"board":…}`,
  `{"column":…}`, `{"item":…}`, `{"label":…}`, `{"comment":…}`,
  `{"relation":…}`.
- **Lists are plural:** `{"workspaces":[…]}`, `{"boards":[…]}`,
  `{"labels":[…]}`, `{"comments":[…]}`.
- **Errors always use the envelope** `{"error":{"code":"…","message":"…"}}`
  with `400` (validation, including malformed UUIDs), `404` (missing), `409`
  (duplicate name / deleting a non-empty column), `500` (internal).
- **PATCH semantics:** absent field = keep. `description: ""` clears it
  (stored `null`); explicit `null` keeps it. Empty `title`/`name` → `400`.
  `label_ids` **replaces** the whole set when present (`[]` clears).
- Request bodies are capped at 1 MiB.

## Discovery first

Never guess ids. Walk the hierarchy — workspaces → boards → snapshot — and
read the columns and labels before creating anything:

```sh
BASE=http://127.0.0.1:18080/api
curl -s $BASE/workspaces                                   # pick a workspace id
curl -s $BASE/workspaces/$WS/boards                        # pick a board id
curl -s $BASE/boards/$BOARD                                # full snapshot
curl -s $BASE/workspaces/$WS/labels                        # workspace labels
```

`GET /api/boards/{id}` is the one composite read (board + columns + items;
optionally `?q=<query>` for a searched view and `?include_archived=true`):

```json
{"board":{"id","workspace_id","name"},
 "columns":[{"id","board_id","name","position"}],
 "items":[{"id","column_id","title","owner_agent","labels":[…],"position",
           "closed","created_by","created_at","updated_at", …}]}
```

## Item hierarchy & kinds

`workspace` → `board` → `column` → `item`. Every item has a `kind`
(`milestone`, `epic` or `task`, default `task`) and an optional
`parent_item_id`; the hierarchy is at most three levels deep and the
kind/parent-kind rule enforces it (milestone: no parent; epic's parent must be
a milestone; task's parent may be epic or milestone). `GET /api/items/{id}`
returns the item plus its `children` (oldest first, archived included) in one
call.

- Every item view carries `closed_children`/`total_children` (direct
  children, workspace-wide, archived children excluded from both) and the
  roll-up pair `closed_descendants`/`total_descendants`. Render nothing when
  the total is `0`; there is no percent on the wire.
- No item/board/workspace **delete** in v1 — closing is the lifecycle;
  **archiving** (`POST /api/items/{id}/archive`) hides from default listings
  without deleting, and is independent of `closed`.
- Items link via directed **relations** (`blocks`, `relates_to`,
  `duplicates`); `(A blocks B)` means B is blocked by A.
- Comments accept `{"body","agent"}`; `author_type` is `"agent"` unless the
  agent field is absent or exactly `"human"`.
- Labels are workspace-scoped and attach only via `label_ids` on item
  create/patch. Prefer `?top=N` short lists and `?q=<text>` search before
  creating new labels (`409` on duplicate names).

### Move, transfer, position semantics

- `POST /api/items/{id}/move` `{column_id, index?}` — `index 0` = top
  (default), past-the-end clamps; target column must be on the item's board.
- `POST /api/items/{id}/transfer` `{to_agent, note?, agent?}` — sets
  `owner_agent` **and** auto-posts a `[handoff] <from> → <to> — <note>` audit
  comment in the same transaction.
- `position` is a JSON **string** decimal (fractional-midpoint ordering,
  30-digit precision): `"1"`, `"0.5"`, `"0.75"`. **Parse it numerically, never
  lexically** (`"10"` sorts before `"2"` as a string); ties break by
  `created_at`; a real collision triggers a silent renormalization of the
  column to `1..n` — responses and events always carry the final
  post-renormalization value. In Go use `math/big.Rat` or a decimal type; in
  Python `decimal.Decimal`; in JS a decimal library or `BigInt`, not default
  string comparison.

## Agent identity

There is no user table and no auth in v1 — identity is a plain string you
pass yourself:

- `"agent"` on every mutating REST body and every mutating MCP tool;
- defaults to `"human"` when omitted or blank;
- recorded as `created_by`/`owner_agent` on items, `author_name` on comments,
  and `agent` on every event;
- use a stable, unique-ish name per session (e.g. `worker-<task>-<n>`) so
  handoffs and the audit trail stay meaningful;
- delete endpoints (columns, labels, relations) also accept an optional
  `{"agent": "…"}` body and record it on their events.

## Repo conventions (code)

- Go stdlib `net/http` routing — no router dependency.
- Transport-agnostic domain layer in `internal/`; REST (`httpapi`) and MCP are
  thin sibling adapters over it; the web UI is just another REST client.
- Persistence goes through sqlc-generated typed queries (`internal/db`) —
  parameterized everywhere, no string interpolation into SQL.
- SOLID / DRY / KISS; append-only events (a DB trigger rejects
  UPDATE/DELETE).

## Build & test

```sh
go build ./... && go vet ./...
make test                      # runs against the dedicated kanban_test DB
KANBAN_TEST_STRICT=1 make test # FAIL (not skip) if the test DB is unreachable
pnpm -C web check              # svelte-check + tsc
pnpm -C web build              # frontend production build
make release                   # single embedded binary -> bin/kanban-server
make test-release              # black-box smoke suite against the release binary
```

The test suites use a dedicated `kanban_test` database on the shared dev
Postgres (started by `make dev-db`); the live `kanban` database is never
touched by `go test`. See README for the full dev/release story.

## Audit

`make audit` runs the repo audit suite (secret/PII/artifact checks over the
working tree). Keep it green: never commit credentials, personal paths, or
build artifacts.

## Workflow rules for agents working on this repo

- Work only in your own managed Orca worktree; commit on your branch;
  **never push** anywhere.
- Board hygiene: post a START comment and move your task to In Progress; post
  an evidence comment and move to In Review when done. **Never** move anything
  to Done — the orchestrator merges.
- Report progress and blockers to the orchestrator (not side channels); ask
  before expanding scope.
- Never paste secret values, credentials, or personal data into comments,
  messages, commits, or logs — reference `file:line` + type instead.
- After any backend commit, restart the API and confirm the running build
  matches the repo: `make check-deployed` compares `GET /api/healthz`'s
  `commit` field with `git rev-parse HEAD` (exit 1 on mismatch).

## Troubleshooting

| Symptom | Likely cause |
|---|---|
| `connection refused` on `:18080` | server not running (`make run`) or bound elsewhere |
| `{"error":{"code":"validation"…}}` | malformed UUID or empty required field |
| `{"error":{"code":"not_found"…}}` | the id does not exist (e.g. wrong workspace) |
| `{"error":{"code":"conflict"…}}` | duplicate name, or deleting a column that holds items |
| positions look "wrong" when sorted as strings | they are decimals: sort numerically |
| SSE stream stops updating | reconnect and refetch a snapshot; the client may have been dropped |
