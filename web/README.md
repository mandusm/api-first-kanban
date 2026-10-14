# web/ — api-first-kanban frontend

Vite + Svelte 5 (runes) + TypeScript + Tailwind CSS 4. Package manager: **pnpm**.

## Milestones

**M1 (done)** — scaffold: Vite + Svelte 5 (runes) + TS + Tailwind 4, dev proxy.

**M3a (done)** — realtime board view (read-only at the time):

**M3b (done)** — human mutations: add item, edit, close/reopen, move, transfer,
comments, label filter. Every write applies the server's response to the store
immediately, so behaviour is correct with or without the SSE stream.

| File | Role |
|------|------|
| `src/lib/types.ts` | API v1 contract types (REST + SSE envelope) |
| `src/lib/api.ts` | typed client, one function per endpoint, shared `ApiError` |
| `src/lib/kanban.svelte.ts` | board store as a Svelte 5 runes class (`$state`) + mutations |
| `src/lib/sse.ts` | `EventStream`: EventSource + exponential backoff + resync |
| `src/lib/format.ts` | `timeAgo`, label pill styling |
| `src/lib/Card.svelte` | item card + inline edit, move, close, transfer, comments actions |
| `src/lib/Column.svelte` | column + counter + add-item form |
| `src/lib/BoardView.svelte` | columns, label filter, owns the dialogs |
| `src/lib/TransferDialog.svelte` | hand-off to_agent + note (native `<dialog>`) |
| `src/lib/CommentsDrawer.svelte` | per-card comments list + composer |
| `src/lib/WorkspaceSelector.svelte` | workspace → board discovery |

### Response shape convention

Every single-resource mutation response is **wrapped** under the entity's
singular key (`{workspace}`/`{board}`/`{column}`/`{item}`/`{label}`/`{comment}`);
lists use plural keys; `GET /api/items/{id}` returns `{item, comments}` and
`GET /api/boards/{id}` returns `{board, columns, items}`. `unwrapEntity` in
`api.ts` enforces this strictly, throwing on a bare entity so contract drift
surfaces immediately instead of being silently tolerated.

`item.position` is an exact decimal **string**; ordering parses it with
`Number()` (never lexically) and falls back to `created_at`.

No router, no state library, no component library.

### Live updates

`App.svelte` owns one `EventStream` per open board. It is rebuilt whenever the
selected board changes, and every (re)open triggers `kanban.refresh()` so
events missed while disconnected are recovered from a board snapshot.
`KanbanStore.applyEvent` applies an event incrementally when the entity is
present (SSE entity keys are the singular type prefix: `item`, `column`,
`label`, `board`, `workspace`, `comment`) and falls back to a snapshot re-fetch
otherwise.

## Commands

```sh
pnpm install
pnpm dev      # dev server on :5173, proxies /api -> http://127.0.0.1:18080
pnpm build    # production build to dist/
pnpm preview  # serve the build
pnpm test     # unit tests for the markdown renderer (node --test)
pnpm check    # svelte-check + tsc (must pass clean)
```

## Markdown rendering

Item descriptions and comment bodies are rendered as markdown in the detail
modal by `src/lib/markdown.ts` — a dependency-free, escape-first subset
renderer (headings, bold/italic, inline + fenced code, lists, https links).
XSS-safe by construction: all HTML is escaped before any transform, so raw
`<script>`/`<iframe>`/event handlers/`javascript:` links can never survive;
plain text always renders sensibly. Tests: `src/lib/markdown.test.mjs`
(includes XSS vectors); live CDP suite: `e2e/pb104-comment-markdown.mjs`.

Set `VITE_API_URL` to bypass the dev proxy and talk to another API origin.

## Pinned versions (verified against npm, Oct 2026)

| Package                       | Version  |
|-------------------------------|----------|
| vite                          | ^8.3.2   |
| svelte                        | ^5.57.1  |
| @sveltejs/vite-plugin-svelte  | ^7.3.1   |
| tailwindcss                   | ^4.3.3   |
| @tailwindcss/vite             | ^4.3.3   |
| typescript                    | ~6.0.3   |
| svelte-check                  | ^4.7.6   |

TypeScript is pinned to 6.x deliberately: `svelte-check` 4.7.6 declares
`typescript: ^5 || ^6` as its peer range, so TS 7 (current `latest`) is not yet
supported by the checker.

## Tailwind 4

Tailwind 4 is wired through the `@tailwindcss/vite` plugin (see
`vite.config.ts`). There is **no `tailwind.config.js`** — configuration lives in
CSS via `@import "tailwindcss";` and `@theme` in `src/app.css`.

## Dev proxy / SSE

`vite.config.ts` proxies `/api` to `http://127.0.0.1:18080`. The SSE stream also
lives under `/api`; streaming through the dev proxy was observed working during
M3a verification (events arrived incrementally), but production reverse proxies
must not buffer it — see the `TODO(M3)` note in `vite.config.ts`.
