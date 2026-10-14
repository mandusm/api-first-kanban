# API v1 — api-first-kanban

Base URL: `http://127.0.0.1:18080`. All routes live under `/api`, bodies are JSON.

## Conventions

**Wrappers**: single-resource mutation responses are wrapped —
`{"workspace":…}`, `{"board":…}`, `{"column":…}`, `{"item":…}`, `{"label":…}`,
`{"comment":…}`, `{"relation":…}`. Lists are plural — `{"workspaces":[…]}`, `{"boards":[…]}`,
`{"labels":[…]}`. `{"comments":[…]}`. `GET /api/boards/{id}` is the full
composite response (`{"board":…,"columns":[…],"items":[…]}`);
`GET /api/boards/{id}/items` returns `{"items":[…],"next_cursor":…}`.

**Errors** always use the envelope:

```json
{"error":{"code":"...","message":"..."}}
```

| Status | Meaning    |
|--------|------------|
| 400    | validation (incl. malformed UUIDs → `"validation"` code) |
| 404    | missing    |
| 409    | conflict (duplicate name, delete column when it holds items) |
| 403    | forbidden (reserved) |
| 500    | internal   |

**Agent identity**: every mutating request accepts an optional `"agent"`
string field; it defaults to `"human"`. It is recorded on events.

**Item ordering**: `position` is a JSON *string* decimal ("1", "0.5", "0.75")
carrying a fractional midpoint: a new item between neighbors n1 and n2 gets
(n1+n2)/2 (30-digit precision, trailing zeros trimmed); top of a column →
min/2; empty column → "1"; append → last+1. Duplicates trigger a silent
renormalization of the column to 1..n (integer positions), which preserves
order; clients must parse position numerically, never lexically.

**PATCH semantics**: absent field = keep current value. `description: ""`
explicitly CLEARS the description (stored null); `null` keeps it (v1).
Empty `title`/`name` when present → 400.

**Item keys**: every item carries a stable, globally unique human key
`<board key_prefix>-<sequence>` (e.g. `PB-123`) on every item view (REST
snapshot, paginated items, `get_item`, MCP tools, SSE payloads). The key is
composed once at create and never changes across move/patch/archive/transfer.
The board's `key_prefix` is an uppercase initialism of the board name (≤ 5
chars, numeric suffix on collision, e.g. `PB`, `PB2`) chosen at board creation
and immutable thereafter; the sequence is per board and gap-free, allocated in
the create transaction. Keys match `^[A-Z][A-Z0-9]{0,4}-[1-9][0-9]*$`.

## Health

- `GET /api/healthz` → `200`
  `{"status":"ok","db":true,"commit":"<40-hex>|<40-hex>-dirty|unknown"}`;
  `503` with `db:false` when the database is unreachable (the process itself is
  still up, so the handler always answers).
- `commit` is the build identity of the running binary: the Go toolchain's
  `vcs.revision` (`-dirty` suffix when the working tree had uncommitted changes),
  the `KANBAN_COMMIT` env override, or `"unknown"` for builds with no VCS
  stamping. Compare it to `git rev-parse HEAD` (`make check-deployed`) to catch a
  server that was never restarted after a merge.

## Workspaces

- `GET /api/workspaces`
  → `200` `{"workspaces":[{id,name,description,created_at}]}`
- `POST /api/workspaces` `{name, description?}` → `201` `{"workspace":{...}}`;
  `409` duplicate name.

## Boards & columns

- `GET /api/workspaces/{id}/boards` — unknown workspace → `404`
  → `200` `{"boards":[{id,workspace_id,name,key_prefix,created_at}]}`
- `POST /api/workspaces/{id}/boards` `{name, columns?:string[]}` — unknown
  workspace → `404`; duplicate board name → `409`
  → `201` `{"board":{id,workspace_id,name,key_prefix,"columns":[{id,name,position}]}}`
  `columns` defaults to `["todo","doing","done"]`, positions 0..n.
  `key_prefix` is the board's immutable item-key namespace (see **Item keys**).
