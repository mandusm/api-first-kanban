// KB-21 regression: an archived item must not render on the board while
// "Show archived" is off, and must render (dimmed, badged) when it is on.
//
// Self-seeding, like the other fe2-* live scripts: creates a fresh
// workspace/board via the REAL REST API so per-title assertions are not
// confused by items left over from earlier runs. Drives the live app through
// CDP against the live app (isolated stack by default — see lib/harness.mjs).
//
// Flow covered (KB-21 verification a–f):
//   a. create item A via REST
//   b. it renders on the board (SSE / snapshot)
//   c. archive A THROUGH THE DETAIL MODAL -> card disappears
//   d. hard reload -> still gone (snapshot excludes archived)
//   e. create B via REST (live card), archive B via API only -> live SSE removal
//   f. "Show archived" on -> both render dimmed + archived badge; off -> gone
//
// Cleanup: every kb21-* item created here is archived + closed via the API so
// the board is left clean (the workspace/board themselves follow the fe2-seed
// convention and stay for inspection).
import {
  APP_URL, req, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const PREFIX = `kb21-${TS}`
const TITLE_A = `${PREFIX}-modal`
const TITLE_B = `${PREFIX}-live`

const { results, pass, fail } = checks()

// --- seed a fresh workspace/board via the real API --------------------------
const { ws, board, columns } = await seedWorkspace({ name: `kb21-ws-${TS}`, agent: 'worker-arch-bug' })
if (columns.length < 1) throw new Error('expected >=1 default column')
const COL1 = columns[0].id
console.error(`[t] seeded workspace=${ws.id} board=${board.id}`)

// --- CDP plumbing (shared, with stale-chrome self-heal) ---------------------
console.error('[t] connecting cdp')
const exceptions = []
const { ws: cdp, send, onMessage } = await connectCdpPage({ url: APP_URL })
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
})
const { js, soft, waitJs } = makeEval(send)

const HELPERS = `window.__ui={card:(id)=>document.querySelector('article[data-item="'+id+'"]'),titleOf:(a)=>((a.querySelector('h3')||{}).textContent||'').trim()};return true`
const hasCard = (id) => js(`return !!document.querySelector('article[data-item="${id}"]')`)
const waitCard = (id, present, tries = 80) =>
  waitJs(`return ${present ? '!!' : '!'}document.querySelector('article[data-item="${id}"]')`, tries)

async function ready() {
  return waitAppReady({ soft, inject: HELPERS })
}

async function selectBoard() {
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id }))) return false
  return waitJs(`return !!document.querySelector('[data-column="${columns[0].name}"]')`, 150)
}

async function hardReload() {
  await send('Page.navigate', { url: APP_URL })
  await ready()
  return selectBoard()
}

console.error('[t] first ready()')
await ready()
// A previous live session may have persisted "Show archived" locally; clear it
// so this run starts from the documented default (off) before checking.
await js('try{localStorage.clear()}catch{};return true')
await hardReload()
await waitJs(`const e=document.querySelector('input[data-show-archived]');return e && e.checked===false`, 60)

// --- a + b: create item A via REST, verify it renders -----------------------
let A
{
  const { item } = await req('POST', `/api/boards/${board.id}/items`, { title: TITLE_A, column_id: COL1, agent: 'worker-arch-bug' })
  A = item.id
  const shown = await waitCard(A, true)
  const title = await js(`const a=document.querySelector('article[data-item="${A}"]');return a?((a.querySelector('h3')||{}).textContent||'').trim():''`)
  shown && title === TITLE_A
    ? pass(`a+b: REST-created "${TITLE_A}" renders on the board`)
    : fail('a+b create/render', `shown=${shown} title=${JSON.stringify(title)}`)
}

