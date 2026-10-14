// PB-62 regression: a parent item's detail modal must list its CHILD items
// with their status (column name), live-updating over SSE while the modal is
// open. Self-seeding like the other fe2-* live scripts: fresh workspace/board
// via the real REST API, then the live app through CDP (isolated stack by
// default — see lib/harness.mjs).
//
// Flow covered:
//   a. seed milestone M + epic E under M + task children T1 (todo col),
//      T2 (done col), T3 (closed) + a childless task T4
//   b. open E's modal -> Children section lists 3 rows; T2 shows the "done"
//      status pill (emerald), T3 is struck through (closed)
//   c. REST-move T1 to the done column while the modal is open -> its status
//      flips live (SSE item.moved -> itemsVersion -> modal refetch)
//   d. REST-create a new child T5 under E while the modal is open -> the row
//      appears live (SSE item.created)
//   e. REST-close T2 while the modal is open -> it strikes through live
//   f. T4 (a task) shows NO Children section — tasks can never be parents
//   g. M's modal lists E as a child with its Epic kind chip
//
// Cleanup: every pb62e2e-* item created here is closed + archived via the API
// (the workspace/board stay, per the fe2-seed convention).
import { setTimeout as sleep } from 'node:timers/promises'
import {
  APP_URL, req, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const AGENT = 'pb62-e2e'

const { results, pass, fail } = checks()

// --- seed a fresh workspace/board + hierarchy via the real API -------------
const { ws, board, columns } = await seedWorkspace({ name: `pb62-ws-${TS}`, agent: AGENT })
const [TODO, , DONE] = columns.map((c) => c.id)
console.error(`[t] seeded workspace=${ws.id} board=${board.id}`)

const mk = async (title, kind, parent, column, extra = {}) => {
  const { item } = await req('POST', `/api/boards/${board.id}/items`, {
    title, kind, column_id: column, ...(parent ? { parent_item_id: parent } : {}), ...extra, agent: AGENT,
  })
  return item
}
const M = await mk(`pb62e2e-m-${TS}`, 'milestone', null, TODO)
const E = await mk(`pb62e2e-epic-${TS}`, 'epic', M.id, TODO)
const T1 = await mk('child-one', 'task', E.id, TODO)
const T2 = await mk('child-two', 'task', E.id, DONE)
const T3 = await mk('child-closed', 'task', E.id, TODO)
await req('PATCH', `/api/items/${T3.id}`, { closed: true, agent: AGENT })
const T4 = await mk('childless-task', 'task', null, TODO)

// --- CDP plumbing -----------------------------------------------------------
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
  return waitJs(`return !!document.querySelector('article[data-item="${E.id}"]')`, 150)
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

// --- b: epic modal lists children with status -------------------------------
{
  const opened = await openModal(E.id)
  // The section renders for epics before the detail fetch lands; wait for the
  // three rows themselves so the assertions below never race the fetch.
  const rows3 = await waitJs(`return document.querySelectorAll('dialog li[data-child]').length === 3`, 80)
  if (!opened || !rows3) {
    fail('b modal children section', `opened=${opened} rows3=${rows3}`)
  } else {
    const head = await js(`return document.querySelector('dialog section[data-children] h3').textContent.trim()`)
    const rows = await js(`return Array.from(document.querySelectorAll('dialog li[data-child]')).map(li=>({
      id: li.getAttribute('data-child'),
      status: (li.textContent.match(/\\b(todo|done)\\b/)||['?'])[0],
      struck: !!li.querySelector('.truncate.line-through'),
      emerald: !!li.querySelector('span.border-emerald-200'),
    }))`)
    const order = rows.map((r) => r.id).join(',')
    const want = [T1.id, T2.id, T3.id].join(',')
    head === 'Children (3)' && order === want
      ? pass(`b1: epic modal lists Children (3), created_at order (${order})`)
      : fail('b1 children list', `head=${JSON.stringify(head)} order=${order} want=${want}`)
    const t2 = rows.find((r) => r.id === T2.id)
    t2?.status === 'done' && t2?.emerald
      ? pass('b2: done-column child shows the "done" status pill (emerald)')
      : fail('b2 done status pill', JSON.stringify(t2))
    const t3 = rows.find((r) => r.id === T3.id)
    t3?.struck
      ? pass('b3: closed child is struck through')
      : fail('b3 closed strike', JSON.stringify(t3))
  }
}

// --- c: REST-move a child while the modal is open -> status flips live ------
{
  await req('POST', `/api/items/${T1.id}/move`, { column_id: DONE, agent: AGENT })
  const flipped = await waitJs(`const li=document.querySelector('dialog li[data-child="${T1.id}"]');return !!li && li.textContent.includes('done')`, 80)
  flipped
    ? pass('c: REST move of a child flips its status in the open modal live (SSE)')
    : fail('c live status flip')
}

// --- d: REST-create a child while the modal is open -> row appears live -----
let T5
{
  T5 = await mk('child-live-created', 'task', E.id, TODO)
  const appeared = await waitJs(`return !!document.querySelector('dialog li[data-child="${T5.id}"]')`, 80)
  const head = await js(`return document.querySelector('dialog section[data-children] h3').textContent.trim()`)
  appeared && head === 'Children (4)'
    ? pass('d: REST-created child appears in the open modal live, count 3 -> 4')
    : fail('d live child create', `appeared=${appeared} head=${JSON.stringify(head)}`)
}

// --- e: REST-close a child while the modal is open -> strike-through live ---
{
  await req('PATCH', `/api/items/${T2.id}`, { closed: true, agent: AGENT })
  const struck = await waitJs(`const li=document.querySelector('dialog li[data-child="${T2.id}"]');return !!li && !!li.querySelector('.truncate.line-through')`, 80)
  struck
    ? pass('e: REST close of a child strikes it through in the open modal live')
    : fail('e live close')
}
await closeModal()
await waitJs(`return !document.querySelector('dialog[open]')`, 40)

// --- f: a task's modal has no Children section ------------------------------
{
  const opened = await openModal(T4.id)
  const modalUp = await waitJs(`return !!document.querySelector('dialog[open]')`, 80)
  const noSection = await js(`return !document.querySelector('dialog section[data-children]')`)
  opened && modalUp && noSection
    ? pass('f: a task item shows no Children section (tasks can never be parents)')
    : fail('f task has no children section', `opened=${opened} modalUp=${modalUp} noSection=${noSection}`)
  await closeModal()
  await waitJs(`return !document.querySelector('dialog[open]')`, 40)
}

// --- g: the milestone sees the epic as a child -------------------------------
{
  const opened = await openModal(M.id)
  const epicRow = await waitJs(`return !!document.querySelector('dialog li[data-child="${E.id}"]')`, 80)
  // The Epic kind chip (violet) — NOT a text match on the row, whose title
  // also contains "epic".
  const chip = await js(`const li=document.querySelector('dialog li[data-child="${E.id}"]');return li ? !!li.querySelector('span.bg-violet-100') : null`)
  opened && epicRow && chip
    ? pass('g: milestone modal lists the epic child with its Epic kind chip')
    : fail('g milestone children', `opened=${opened} epicRow=${epicRow} chip=${chip}`)
  await closeModal()
}

// --- cleanup: close + archive every seeded item ------------------------------
for (const it of [T5, T4, T3, T2, T1, E, M]) {
  await req('PATCH', `/api/items/${it.id}`, { closed: true, agent: AGENT }).catch(() => {})
  await req('POST', `/api/items/${it.id}/archive`, { agent: AGENT }).catch(() => {})
}
console.log(`cleaned up pb62e2e items on board ${board.id}`)

console.log('\n=== page exceptions ===')
console.log(exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)')
console.log('\n=== SUMMARY ===')
const failed = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - failed}/${results.length} passed`)
try { cdp.close() } catch { /* already closed */ }
process.exit(failed ? 1 : 0)
