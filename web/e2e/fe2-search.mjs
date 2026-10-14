// PB-61: e2e coverage for the frontend search box over the v1 search query
// language (docs/search-query.md).
//
// Self-seeding like the other fe2-* live scripts: a DEDICATED workspace/board
// via the REAL REST API, with items exercising labels (bug/feat/wontfix),
// ages (created_at backdated via psql, like fe2-cdp-a reads the DB), kinds,
// priorities, closed states and owners. Drives the live app through CDP
// against the live app (isolated stack by default — see lib/harness.mjs).
//
// Flow covered:
//   a. epic's example query: CREATED < -1d AND LABEL IN ('bug') -> exact card
//      set, per-column visible counts reflect the filtered set
//   b. PRIORITY / CLOSED / NOT: PRIORITY <= 1 AND NOT CLOSED = true
//   c. KIND IN / OWNER = (exact, case-insensitive)
//   d. NOT / AND / OR grouping via parens, LABEL != set semantics
//   e. valid query matching nothing (KIND = 'story') -> empty board, no error
//   f. invalid query -> data-search-error shows the §6 message verbatim,
//      board stays intact
//   g. clear -> the lazy (non-search) flow is restored: cards return AND
//      per-column /items page requests resume
//   h. while a query is active the UI issues NO /items requests (snapshot-
//      only searched view), observed via the CDP Network domain
//
// Cleanup: every s61-* item is archived + closed via the API; the
// workspace/board follow the fe2-seed convention and stay for inspection.
import { setTimeout as sleep } from 'node:timers/promises'
import {
  APP_URL, req, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, makeSql,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const PREFIX = `s61-${TS}`

const { results, pass, fail } = checks()

// --- seed a fresh workspace/board/labels/items via the real API -------------
const { ws, board, columns } = await seedWorkspace({ name: `s61-ws-${TS}`, agent: 'eng-e2e' })
const COL1 = columns[0].id
const COL3 = columns[columns.length - 1].id
// Fresh workspace: the literal names bug/feat/wontfix are free and the query
// language matches label NAMES (§5.4), so keep them unprefixed.
const label = async (name) => (await req('POST', `/api/workspaces/${ws.id}/labels`, { name, agent: 'eng-e2e' })).label.id
const L = { bug: await label('bug'), feat: await label('feat'), wontfix: await label('wontfix') }

const mk = async (title, extra = {}) =>
  (await req('POST', `/api/boards/${board.id}/items`, { title, column_id: COL1, agent: 'eng-e2e', ...extra })).item
const itemBugOld = await mk(`${PREFIX}-bug-old`, { label_ids: [L.bug], priority: 0, owner_agent: 'agent-x' })
const itemFeatNew = await mk(`${PREFIX}-feat-new`, { label_ids: [L.feat], priority: 3 })
const itemEpic = await mk(`${PREFIX}-epic`, { kind: 'epic', priority: 1, owner_agent: 'agent-y' })
const itemWontfix = await mk(`${PREFIX}-wontfix`, { label_ids: [L.wontfix], priority: 2, owner_agent: 'agent-y' })
const itemBugClosed = await mk(`${PREFIX}-bug-closed`, { label_ids: [L.bug], priority: 2, owner_agent: 'agent-x', column_id: COL3 })
await req('PATCH', `/api/items/${itemBugClosed.id}`, { closed: true, agent: 'eng-e2e' })

// Age-dependent tests need an old created_at: backdate directly in the DB
// (the shared psql helper — the same path fe2-cdp-a/fe2-moves observe with).
const { sql } = makeSql()
sql(`update items set created_at = now() - interval '3 days', updated_at = now() - interval '3 days' where id in ('${itemBugOld.id}', '${itemBugClosed.id}')`)
const aged = sql(`select count(*) from items where id in ('${itemBugOld.id}', '${itemBugClosed.id}') and created_at < now() - interval '2 days'`)
if (aged !== '2') throw new Error(`backdate failed: ${aged}`)
console.error(`[t] seeded workspace=${ws.id} board=${board.id} items=5 (2 backdated -3d)`)

const ALL = [itemBugOld, itemFeatNew, itemEpic, itemWontfix, itemBugClosed]
const NAMES = { [itemBugOld.id]: 'bug-old', [itemFeatNew.id]: 'feat-new', [itemEpic.id]: 'epic', [itemWontfix.id]: 'wontfix', [itemBugClosed.id]: 'bug-closed' }

// --- CDP plumbing ------------------------------------------------------------
console.error('[t] connecting cdp')
const exceptions = []
const netUrls = []
const { ws: cdp, send, onMessage } = await connectCdpPage({ url: APP_URL })
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
  if (m.method === 'Network.requestWillBeSent') netUrls.push(m.params.request.url)
})
await send('Network.enable')
const { js, soft, waitJs } = makeEval(send)