- `GET /api/boards/{id}?include_items=&include_archived=` — full snapshot (the UI
  payload); archived items are excluded unless `?include_archived=true`:
  `200` `{"board":{id,workspace_id,name,key_prefix},"columns":[{id,name,position,item_count}],"items":[{id,column_id,key,title,description,owner_agent,priority,kind,parent_item_id,position,labels:[label],closed,closed_children,total_children,archived_at,created_by,created_at,updated_at}]}`
  The snapshot is COMPLETE and unchanged; it is the path SSE resync uses.
  - Every column carries `item_count`: the number of live (non-archived) items
    in that column, respecting `include_archived` exactly as an item page would.
  - Every item carries the derived progress fields `closed_children` and
    `total_children` (see **Child progress** below). The lightweight
    `?include_items=false` envelope has no `items` array, so it carries no
    progress fields.
  - `include_items` defaults to true. `?include_items=false` returns the same
    envelope with `"items":[]` and the same per-column `item_count`, without
    walking the items table — a lightweight board+columns read for lazy loading.
    Only the literal `false` opts out.
  - `q=<query>` selects the **searched view** (search query language, see
    `docs/search-query.md`): items are filtered in memory over the full snapshot
    with board order preserved, and every column's `item_count` is derived from
    the **filtered** set (what you see is what's counted). A queried view always
    returns the full composite including the filtered `items` array —
    `include_items` is ignored under `q`. `include_archived` composes with `q`
    (archived items are still excluded by default). An empty/whitespace-only `q`
    is treated exactly like an absent `q` (today's behavior, unchanged).
    A query that fails to parse → `400` with the standard error envelope
    `{"error":{"code":"validation","message":"<message>"}}`, where `<message>`
    is the parse-error string per spec §6:
    `parse error at <line>:<col> (offset <n>): <description>`. Example:
    `?q=CREATED%20%3C%20-1d%20AND%20LABEL%20IN%20('bug')`. Syntactically valid
    queries with no matches → `200` with empty items and zero counts.
- `GET /api/boards/{id}/items?column_id=&limit=&cursor=&include_archived=` —
  keyset pagination
  of ONE column, for boards with many items:
  `200` `{"items":[{…same item view as the snapshot…}],"next_cursor":"…"|"null"}`.
  Archived items are excluded unless `include_archived=true`. Each item view
  carries the derived `closed_children`/`total_children` progress fields (see
  **Child progress**).
  Items are ordered by `(position, id)`; the tiebreak is the item id (the
  snapshot instead breaks position ties by `created_at`, so pages and the
  snapshot agree whenever positions are distinct). This endpoint does **not**
  accept `q` — searched views are snapshot-only (spec §7.1).
  - `column_id` is **required** in v1 (single-column keyset). Missing,
    malformed, unknown, or a column of another board → `400`.
  - `limit` defaults to `100` and must be in `1..500`; `0`, negative or
    `>500` → `400`.
  - `cursor` is the opaque `next_cursor` token from the previous page (omit it
    for the first page); a malformed or unknown token → `400`. `next_cursor`
    is `null` on the last page (also for an empty column, which returns
    `"items":[]`).
  - Concurrent inserts do not shift the window: a new top-of-column item has
    a smaller position than the cursor, so the next page neither repeats nor
    skips existing items.
  - Pagination is an ADDITIONAL read path only: the full snapshot above is
    not truncated and carries no per-column item counts.
- `POST /api/boards/{id}/columns` `{name, position?}` → `201`
  `{"column":{id,board_id,name,position}}`; omitted `position` appends at the
  end. `409` duplicate column name.
- `PATCH /api/columns/{id}` `{name?, position?}` → `200` `{"column":…}`
  (both fields applied when present; empty name → 400)
- `DELETE /api/columns/{id}` `{agent?}` → `204`; `409` if the column holds
  items (single guarded statement — no delete race). The optional `agent`
  body field is recorded on `column.deleted`, defaulting to `"human"`.
- `POST /api/columns/{id}/move` `{index?}` → `200` `{"column":…}`. Reorders
  the column on its board: index 0 = first (omitted = 0), past-the-end
  clamps. Positions are renumbered densely 0..n-1 in one transaction. The
  `column.moved` SSE event carries `data.column` (the moved column) plus
  `data.columns` — the FULL reordered sibling list — because every column's
  position may change. `400` negative index.

## Items

No item delete in v1 — closing is the lifecycle; **archiving hides** an item
from the default listings without deleting it. `archived_at` is a nullable
timestamp on every item view: `null` = active, a RFC3339 timestamp = archived.
Archive state is independent of `closed` (a closed item archives fine and an
archived item keeps its closed badge).

### Archive lifecycle

- `POST /api/items/{id}/archive` `{agent?}` → `200` `{"item":…}` — sets
  `archived_at = now()`. `GET /api/boards/{id}` and
  `GET /api/boards/{id}/items` **exclude archived items by default**; pass
  `include_archived=true` to either to include them. Archiving an
  already-archived item → `409`; unknown item → `404`; malformed id → `400`.
- `POST /api/items/{id}/unarchive` `{agent?}` → `200` `{"item":…}` — clears
  `archived_at` (active again). Unarchiving an item that is not archived →
  `409`; unknown item → `404`.
- **No cascade**: archiving touches nothing but the item itself — its children,
  labels, comments and relations are untouched, and an archived item MAY still
  be referenced as a `parent_item_id`. Archived items stay reachable by
  `GET /api/items/{id}`.

### Hierarchy (milestone → epic → task)

Every item has a `kind` (`milestone`, `epic` or `task`, default `task`) and an
optional `parent_item_id`. The hierarchy is at most three levels deep; the
`kind`/parent-kind rule enforces this, so cycles are impossible by
construction (no separate cycle detection):

- a **milestone** has no parent (a `parent_item_id` on a milestone → `400`);
- an **epic**'s parent, when present, must be a **milestone** (a non-milestone
  parent → `400`); a parentless epic is a valid top-level item;
- a **task**'s parent may be an **epic** or a **milestone** (a task with no
  parent is top-level).

