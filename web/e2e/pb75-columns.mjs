// PB-75: drag-and-drop column reordering.
//
// Self-seeding like pb70/pb72: a throwaway workspace + board with FIVE
// uniquely-named columns via the REAL REST API, then the live app through CDP
// (headless chrome, CDP_PORT 9224). Runs against the ISOLATED STACK (PB-68
// convention): FE2_API=:18081, APP_URL=:5175 — never the live API.
//
// Checks:
//   a. REAL DOM drag flow: dragstart on column 3's header handle →
//      dragenter/dragover/drop on column 1's root (DataTransfer carrying
//      'application/x-kanban-column'), i.e. through the actual Svelte DnD
//      handlers — NOT a direct store.moveColumn call. UI order updates.
//   b. API truth: GET /api/boards/{id} shows the new order with DENSE
//      positions 0..n-1 (transactional renumber).
//   c. Second client converges via SSE: a second CDP tab that loaded the
//      board BEFORE the move re-renders the new order from the column.moved
//      event (no reload).
//   d. Keyboard/a11y affordance: the ◀ header button moves a column left via
//      the same API path.
//
// Cleanup convention: the throwaway workspace shell stays (no workspace
// DELETE endpoint), same as fe2-seed/pb67/pb70/pb72.
import {
  APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb75-${TS}`

// --- seed: board with 5 uniquely-named columns ------------------------------

const { ws } = await seedWorkspace({ name: NAME })
const colNames = [`${NAME}-alpha`, `${NAME}-bravo`, `${NAME}-charlie`, `${NAME}-delta`, `${NAME}-echo`]
const board = (await req('POST', `/api/workspaces/${ws.id}/boards`, { name: `${NAME}-dnd-board`, columns: colNames })).board
const columns = board.columns.map((c) => ({ id: c.id ?? c.id, name: c.name }))
for (const col of columns) {
  await req('POST', `/api/boards/${board.id}/items`, { title: `${col.name}-card`, column_id: col.id, agent: 'human' })
}
console.log(`seeded ws=${ws.id} board=${board.id} columns=${columns.length}`)

const apiOrder = async () =>
  (await req('GET', `/api/boards/${board.id}`)).columns.map((c) => c.name)
const shortName = (n) => n.replace(`${NAME}-`, '')

// --- client A: the drag surface ----------------------------------------------

const page = await connectCdpPage({ url: APP_URL })
page.onMessage((m) => {
  if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error')
    console.error('page console error:', JSON.stringify(m.params.args?.map((a) => a.value ?? a.description)))
})
const { js, soft, waitJs } = makeEval(page.send)

if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
  fail('workspace/board selectors never showed our ids')
if (!(await waitJs(`return document.querySelectorAll('[data-column]').length === 5`, 200)))
  fail('board never rendered 5 columns')

// DOM order of rendered columns (data-column carries the column NAME).
const domOrder = () => js(`
  return [...document.querySelectorAll('[data-column]')].map((el) => el.getAttribute('data-column'))
`)

// --- client B: second viewer loaded BEFORE the move (SSE convergence) --------

const tabB = await (await fetch(`http://127.0.0.1:${(process.env.CDP_PORT || '9224')}/json/new?${encodeURIComponent(APP_URL)}`, { method: 'PUT' })).json()
const wsB = new WebSocket(tabB.webSocketDebuggerUrl)
let bid = 0
const pendB = new Map()
wsB.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data)
  if (m.id && pendB.has(m.id)) { pendB.get(m.id)(m); pendB.delete(m.id) }
})
const sendB = (method, params = {}) => new Promise((resolve) => {
  const id = ++bid
  pendB.set(id, resolve)
  wsB.send(JSON.stringify({ id, method, params }))
})
await new Promise((r) => wsB.addEventListener('open', r, { once: true }))
await sendB('Runtime.enable')
const { js: jsB, soft: softB, waitJs: waitJsB } = makeEval(sendB)

if (!(await waitAppReady({ js: jsB, soft: softB, waitJs: waitJsB }))) fail('client B never booted')
if (!(await selectWorkspaceAndBoard({ js: jsB, waitJs: waitJsB, wsId: ws.id, boardId: board.id })))
  fail('client B never reached the board')
if (!(await waitJsB(`return document.querySelectorAll('[data-column]').length === 5`, 200)))
  fail('client B never rendered 5 columns')
console.log('both clients on the board (pre-move order confirmed)')

// --- a. REAL drag: column 3 (charlie) → before column 1 (alpha) --------------

