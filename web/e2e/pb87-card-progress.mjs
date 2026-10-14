// PB-87 smoke: progress bar badge on parent cards (milestone/epic).
//
// Migrated onto the shared PB-81 harness (like pb75): seed/board-selection
// helpers from lib/harness.mjs, and the pb75 exit convention — BOTH CDP
// websocket sessions are closed before process.exit, otherwise node keeps the
// socket alive and the suite never exits (reviewer BLOCK on the pre-harness
// version).
//
// Self-seeding: throwaway workspace + board + hierarchy with the REAL REST
// API, then the live app through CDP (headless chrome, CDP_PORT 9224).
// Defaults to the ISOLATED STACK (PB-81): FE2_API=:18081, APP_URL=:5175 —
// never the live API.
//
// Seed: milestone M, epic E under M, five task children of E (T1/T2 closed),
// a childless milestone M2, and a plain task T4.
//
//   a. epic card E shows a role="progressbar" bar with aria-valuenow=2,
//      aria-valuemin=0, aria-valuemax=5, aria-label "2 of 5 children closed"
//      and visible "2/5" text
//   b. percent text appears ONLY in the tooltip (title "% of direct children
//      closed" phrasing); no bare percent text node on the card
//   c. milestone card M (1 child: E, 0 closed) shows a 0/1 bar
//   d. childless milestone M2 (0/0) and plain task T4 show NO indicator
//   e. live: REST-closing T3 while the board is open updates E's bar to 3/5
//      via SSE (parent re-emit, PB-85 — in mainline since fda5424). This is
//      asserted as a LIVE pass; a reload fallback only proves convergence and
//      is reported as FAIL-live (kept to give a precise diagnostic).
//
// Cleanup: every pb87e2e-* item is closed + archived via the API; the
// workspace/board stay (fe2-seed convention).
import {
  APP_URL, req, seedWorkspace, waitAppReady, selectWorkspaceAndBoard, checks,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'
import { setTimeout as sleep } from 'node:timers/promises'

const TS = Date.now() % 1000000
const AGENT = 'pb87-e2e'
const { pass, fail, summary } = checks()

// --- seed -------------------------------------------------------------------
const { ws } = await seedWorkspace({ name: `pb87-ws-${TS}`, agent: AGENT })
const { board } = (await req('POST', `/api/workspaces/${ws.id}/boards`, { name: `pb87-board-${TS}` }))
const TODO = [...board.columns].sort((a, b) => a.position - b.position)[0].id

const mk = async (title, kind, parent = null) => {
  const { item } = await req('POST', `/api/boards/${board.id}/items`, {
    title, kind, column_id: TODO, ...(parent ? { parent_item_id: parent } : {}), agent: AGENT,
  })
  return item
}
const M = await mk(`pb87e2e-m-${TS}`, 'milestone')
const E = await mk(`pb87e2e-epic-${TS}`, 'epic', M.id)
const M2 = await mk(`pb87e2e-m2-${TS}`, 'milestone')
const T4 = await mk(`pb87e2e-task4-${TS}`, 'task')
const children = []
for (let i = 1; i <= 5; i++) {
  const t = await mk(`pb87e2e-child-${i}-${TS}`, 'task', E.id)
  if (i <= 2) await req('PATCH', `/api/items/${t.id}`, { closed: true, agent: AGENT })
  children.push(t)
}
console.error(`[t] seeded ws=${ws.id} board=${board.id} M=${M.id} E=${E.id}`)

// Sanity: the server fields PB-87 consumes.
{
  const detail = await req('GET', `/api/items/${E.id}`)
  if (detail.item.closed_children !== 2 || detail.item.total_children !== 5) {
    fail('server fields', `closed=${detail.item.closed_children} total=${detail.item.total_children}`)
  } else pass('server fields: epic detail carries closed_children=2 total_children=5')
}

// --- CDP plumbing -----------------------------------------------------------
const exceptions = []
const { ws: cdp, send, onMessage: onCdpMessage } = await connectCdpPage({ url: APP_URL })
onCdpMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
})
const { js, soft, waitJs } = makeEval(send)

if (!(await waitAppReady({ js, soft, waitJs, send }))) fail('app never booted')
if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
  fail('workspace/board selectors never showed our ids')
if (!(await waitJs(`return !!document.querySelector('article[data-item="${E.id}"]')`, 150)))
  fail('board never rendered the epic card')

const cardSel = (id) => `article[data-item="${id}"]`
const barOf = (id) => `return (() => { const c=document.querySelector('${cardSel(id)}'); const b=c&&c.querySelector('[role="progressbar"]'); return b ? { now:b.getAttribute('aria-valuenow'), min:b.getAttribute('aria-valuemin'), max:b.getAttribute('aria-valuemax'), label:b.getAttribute('aria-label'), title:(b.closest('[title]')||b).getAttribute && (b.closest('[title]')||b).getAttribute('title'), text:(b.parentElement.textContent||'').trim() } : null })()`