A parent must be an existing item in the **same workspace** (`400` otherwise;
an unknown parent → `404`). Board snapshots carry `kind` + `parent_item_id` on
every item; clients derive the grouping. `GET /api/items/{id}` additionally
returns the item's `children` (oldest first, archived included), so a parent's
sub-items and their statuses can be read in one call — including children not
loaded by a lazily-paginated board view. There is no cascade delete — items are
closed, never deleted.

- `POST /api/boards/{id}/items`
  `{title, description?, column_id?, owner_agent?, priority?, kind?, parent_item_id?, label_ids?, agent?}`
  → `201` `{"item":{…}}` — goes to the TOP of the target column; `column_id`
  defaults to the first column by position; `label_ids` referencing labels
  from another workspace → `400`. `priority` is an integer enum `0..3`
  (`0`=P0 blocker, `1`=P1 high, `2`=P2 normal, `3`=P3 low): it defaults to `2`
  when omitted and is echoed in every item payload and board snapshot; a value
  outside `0..3` → `400`. `kind` defaults to `task`; an invalid kind or an
  invalid kind/parent combination → `400`.
- `GET /api/items/{id}` → `200`
  `{"item":{…labels flattened…},"comments":[…],"relations":{"outgoing":[…],"incoming":[…]},"children":[…]}`
  — unknown item → `404`. See Relations below. `children` are the item's
  child items (full item shape, labels flattened), ordered `created_at`
  ascending (positions are per-column and not comparable across children);
  archived children are included, and children on other boards of the same
  workspace appear too (the parent rule is workspace-scoped). Empty array
  when the item has no children (`[]`, never `null`). Both the `item` and
  every row of `children` carry the derived `closed_children`/`total_children`
  progress fields (see **Child progress**); note that `children` LISTS archived
  children while the counts exclude them.
- `PATCH /api/items/{id}`
  `{title?, description?, owner_agent?, priority?, kind?, parent_item_id?, label_ids?, closed?, agent?}`
  → `200` `{"item":…}`. `label_ids` REPLACES the full set when present.
  `priority` absent keeps the current value; a value outside `0..3` → `400`.
  `kind` absent keeps the current value; changing to a value that conflicts
  with the item's parent (or existing kind) → `400`. `parent_item_id` is
  three-state: absent keeps the current parent, a string sets it (validated as
  above), and an explicit `null` clears it — clearing is rejected when the
  resulting kind/parent combination would be invalid (e.g. a milestone with a
  parent) → `400`.
  `closed: true` sets `closed_at`; `closed: false` reopens (clears it).
- `POST /api/items/{id}/move` `{column_id, index?}` → `200` `{"item":…}`.
  `index 0` = top (default 0); index beyond the end clamps to the end; the
  target column must belong to the item's board (`400` otherwise).