const cardSel = (id) => `article[data-item="${id}"]`
const hasCard = (id) => js(`return !!document.querySelector('${cardSel(id)}')`)
const waitCards = async (presentIds, absentIds, tries = 100) => {
  for (let i = 0; i < tries; i++) {
    const ok = await soft(`return ${presentIds.map((id) => `!!document.querySelector('${cardSel(id)}')`).join(' && ') || 'true'} && ${absentIds.map((id) => `!document.querySelector('${cardSel(id)}')`).join(' && ') || 'true'}`)
    if (ok) return true
    await sleep(150)
  }
  return false
}
// Per-column visible count read straight off the column header badge.
const columnCounts = () => js(`
  return Array.from(document.querySelectorAll('[data-column]')).map(el => ({
    name: el.getAttribute('data-column'),
    n: el.querySelectorAll('article[data-item]').length,
  }))`)

// Gate on the workspace selector actually being populated — not on any
// injected helper (the app defines no window.__ui). Returns false when the
// app never loads so the caller can bail out and still clean up the seed.
async function ready() {
  return waitAppReady({ soft })
}
async function selectBoard() {
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id }))) return false
  return waitJs(`return !!document.querySelector('[data-column="${columns[0].name}"]')`, 150)
}
// Type a query into the search box (debounced 300ms in the store) and wait
// for the searched view to settle around the expected card set.
async function search(query) {
  netUrls.length = 0
  await js(`const i=document.querySelector('input[data-search-input]');i.value=${JSON.stringify(query)};i.dispatchEvent(new Event('input',{bubbles:true}));return true`)
}
const expectCards = async (ids, label) => {
  const ok = await waitCards(ids, ALL.map((i) => i.id).filter((id) => !ids.includes(id)))
  ok ? pass(label) : fail(label, `visible=${JSON.stringify(await Promise.all(ALL.map(async (i) => NAMES[i.id] + ':' + (await hasCard(i.id)))))}`)
}

console.error('[t] first ready()')
// Cleanup runs on every exit path (including an app that never loads), so no
// seed item is ever left behind on the live board.
const cleanup = async () => {
  for (const it of ALL) {
    await req('PATCH', `/api/items/${it.id}`, { closed: true, agent: 'eng-e2e' }).catch(() => {})
    await req('POST', `/api/items/${it.id}/archive`, { agent: 'eng-e2e' }).catch(() => {})
  }
}
// Any uncaught failure (e.g. a wedged CDP page mid-flow) must still clean up
// the seed workspace's items before the suite dies.
process.on('uncaughtException', async (e) => {
  console.error('UNCAUGHT', e)
  await cleanup()
  process.exit(1)
})
if (!(await ready())) {
  fail('app-load: vite/API page never became interactive; cleaning up seed only')
  await cleanup()
  process.exit(1)
}
await js('try{localStorage.clear()}catch{};return true')
await send('Page.navigate', { url: APP_URL })
await ready()
await selectBoard()
const allVisible = await waitCards(ALL.map((i) => i.id), [])
if (!allVisible) {
  fail('seed render: items never appeared on the board; cleaning up seed')
  await cleanup()
  process.exit(1)
}
// Let the initial lazy flow (3 column pages + relation hydration) finish so
// the network snapshot below only observes search-driven traffic.
await sleep(1500)

// --- a: the epic's example query ---------------------------------------------
await search(`CREATED < -1d AND LABEL IN ('bug')`)
await expectCards([itemBugOld.id, itemBugClosed.id], "a: CREATED < -1d AND LABEL IN ('bug') matches exactly the two backdated bug items")
{
  const counts = await columnCounts()
  const todo = counts.find((c) => c.name === columns[0].name)
  const done = counts.find((c) => c.name === columns[columns.length - 1].name)
  todo?.n === 1 && done?.n === 1
    ? pass(`a2: per-column counts reflect the filtered set (todo=1, done=1; got ${todo?.n}/${done?.n})`)
    : fail('a2 column counts', JSON.stringify(counts))
}

// --- h: snapshot-only while searching (no /items requests) --------------------
{
  const itemPageReqs = netUrls.filter((u) => /\/api\/boards\/[0-9a-f-]+\/items\?/.test(u))
  const qReqs = netUrls.filter((u) => /[?&]q=/.test(u))
  itemPageReqs.length === 0 && qReqs.length > 0
    ? pass(`h1: applying the query fetched the snapshot (q=...) and issued NO per-column /items requests`)
    : fail('h1 snapshot-only search', `items=${itemPageReqs.length} q=${qReqs.length} urls=${JSON.stringify(netUrls.slice(0, 8))}`)
}