// --- c: archive A through the detail modal -> card disappears ---------------
{
  const opened = await js(`const b=document.querySelector('article[data-item="${A}"] h3 button'); if(!b) return false; b.click(); return true`)
  const modalReady = await waitJs(`return !!document.querySelector('button[title="Hide from the board"]')`, 80)
  if (!opened || !modalReady) fail('c modal open', `opened=${opened} modalReady=${modalReady}`)
  const clicked = await js(`const b=document.querySelector('button[title="Hide from the board"]'); if(!b) return false; b.click(); return true`)
  const gone = await waitCard(A, false)
  const dbArchived = (await req('GET', `/api/items/${A}`)).item.archived_at
  clicked && gone && dbArchived != null
    ? pass('c: modal Archive removes the card from the board (DB archived_at set)')
    : fail('c modal archive', `clicked=${clicked} gone=${gone} archived_at=${dbArchived}`)
  // close the modal so later assertions see the plain board
  await js(`const b=document.querySelector('button[aria-label="Close"]'); if(b) b.click(); return true`)
  await waitJs(`return !document.querySelector('button[title="Restore to the board"]')`, 40)
}

// --- d: hard reload -> still gone (snapshot excludes archived) --------------
{
  await hardReload()
  const gone = await waitCard(A, false)
  const inSnapshot = (await req('GET', `/api/boards/${board.id}`)).items.some((i) => i.id === A)
  gone && !inSnapshot
    ? pass('d: after hard reload the archived card is still gone (snapshot excludes archived)')
    : fail('d reload hides archived', `gone=${gone} inSnapshot=${inSnapshot}`)
}

// --- e: create B live, archive B via API -> live SSE removal ----------------
let B
{
  const { item } = await req('POST', `/api/boards/${board.id}/items`, { title: TITLE_B, column_id: COL1, agent: 'worker-arch-bug' })
  B = item.id
  const appeared = await waitCard(B, true)
  await req('POST', `/api/items/${B}/archive`, { agent: 'worker-arch-bug' })
  const removed = await waitCard(B, false)
  const inSnapshot = (await req('GET', `/api/boards/${board.id}`)).items.some((i) => i.id === B)
  appeared && removed && !inSnapshot
    ? pass('e: API archive of a live card removes it without reload (SSE item.archived)')
    : fail('e live SSE archive removal', `appeared=${appeared} removed=${removed} inSnapshot=${inSnapshot}`)
}

// --- f: Show archived on -> both render dimmed+badged; off -> gone ----------
{
  await js(`const c=document.querySelector('input[data-show-archived]'); c.checked=true; c.dispatchEvent(new Event('change',{bubbles:true})); return true`)
  const bothBack = await waitJs(`return !!document.querySelector('article[data-item="${A}"]') && !!document.querySelector('article[data-item="${B}"]')`, 120)
  const detail = await js(`
    const read=(id)=>{const el=document.querySelector('article[data-item="'+id+'"]');if(!el)return null;
      return {dim:el.classList.contains('opacity-40'),badge:/archived/i.test(el.textContent)}};
    return {a:read(${JSON.stringify(A)}),b:read(${JSON.stringify(B)})}`)
  const ok = bothBack && detail?.a?.dim && detail?.a?.badge && detail?.b?.dim && detail?.b?.badge
  ok
    ? pass('f1: "Show archived" on renders both archived cards dimmed with an archived badge')
    : fail('f1 show archived on', `bothBack=${bothBack} detail=${JSON.stringify(detail)}`)
}

{
  await js(`const c=document.querySelector('input[data-show-archived]'); c.checked=false; c.dispatchEvent(new Event('change',{bubbles:true})); return true`)
  const gone = await waitJs(`return !document.querySelector('article[data-item="${A}"]') && !document.querySelector('article[data-item="${B}"]')`, 120)
  gone
    ? pass('f2: "Show archived" off hides both archived cards again')
    : fail('f2 show archived off')
}

// --- cleanup: archive + close every kb21- item so no residue is left --------
for (const id of [A, B]) {
  await req('PATCH', `/api/items/${id}`, { closed: true, agent: 'worker-arch-bug' }).catch(() => {})
  await req('POST', `/api/items/${id}/archive`, { agent: 'worker-arch-bug' }).catch(() => {})
}
console.log(`cleaned up kb21- items on board ${board.id}`)

console.log('\n=== page exceptions ===')
console.log(exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)')
console.log('\n=== SUMMARY ===')
const failed = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - failed}/${results.length} passed`)
try { cdp.close() } catch { /* already closed */ }
process.exit(failed ? 1 : 0)