- `POST /api/items/{id}/transfer` `{to_agent, note?, agent?}` → `200`
  `{"item":…}`. Sets `owner_agent` AND auto-posts the handoff audit comment:
  author_name = requesting agent, body = `[handoff] <from> → <to>`
  (`→ <to>` alone when `from` is empty) + (` — <note>` when present).
  Missing `to_agent` → `400`.

### Child progress (`closed_children` / `total_children`)

Every payload that embeds an item view — board snapshot items, column pages,
`GET /api/items/{id}` (the `item` AND each row of `children`), and every item
mutation response (`create`, `patch`, `move`, `transfer`, `archive`,
`unarchive`) — carries two additive integer fields:

- `total_children` — the item's **direct** children (one level down, via
  `parent_item_id`), across the whole **workspace** (a child may sit on another
  board); grandchildren are NOT counted;
- `closed_children` — how many of those direct children have `closed = true`.

Read the pair as "% of **direct** children closed". It is deliberately flat,
not a rollup: nothing auto-closes a parent when its children finish, so an
epic's own `closed` flag remains the source of truth for epic completion — and
a milestone can read low (even `0/…`) while every task beneath it is done but
the intervening epic was never closed.

- **Archived children are excluded from the numerator AND the denominator**,
  always — including under `include_archived=true`. In those views the archived
  children are still LISTED (dimmed/tagged) but ignored by the counts; that is
  intentional and self-consistent (one number everywhere).
- `total_children == 0` (a leaf, or a parent whose children are all archived)
  means "no indicator" — render nothing, not `0/0`.
- There is **no percent** on the wire; derive `closed_children /
  total_children` client-side when a percentage is needed.

### Activity timestamp (`activity_at`)

Every payload that embeds an item view (same payloads as the progress fields
above) also carries `activity_at` (PB-122): the item's `updated_at` or the
`created_at` of its newest comment, whichever is later. Adding a comment
deliberately does **not** bump `updated_at` — comment recency surfaces only
through `activity_at`. Use it for "most recently touched" views (the board UI's
"Recently updated" sort) and never for change detection (`updated_at` remains
the item-mutation timestamp).

### Subtree progress (`closed_descendants` / `total_descendants`)

Every payload that carries the child-progress pair above also carries two
additive integer fields (PB-118):

- `total_descendants` — the item's **entire non-archived subtree** at any depth
  (milestone → epic → task, via recursive `parent_item_id` walk), across the
  whole **workspace** (a descendant may sit on another board);
- `closed_descendants` — how many of those descendants have `closed = true`.

Read the pair as "% of the **whole subtree** closed". Archived descendants are
excluded from the numerator AND the denominator (same rule as the direct
counts). A leaf (or a parent whose descendants are all archived) reads `0/0`
— no indicator. When the item has only direct children (no grandchildren),
`closed_descendants == closed_children` and `total_descendants ==
total_children`. Unlike the direct pair this IS a roll-up: a milestone reads
its full nested progress here even when the intervening epics were never
closed.

### Auto transition of parent columns (milestone / epic)