await js(`
  const src = document.querySelector('[data-column="${colNames[2]}"] header')
  const dst = document.querySelector('[data-column="${colNames[0]}"]')
  if (!src?.draggable) throw new Error('header is not draggable')
  const dt = new DataTransfer()
  // Full HTML5 drag flow through the real handlers: dragstart on the handle,
  // then enter/over (pointer in the LEFT half → drop side 'before').
  src.dispatchEvent(new DragEvent('dragstart', { bubbles: true, dataTransfer: dt }))
  dst.dispatchEvent(new DragEvent('dragenter', { bubbles: true, dataTransfer: dt, clientX: dst.getBoundingClientRect().left + 2 }))
  dst.dispatchEvent(new DragEvent('dragover', { bubbles: true, dataTransfer: dt, clientX: dst.getBoundingClientRect().left + 2 }))
  // Svelte flushes DOM updates asynchronously — persist the DataTransfer so
  // the follow-up evals can reuse it for the drop.
  window.__pb75dt = dt
  return true
`)
if (!(await waitJs(`return !!document.querySelector('[data-column="${colNames[0]}"] .bg-indigo-500')`)))
  fail('drop indicator did not render during column dragover')
else console.log('PASS drop indicator rendered on dragover (left edge of target column)')
await js(`
  const dst = document.querySelector('[data-column="${colNames[0]}"]')
  dst.dispatchEvent(new DragEvent('drop', { bubbles: true, dataTransfer: window.__pb75dt, clientX: dst.getBoundingClientRect().left + 2 }))
  return true
`)

const want = [colNames[2], colNames[0], colNames[1], colNames[3], colNames[4]]
const wantShort = want.map((n) => shortName(n))
{
  const ok = await waitJs(`return JSON.stringify([...document.querySelectorAll('[data-column]')].map(el => el.getAttribute('data-column'))) === JSON.stringify(${JSON.stringify(want)})`)
  const got = (await domOrder()).map((n) => shortName(n))
  if (ok && JSON.stringify(got) === JSON.stringify(wantShort)) {
    console.log('PASS real drag flow reordered column 3 → position 0 in the UI')
  } else {
    fail(`UI order after drag = [${got.join(', ')}], want [${wantShort.join(', ')}]`)
  }
}

// --- b. API truth + dense renumber --------------------------------------------

{
  const order = await apiOrder()
  if (JSON.stringify(order) === JSON.stringify(want)) {
    console.log('PASS API column order matches the drag (column.moved persisted)')
  } else {
    fail(`API order = [${order.map(shortName).join(', ')}], want [${want.map(shortName).join(', ')}]`)
  }
  const snap = (await req('GET', `/api/boards/${board.id}`)).columns
  const dense = snap.every((c, i) => c.position === i)
  if (dense) console.log('PASS API positions are dense 0..n-1 after the move')
  else fail(`API positions not dense: ${snap.map((c) => c.position).join(',')}`)
}

// --- c. second client converges via SSE (no reload) ---------------------------

{
  const ok = await waitJsB(`return JSON.stringify([...document.querySelectorAll('[data-column]')].map(el => el.getAttribute('data-column'))) === JSON.stringify(${JSON.stringify(want)})`)
  const gotB = (await jsB(`return [...document.querySelectorAll('[data-column]')].map(el => el.getAttribute('data-column'))`)).map(shortName)
  if (ok && JSON.stringify(gotB) === JSON.stringify(want.map(shortName))) {
    console.log('PASS second client converged via SSE column.moved (no reload)')
  } else {
    fail(`client B order = [${gotB.map(shortName).join(', ')}] (SSE convergence failed)`)
  }
}

// --- d. keyboard/a11y affordance: ◀ button moves a column left -----------------

await js(`
  const header = document.querySelector('[data-column="${colNames[3]}"] header')
  const left = [...header.querySelectorAll('button')].find(b => b.getAttribute('aria-label')?.includes('left'))
  if (!left) throw new Error('move-left button not found')
  if (left.disabled) throw new Error('move-left button unexpectedly disabled for a mid-board column')
  left.click()
  return true
`)
// delta was at index 3 → ◀ moves it to index 2
if (await waitJs(`
  const cols = [...document.querySelectorAll('[data-column]')].map(el => el.getAttribute('data-column'))
  return cols[2] === '${colNames[3]}' && cols[3] === '${colNames[1]}'
`)) {
  const order = await apiOrder()
  if (order[2] === colNames[3]) console.log('PASS ◀ button moved the column left (UI + API)')
  else fail(`◀ moved the UI but API order is [${order.map(shortName).join(', ')}]`)
} else {
  fail('◀ button did not move the column left in the UI')
}

console.log(process.exitCode ? 'pb75-columns: FAILURES above' : 'pb75-columns: all checks passed')
// Close BOTH websocket sessions — leaving page.ws open hangs the process
// (node keeps the socket alive; reviewer hit exit 124 on CI-style runs).
try { wsB.close() } catch { /* already dead */ }
try { page.ws.close() } catch { /* already dead */ }
await new Promise((r) => setTimeout(r, 100))
process.exit(process.exitCode ?? 0)