// --- a/b: epic card bar + tooltip-only percent ------------------------------
{
  if (!(await waitJs(`return !!document.querySelector('${cardSel(E.id)} [role="progressbar"]')`, 80))) {
    fail('a epic bar', 'progressbar never rendered')
  } else {
    const b = await js(barOf(E.id))
    b.now === '2' && b.min === '0' && b.max === '5'
      ? pass('a1: epic bar aria-valuenow=2 valuemin=0 valuemax=5')
      : fail('a1 epic bar values', JSON.stringify(b))
    b.label === '2 of 5 children closed'
      ? pass('a2: aria-label "2 of 5 children closed"')
      : fail('a2 aria-label', JSON.stringify(b.label))
    const countText = /2\s*\/\s*5/.test(b.text)
    countText
      ? pass('a3: visible "2/5" count text')
      : fail('a3 2/5 text', JSON.stringify(b.text))
  }
  const t = await js(`return document.querySelector('${cardSel(E.id)} [title*="% of direct children closed"]')?.getAttribute('title') ?? null`)
  t && t.includes('% of direct children closed')
    ? pass(`b1: tooltip-only percent (${t})`)
    : fail('b1 tooltip', JSON.stringify(t))
  // No bare percent text visible outside the title attribute.
  const hasPercentText = await js(`return /\\d+%/.test(document.querySelector('${cardSel(E.id)}').textContent)`)
  !hasPercentText
    ? pass('b2: no percent in visible card text')
    : fail('b2 visible percent text')
}

// --- c: milestone card 0/1 ---------------------------------------------------
{
  if (!(await waitJs(`return !!document.querySelector('${cardSel(M.id)} [role="progressbar"]')`, 80))) {
    fail('c milestone bar', 'progressbar never rendered')
  } else {
    const b = await js(barOf(M.id))
    b.now === '0' && b.max === '1'
      ? pass('c: milestone card shows 0/1 bar')
      : fail('c milestone bar values', JSON.stringify(b))
  }
}

// --- d: 0/0 and task cards have NO indicator ---------------------------------
{
  const m2 = await js(`return !!document.querySelector('${cardSel(M2.id)} [role="progressbar"]')`)
  const t4 = await js(`return !!document.querySelector('${cardSel(T4.id)} [role="progressbar"]')`)
  !m2 ? pass('d1: childless milestone (0/0) shows no indicator') : fail('d1 0/0 indicator')
  !t4 ? pass('d2: plain task card shows no indicator') : fail('d2 task indicator')
}

// --- e: LIVE update on child close (SSE parent re-emit, PB-85) ---------------
{
  await req('PATCH', `/api/items/${children[2].id}`, { closed: true, agent: AGENT })
  let live = false
  for (let i = 0; i < 20 && !live; i++) {
    const b = await soft(barOf(E.id))
    live = !!b && b.now === '3'
    if (!live) await sleep(250)
  }
  if (live) {
    pass('e: LIVE SSE parent re-emit → epic bar flips to 3/5 without reload')
  } else {
    // Diagnostic fallback only: proves the card renders the fresh server
    // fields after reload, but the LIVE path itself failed.
    await js('location.reload(); return true')
    if (await waitAppReady({ js, soft, waitJs, send })) {
      await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })
      const b = await waitJs(
        `return (document.querySelector('${cardSel(E.id)} [role="progressbar"]')||{getAttribute:()=>null}).getAttribute('aria-valuenow') === '3'`, 100)
      b ? fail('e live SSE flip', 'bar only updated after reload — parent re-emit missing/degraded')
        : fail('e live SSE flip', 'neither live nor after reload')
    } else fail('e live SSE flip', 'app never re-booted for the fallback check')
  }
}

// --- cleanup + summary --------------------------------------------------------
for (const id of [M.id, M2.id, T4.id, E.id, ...children.map((c) => c.id)]) {
  await req('PATCH', `/api/items/${id}`, { closed: true, agent: AGENT }).catch(() => {})
  await req('POST', `/api/items/${id}/archive`, { agent: AGENT }).catch(() => {})
}

const fails = summary()
if (exceptions.length) console.error('page exceptions:', exceptions.slice(0, 3))
console.log(process.exitCode || fails ? 'pb87-card-progress: FAILURES above' : 'pb87-card-progress: all checks passed')
// pb75 exit convention: the websocket session closed before exit, else the
// process never terminates.
try { cdp.close() } catch { /* already dead */ }
await new Promise((r) => setTimeout(r, 100))
process.exit(fails > 0 ? 1 : 0)