When a **child** item is moved between columns (`POST /api/items/{id}/move`)
or its `closed` flag is flipped (`PATCH /api/items/{id}`), the server examines
the child's **direct parent** and, when that parent is a `milestone` or `epic`
on the **same board**, automatically transitions the parent's column. Tasks
never auto-move, and the rule never cascades beyond depth 1 (a milestone's
auto-transition does not itself trigger the milestone's own parent).
Cross-board children still count toward roll-up but never drive the parent's
column.

Column roles are derived from the column **name** (lowercased, trimmed), so
the rule works on any board — the default board (`todo`/`doing`/`done`) and the
project board (`Backlog`/`In Progress`/`In Review`/`Done`) alike:

| Column name (case-insensitive) | Role |
|---|---|
| `todo`, `backlog` | todo |
| `doing`, `in progress` | in_progress |
| `done` | done |
| anything else (e.g. `in review`) | unclassified — never a target |

`in review` is deliberately **not** `done` — review is a human gate.

Rules (evaluated in the same transaction as the child mutation, so the emitted
events carry post-mutation state):

- **Start:** when the parent is in a `todo` column and at least one
  non-archived child is in an `in_progress` column, the parent moves to the
  `in_progress` column (top). Emits `item.moved` for the parent.
- **Finish:** when the parent is in a `todo` or `in_progress` column and
  **all** non-archived children are closed, the parent moves to the `done`
  column **and** is closed (`closed = true`). Emits `item.moved` for the
  parent (the entity reflects `closed`).
- **Reverse (best-effort):** when the parent is closed or in `done` and a child
  is reopened or moved out of `done` into a non-done column, the parent is
  reopened (`closed = false`) and, if it was in `done`, moved to
  `in_progress`. Emits `item.moved`/`item.updated` for the parent.
- **Idempotent:** if the parent already satisfies the target state, nothing
  happens (no spurious move or event).

A parent with **zero** non-archived children is never auto-moved (no signal).

## Relations

Items can be linked with a directed relation. There are three relation
values: `blocks`, `relates_to`, `duplicates`. Direction matters: **(A blocks
B) means B is blocked by A** — "blocked by" is the derived **incoming** view,
never a stored value.

- `POST /api/items/{id}/relations` `{to_item_id, relation, agent?}` → `201`
  `{"relation":{…}}`. The `{id}` is the `from` item. `relation` must be one of
  the three values (`400` otherwise); both items must exist and live in the
  SAME workspace (`404` / `400`); self-relations are rejected (`400`); an exact
  `(from,to,relation)` duplicate → `409`.
- `DELETE /api/relations/{relation_id}` `{agent?}` → `204`; unknown id → `404`.

A **relation edge** (`GET /api/items/{id}` returns one item's edges as
`{"outgoing":[…],"incoming":[…]}`):

```json
{"id":"<relation id>","relation":"blocks","direction":"outgoing",
 "item":{…the item at the OTHER end, same shape as any item view…},
 "created_by_agent":"agent","created_at":"2026-…"}
```

`direction` is `"outgoing"` for edges the item is the source of and
`"incoming"` for edges pointing at it; `item` is the target for outgoing and
the source for incoming. So for `A blocks B`: `GET /api/items/A` lists the
outgoing `blocks` edge (item = B) and `GET /api/items/B` lists the incoming
`blocks` edge (item = A) — the derived "blocked by" view.

## Labels

- `GET /api/workspaces/{id}/labels` — unknown workspace → `404`
  → `200` `{"labels":[{id,workspace_id,name,color}]}`
  - With **no** query params the response is byte-identical to the v1 shape
    above (all labels, name-ordered) — existing consumers are unaffected.
  - `?top=N` (positive integer) returns only the N most-used labels, ranked by
    `usage_count` (count of item-label links across the workspace's items,
    archived items included), ties broken by name ascending. Invalid `top`
    → `400`.
  - `?q=<text>` filters by case-insensitive literal name substring **before**
    ranking (filter-then-rank) — `%` and `_` match themselves, not wildcards;
    `?top=N&q=…` combines: search within the workspace, then rank the matches
    and take the top N.
  - When either param is present **with a non-blank value**, each label
    carries `usage_count`:
    `{"labels":[{id,workspace_id,name,color,usage_count}]}`. An empty or
    whitespace-only `?top=` or `?q=` counts as absent (legacy shape).
- `POST /api/workspaces/{id}/labels` `{name, color?}` → `201` `{"label":…}`
  (default `#6366f1`); `409` duplicate name.
- `PATCH /api/labels/{id}` `{name?, color?}` → `200` `{"label":…}`
- `DELETE /api/labels/{id}` → `204`

## Comments

- `GET /api/items/{id}/comments` → `200` `{"comments":[…]}` ascending
- `POST /api/items/{id}/comments` `{body, agent?}` → `201` `{"comment":…}`;
  `author_type` = `"human"` iff `agent` is absent or `"human"`, else `"agent"`.

## Events (SSE)

- `GET /api/events?board_id=&workspace_id=` — SSE stream, filters optional
  (none = all). `text/event-stream`; heartbeat comment `: ping` every 15s.
- Event `data` is one JSON object: `{"type","workspace_id","board_id","agent","at","
  <entity key>":{...}}` where the entity key always matches the singular type
  prefix: `item.*` → `data.item`, `comment.added` → `data.comment`,
  `column.*` → `data.column`, `label.*` → `data.label`, `relation.*` →
  `data.relation`, `board.created` → `data.board`, `workspace.created` →
  `data.workspace` — each object in the exact shape of the corresponding REST
  response, so `item.*` entities carry the derived `closed_children`/
  `total_children` progress fields (see **Child progress**).
- Event types: `workspace.created`, `board.created`,
  `column.created/updated/moved/deleted`, `item.created/updated/moved/transferred/archived/unarchived`,
  `comment.added`, `label.created/updated/deleted`,
  `relation.created/deleted`.
- Clients receiving an unknown `type` should fall back to a snapshot refetch.
- Mechanism: the event INSERT + `pg_notify('kanban_events', '{"event_id":N}')`
  happen in the SAME transaction as the mutation (a lightweight reference —
  no 8000-byte ceiling); the Go hub LISTENs, hydrates the full `
  events.payload` row by PK, and fans out per connection. LISTEN reconnects
  with bounded backoff.
- Events ALWAYS populate `workspace_id` AND `board_id` (workspace_id derived
  from the board for item/column/comment events; workspace events carry
  `board_id: null`), so either filter mode sees everything.

## MCP endpoint

`POST /mcp` — a **stateless Streamable HTTP** MCP endpoint (protocol
2026-07-28; the legacy `initialize` handshake negotiates down to 2025-11-25,
`server/discover` reports all supported versions). It mounts on the same server
as the REST API and is a thin adapter over the same service layer: every tool
call runs identical domain logic, so mutations keep emitting events + SSE
exactly as the REST routes do. No session state is kept between requests.

Tools mirror the REST resources (same wrappers, same plural list keys, and the
same `{"error":{"code","message"}}` envelope surfaced as an MCP tool error):

| Tool | REST equivalent |
|------|-----------------|
| `list_workspaces` | `GET /api/workspaces` |
| `create_workspace` | `POST /api/workspaces` |
| `list_boards` | `GET /api/workspaces/{id}/boards` |
| `create_board` | `POST /api/workspaces/{id}/boards` |
| `get_board` | `GET /api/boards/{id}` (composite snapshot; `include_archived` opt-in) |
| `create_column` | `POST /api/boards/{id}/columns` |
| `move_column` | `POST /api/columns/{id}/move` |
| `create_item` | `POST /api/boards/{id}/items` |
| `get_item` | `GET /api/items/{id}` |
| `update_item` | `PATCH /api/items/{id}` |
| `move_item` | `POST /api/items/{id}/move` |
| `transfer_item` | `POST /api/items/{id}/transfer` |
| `archive_item` | `POST /api/items/{id}/archive` |
| `unarchive_item` | `POST /api/items/{id}/unarchive` |
| `add_relation` | `POST /api/items/{id}/relations` |
| `remove_relation` | `DELETE /api/relations/{id}` |
| `list_labels` | `GET /api/workspaces/{id}/labels` |
| `create_label` | `POST /api/workspaces/{id}/labels` |
| `list_comments` | `GET /api/items/{id}/comments` |
| `add_comment` | `POST /api/items/{id}/comments` |

Every mutating tool accepts the same optional `agent` string (default
`"human"`). Label attach/detach uses `label_ids` on `create_item`/`update_item`,
which REPLACES the item's full label set like the REST field of the same name.
The item hierarchy is mirrored too: `create_item`/`update_item` take `kind` and
`parent_item_id`, and on `update_item` an explicit `null` `parent_item_id`
clears the parent (absent keeps it) under the same kind/parent-kind rule
(milestone -> epic -> task, at most three levels deep, parent in the same
workspace). Priority is mirrored as the `priority` integer enum on
`create_item`/`update_item` (default `2` when omitted). `get_item` returns the
same composite payload as `GET /api/items/{id}` — including the item's
`children` (oldest first, archived included) — so an agent can read an epic's
sub-tasks and their statuses in one call.
The archive lifecycle is mirrored too: `archive_item`/`unarchive_item` set and
clear `item.archived_at`, every item payload carries `archived_at`, and
`get_board` excludes archived items unless `include_archived` is true (archive
is independent of `closed` and never cascades).
The MCP server `instructions` carry the PATCH/lifecycle/position/identity
guidance from this document, plus the priority scale (P0..P3), the hierarchy
kind/parent rules and the relation direction semantics, so a fresh agent can
discover and use them from `/mcp` alone.