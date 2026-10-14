# fe2 — live CDP e2e harness

Plain Node `.mjs` scripts that drive headless Chrome over the DevTools
protocol (Node's built-in WebSocket — no puppeteer/playwright). Shared helpers
(req/fail/seed/bootstrap/psql + stack defaults) live in `lib/harness.mjs`;
CDP plumbing (connect/self-heal/eval/drag) stays in `fe2-cdp.mjs` and is
re-exported from the harness.

`m3a-cdp-check.mjs`, `m3b-cdp-check.mjs`, `stub-api*.mjs` are the earlier
stub-based checks, kept for reference; the `fe2-*` scripts are the live suite.

## Stack defaults (PB-81): ISOLATED by default, live is explicit opt-in

Every live suite defaults to the ISOLATED STACK (PB-68): FE2_API=:18081,
APP_URL=:5175, FE2_DB=kanban_test — a bare `node <suite>.mjs` can never touch
the live board DB. Live-stack use is an explicit opt-in:

```sh
FE2_STACK=live node pb67-labels.mjs     # live defaults (:18080/:5173/kanban)
FE2_API=… APP_URL=… FE2_DB=… node …     # explicit per-var override (wins)
```

## Isolated stack (PB-68): run the fe2 suite WITHOUT touching the live board DB

Run the suites against a dedicated API instance backed by `kanban_test` (this
is also the DEFAULT since PB-81 — the env exports below only need overriding
when the stack script's addresses change):

```sh
./scripts/e2e-test-stack.sh start   # API(:18081, kanban_test) + Vite(:5175)
export FE2_API=http://127.0.0.1:18081 \
       APP_URL=http://127.0.0.1:5175/ \
       FE2_DB=kanban_test \
       FE2_API_PID=/tmp/fe2-e2e-api.pid \
       FE2_DATABASE_URL='postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable' \
       FE2_API_BIN=/tmp/kan-e2e-api
cd web/e2e && node fe2-seed.mjs && node fe2-cdp-a.mjs && …
./scripts/e2e-test-stack.sh stop
```

Every fe2 script honors these env vars (isolated-stack defaults above; see
"Stack defaults"). The Vite dev server proxy target follows
`KANBAN_API_TARGET` (set by the stack script; default `http://127.0.0.1:18080`).

## Run

```sh
cd <repo-root> && go build -o /tmp/kan-api ./cmd/api && /tmp/kan-api &  # API (migrations run on start)
cd web && pnpm dev &                                                                           # Vite on :5173 (/api proxied to :18080)
google-chrome --headless=new --remote-debugging-port=9224 --user-data-dir=/tmp/fe2-chrome-profile about:blank &

cd web/e2e
node fe2-seed.mjs          # fresh workspace/board/label via REST → /tmp/fe2-seed.env
node fe2-cdp-a.mjs         # 14 checks: M3b mutation UI (DOM + Postgres + events)
node fe2-sse.mjs           # 8 checks: live SSE, R3a label chip, reconnect/resync, malformed-entity guard
node fe2-create-order.mjs  # 3 checks: fractional create-order, DOM vs DB
node fe2-moves.mjs         # 4 checks: ↑/↓ one-slot moves + reload snapshot order
node fe2-children.mjs      # 7 checks: PB-62 modal children list, status pills, SSE-live updates
node pb67-labels.mjs       # PB-67: add/remove item labels in the detail modal (REPLACE set, SSE); APP_URL=:5176+ (own vite)
node pb70-newlabel.mjs     # PB-70: create a label on the fly via the modal's + button (inline form, auto color, attach + reuse)
node pb72-picker.mjs       # PB-72: searchable label picker — top-8-by-usage default, ?q= search, search-before-create, create-if-empty (via "Create '<text>'")
node pb69-create-labels.mjs # PB-69: labels on task creation — picker in the create form (top-8 default, select existing + inline create), label_ids on the create request, chips on the card
node pb74-label-e2e.mjs    # PB-74 closeout: the full label story on one board — create form labels (PB-69) + on-the-fly create (PB-70) + picker top-8/search/create-if-empty (PB-72/73) + chips on cards + LABEL IN filter + modal add/remove persists across reload (PB-67)
node pb75-columns.mjs      # PB-75: column drag-and-drop reorder — real dragstart/dragover/drop flow, dense API positions, SSE convergence on a second client, ◀/▶ a11y nudge
node pb87-card-progress.mjs # PB-87: progress bar badge on parent cards — role=progressbar + aria values, tooltip-only percent, hidden for 0/0 and tasks, LIVE 2/5→3/5 on child close via PB-85 SSE parent re-emit (pre-PB-85 bases: bar updates on reload, not live)
```

Seed data is unique per `fe2-seed.mjs` run — re-seed before a session so the
per-title assertions (`events=1`, order indexes) are not confused by items
left on the board by earlier runs.

## Env vars

| Var            | Default               | Used by                        |
| -------------- | --------------------- | ------------------------------ |
| `FE2_STACK`    | isolated (`live` = live-stack defaults) | all suites (lib/harness.mjs) |
| `FE2_SEED`     | `/tmp/fe2-seed.env`   | all `fe2-*` scripts            |
| `FE2_API`      | `http://127.0.0.1:18081` | all REST-calling scripts       |
| `APP_URL`      | `http://127.0.0.1:5175/` | all CDP suites              |
| `FE2_DB`       | `kanban_test`         | psql assertion target          |
| `FE2_API_PID`  | `/tmp/fe2-api.pid`    | `fe2-sse.mjs` (pid bounced for the restart check) |
| `FE2_DATABASE_URL` | `postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable` | `fe2-sse.mjs` (DSN for the restarted API) |
| `FE2_API_BIN`  | `/tmp/kan-api`        | `fe2-sse.mjs` (restart check)  |
| `CDP_PORT`     | `9224`                | all scripts                    |

`fe2-seed.env` is generated data for the shared dev DB — never commit it.

## Hard-won lessons (encoded in `fe2-cdp.mjs` / `fe2-sse.mjs`)

- A stale headless chrome accepts the WebSocket but never answers CDP sends.
  The harness bounds every handshake/navigation send; on timeout it kills
  chrome, wipes `/tmp/fe2-chrome-profile`, starts a fresh instance, retries.
- `pkill -f` must not see its own pattern in the same shell's cmdline, and
  kill/launch must use separate shells (the launch cmdline contains the
  literal debug port).
- Never issue two `Page.navigate`s back to back — the session wedges.
- `psql -tA` alone still prints `INSERT 0 1` command tags; use `-tAq` when a
  query's output is parsed.
- The API's SIGTERM graceful shutdown hangs while SSE streams are open (the
  stream only ends when the client goes away); the harness SIGKILLs after
  ~12s and records this as a backend finding.
- Cross-column moves are drag & drop, so `fe2-cdp-a`/`m3b-cdp-check` synthesise
  `dragenter` → `dragover` → `drop` DragEvents on the target column
  (`dragCardToColumn` in `fe2-cdp.mjs`) carrying the item id in `text/plain`;
  in-column fine ordering still uses the ↑/↓ buttons.