// --- b: PRIORITY + NOT CLOSED --------------------------------------------------
await search(`PRIORITY <= 1 AND NOT CLOSED = true`)
await expectCards([itemBugOld.id, itemEpic.id], 'b: PRIORITY <= 1 AND NOT CLOSED = true -> open P0/P1 only')

// --- c: KIND IN + OWNER = (case-insensitive exact) -----------------------------
await search(`KIND IN ('epic', 'milestone')`)
await expectCards([itemEpic.id], "c1: KIND IN ('epic','milestone') -> the epic")
await search(`OWNER = 'AGENT-X'`)
await expectCards([itemBugOld.id, itemBugClosed.id], "c2: OWNER = 'AGENT-X' is case-insensitive exact -> both agent-x items")

// --- d: parens grouping + LABEL set semantics -----------------------------------
await search(`(KIND = 'epic' OR LABEL = 'wontfix') AND OWNER != ''`)
await expectCards([itemEpic.id, itemWontfix.id], "d1: (KIND='epic' OR LABEL='wontfix') AND OWNER != '' -> epic + wontfix")
await search(`NOT (LABEL = 'bug' OR LABEL = 'feat' OR LABEL = 'wontfix')`)
await expectCards([itemEpic.id], 'd2: NOT(...) -> only the unlabeled epic')

// --- e: valid query, empty result ------------------------------------------------
{
  await search(`CREATED < -1d AND LABEL IN ('bug')`)
  await waitCards([itemBugOld.id, itemBugClosed.id], [])
  await search(`KIND = 'story'`)
  const ok = await waitCards([], ALL.map((i) => i.id))
  const err = await js(`const e=document.querySelector('[data-search-error]');return e?e.textContent.trim():''`)
  ok && err === ''
    ? pass("e: KIND = 'story' is valid, matches nothing, no cards, no error")
    : fail('e empty result', `empty=${ok} err=${JSON.stringify(err)}`)
}

// --- f: invalid query -> §6 message verbatim, board intact ------------------------
// Run this with cards visible (the lazy flow was just restored by g): a 400
// must keep the previously rendered board instead of blanking it.
{
  // First restore the full card set (the lazy flow re-renders everything;
  // g below re-verifies the /items resumption), then fire the bad query: a
  // 400 must keep the rendered board intact.
  await js(`const i=document.querySelector('input[data-search-input]');i.value='';i.dispatchEvent(new Event('input',{bubbles:true}));return true`)
  await waitCards(ALL.map((i) => i.id), [])
  await search(`created < -1d`)
  const shown = await waitJs(`return !!document.querySelector('[data-search-error]')`, 60)
  const err = await js(`const e=document.querySelector('[data-search-error]');return e?e.textContent.trim():''`)
  const expected = `parse error at 1:1 (offset 0): unexpected input 'created'`
  const intact = await hasCard(itemBugOld.id) && await hasCard(itemEpic.id)
  shown && err === expected && intact
    ? pass('f: invalid query shows the §6 parse-error message verbatim and the board stays intact')
    : fail('f invalid query error', `shown=${shown} err=${JSON.stringify(err)} intact=${intact}`)
}

// --- g: clear restores the lazy flow (cards back + /items requests resume) -------
{
  netUrls.length = 0
  await js(`const i=document.querySelector('input[data-search-input]');i.value='';i.dispatchEvent(new Event('input',{bubbles:true}));return true`)
  const back = await waitCards(ALL.map((i) => i.id), [])
  // A clear re-enters the lazy path: give the page a beat to issue its
  // per-column page requests, then look at what was observed.
  await sleep(1200)
  const itemPageReqs = netUrls.filter((u) => /\/api\/boards\/[0-9a-f-]+\/items\?/.test(u))
  back && itemPageReqs.length > 0
    ? pass(`g: clearing the search restores all cards AND the lazy per-column /items flow (${itemPageReqs.length} page requests)`)
    : fail('g clear restores lazy flow', `back=${back} itemReqs=${itemPageReqs.length}`)
}

// --- cleanup: archive + close every s61- item so no residue is left --------------
for (const it of ALL) {
  await req('PATCH', `/api/items/${it.id}`, { closed: true, agent: 'eng-e2e' }).catch(() => {})
  await req('POST', `/api/items/${it.id}/archive`, { agent: 'eng-e2e' }).catch(() => {})
}
console.log(`cleaned up s61- items on board ${board.id} (workspace ${ws.id} stays for inspection)`)

console.log('\n=== page exceptions ===')
console.log(exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)')
console.log('\n=== SUMMARY ===')
const failed = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - failed}/${results.length} passed`)
try { cdp.close() } catch { /* already closed */ }
process.exit(failed ? 1 : 0)
