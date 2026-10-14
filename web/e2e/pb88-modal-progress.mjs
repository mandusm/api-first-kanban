// PB-88: the item detail modal shows child progress for parents, reading the
// SERVER fields (closed_children/total_children) as the single source — never
// a client-side recompute from the children list. Self-seeding like the other
// fe2-* suites; isolated stack by default (see lib/harness.mjs).
//
// Flow covered:
//   a. seed milestone M + 5 task children (T1..T5) + close two of them via the
//      API; plus an epic child E under M with 2 tasks (1 closed) for the
//      per-child mini-bar
//   b. open M's modal -> header bar + "N of M closed (P%)" text EXACTLY match
//      the server item payload's closed_children/total_children (fetched fresh
//      from GET /api/items/:id at assert time)
//   c. close another child via the API while the modal is open -> the display
//      updates live over SSE and still matches the (refetched) server fields
//   d. childless task T4 shows NO progress display
//   e. E's row in M's modal shows a mini progressbar; a leaf child row does not
//   f. reopen M's modal after the live update (fresh mount) -> still matches
//
// Cleanup: every pb88e2e-* item is closed + archived via the API.
import {
  APP_URL, req, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const AGENT = 'pb88-e2e'

const { results, pass, fail } = checks()

const { ws, board, columns } = await seedWorkspace({ name: `pb88-ws-${TS}`, agent: AGENT })
const [TODO, , DONE] = columns.map((c) => c.id)
console.error(`[t] seeded workspace=${ws.id} board=${board.id}`)

const mk = async (title, kind, parent, column, extra = {}) => {
  const { item } = await req('POST', `/api/boards/${board.id}/items`, {
    title, kind, column_id: column, ...(parent ? { parent_item_id: parent } : {}), ...extra, agent: AGENT,
  })
  return item
}
const M = await mk(`pb88e2e-m-${TS}`, 'milestone', null, TODO)
const E = await mk(`pb88e2e-epic-${TS}`, 'epic', M.id, TODO)
const T1 = await mk('task-one', 'task', M.id, TODO)
const T2 = await mk('task-two', 'task', M.id, TODO)
const T3 = await mk('task-three', 'task', M.id, DONE)
const T4 = await mk('task-four', 'task', M.id, TODO)
const T5 = await mk('task-five', 'task', M.id, TODO)
const T6 = await mk('task-six', 'task', M.id, DONE)
const E1 = await mk('epic-task-one', 'task', E.id, TODO)
const E2 = await mk('epic-task-two', 'task', E.id, DONE)
const T9 = await mk('childless-task', 'task', null, TODO)

await req('PATCH', `/api/items/${T3.id}`, { closed: true, agent: AGENT })
await req('PATCH', `/api/items/${T6.id}`, { closed: true, agent: AGENT })
await req('PATCH', `/api/items/${E2.id}`, { closed: true, agent: AGENT })

const serverFields = async (id) => {
  const { item } = await req('GET', `/api/items/${id}`)
  return { closed: item.closed_children, total: item.total_children }
}
const domProgress = () =>
  js(`const el=document.querySelector('dialog section[data-children] [data-progress]');if(!el)return null;return {
    label: el.querySelector('span.text-\\\\[11px\\\\]').textContent.trim(),
    now: Number(el.querySelector('[role=progressbar]').getAttribute('aria-valuenow')),
  }`)

console.error('[t] connecting cdp')
const exceptions = []
const { ws: cdp, send, onMessage } = await connectCdpPage({ url: APP_URL })
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
})
const { js, soft, waitJs } = makeEval(send)

async function ready() {
  return waitAppReady({ soft })
}
async function selectBoard() {
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id }))) return false
  return waitJs(`return !!document.querySelector('article[data-item="${M.id}"]')`, 150)
}
const openModal = (id) =>
  js(`const b=document.querySelector('article[data-item="${id}"] h3 button'); if(!b) return false; b.click(); return true`)
const closeModal = () =>
  js(`const b=document.querySelector('dialog button[aria-label="Close"]'); if(b) b.click(); return true`)

await ready()
await js('try{localStorage.clear()}catch{};return true')
await send('Page.navigate', { url: APP_URL })
await ready()
await selectBoard()

// --- b: modal progress matches SERVER fields exactly -------------------------
{
  const opened = await openModal(M.id)
  const visible = await waitJs(`return !!document.querySelector('dialog [data-progress]')`, 80)
  const srv = await serverFields(M.id)
  const dom = await domProgress()
  const wantLabel = `${srv.closed} of ${srv.total} closed (${Math.round((srv.closed / srv.total) * 100)}%)`
  opened && visible && dom && dom.label === wantLabel && dom.now === Math.round((srv.closed / srv.total) * 100)
    ? pass(`b: modal progress matches server fields (${wantLabel})`)
    : fail('b server-fields match', `srv=${JSON.stringify(srv)} dom=${JSON.stringify(dom)} want=${wantLabel}`)
}

// --- c: close another child via API while modal open -> live SSE update -----
{
  await req('PATCH', `/api/items/${T1.id}`, { closed: true, agent: AGENT })
  const updated = await waitJs(`const el=document.querySelector('dialog [data-progress] span.text-\\\\[11px\\\\]');return !!el && el.textContent.includes('3 of')`, 80)
  const srv = await serverFields(M.id)
  const dom = await domProgress()
  const wantLabel = `${srv.closed} of ${srv.total} closed (${Math.round((srv.closed / srv.total) * 100)}%)`
  updated && dom && dom.label === wantLabel
    ? pass(`c: live SSE update still matches server fields (${wantLabel})`)
    : fail('c live update', `updated=${updated} srv=${JSON.stringify(srv)} dom=${JSON.stringify(dom)}`)
}

// --- d: childless task shows no progress display -----------------------------
{
  await closeModal()
  await waitJs(`return !document.querySelector('dialog[open]')`, 40)
  const opened = await openModal(T9.id)
  await waitJs(`return !!document.querySelector('dialog[open]')`, 80)
  const gone = await waitJs(`return !document.querySelector('dialog [data-progress]')`, 40)
  opened && gone
    ? pass('d: childless task shows no progress display')
    : fail('d hidden for leaf', `opened=${opened} gone=${gone}`)
  await closeModal()
  await waitJs(`return !document.querySelector('dialog[open]')`, 40)
}

// --- e: per-child epic mini-bar; leaf child has none -------------------------
{
  const opened = await openModal(M.id)
  const epicBar = await waitJs(`const li=document.querySelector('dialog li[data-child="${E.id}"]');return !!li && !!li.querySelector('[role=progressbar]')`, 80)
  const leafBar = await js(`const li=document.querySelector('dialog li[data-child="${T2.id}"]');return !!li && !!li.querySelector('[role=progressbar]')`)
  const now = await js(`const li=document.querySelector('dialog li[data-child="${E.id}"] [role=progressbar]');return li ? Number(li.getAttribute('aria-valuenow')) : null`)
  opened && epicBar && !leafBar && now === 50
    ? pass('e: epic child row shows mini progressbar (50%), leaf child does not')
    : fail('e mini-bars', `opened=${opened} epicBar=${epicBar} leafBar=${leafBar} now=${now}`)
  await closeModal()
  await waitJs(`return !document.querySelector('dialog[open]')`, 40)
}

// --- f: fresh mount after the live update still matches server fields -------
{
  const opened = await openModal(M.id)
  const visible = await waitJs(`return !!document.querySelector('dialog [data-progress]')`, 80)
  const srv = await serverFields(M.id)
  const dom = await domProgress()
  const wantLabel = `${srv.closed} of ${srv.total} closed (${Math.round((srv.closed / srv.total) * 100)}%)`
  opened && visible && dom && dom.label === wantLabel
    ? pass(`f: fresh mount still matches server fields (${wantLabel})`)
    : fail('f fresh mount', `srv=${JSON.stringify(srv)} dom=${JSON.stringify(dom)}`)
  await closeModal()
}

// --- cleanup -----------------------------------------------------------------
for (const it of [T9, E1, E2, T5, T4, T3, T2, T1, E, M]) {
  await req('PATCH', `/api/items/${it.id}`, { closed: true, agent: AGENT }).catch(() => {})
  await req('POST', `/api/items/${it.id}/archive`, { agent: AGENT }).catch(() => {})
}
console.log(`cleaned up pb88e2e items on board ${board.id}`)

console.log('\n=== page exceptions ===')
console.log(exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)')
console.log('\n=== SUMMARY ===')
const failed = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - failed}/${results.length} passed`)
try { cdp.close() } catch { /* already closed */ }
process.exit(failed ? 1 : 0)
