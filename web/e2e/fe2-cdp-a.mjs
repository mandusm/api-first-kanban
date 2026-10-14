// fe-2 live-integration harness (part A): M3b mutation UI against the REAL API.
// DOM assertions + Postgres assertions in the same run.
import { setTimeout as sleep } from 'node:timers/promises'
import {
  API, APP_URL, loadSeedEnv, makeSql,
} from './lib/harness.mjs'
import { connectCdpPage, dragCardToColumn, makeEval } from './fe2-cdp.mjs'

const env = loadSeedEnv()
const WS = env.WS
const BOARD = env.BOARD
const COL1 = env.COL1
const COL2 = env.COL2
const LABEL = env.LABEL
const TS = env.TS

// --- Postgres assertions (source of truth) --------------------------------
const { sql, sql1 } = makeSql({ tab: true })

// --- CDP plumbing (shared, with stale-chrome self-heal) ---------------------
let PHASE = 'startup'
const consoleErrors = []
const exceptions = []
const { ws, send, onMessage } = await connectCdpPage({ url: APP_URL })
onMessage((m) => {
  if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
    consoleErrors.push(m.params.args.map((a) => a.value ?? a.description ?? '').join(' '))
  }
  if (m.method === 'Runtime.exceptionThrown') {
    exceptions.push({ phase: PHASE, msg: m.params.exceptionDetails?.exception?.description ?? JSON.stringify(m.params) })
  }
})
const { js, soft, waitJs } = makeEval(send)

const HELPERS = `
window.__ui = {
  col: (name) => document.querySelector('[data-column="' + name + '"]'),
  cards: () => Array.from(document.querySelectorAll('article[data-item]')),
  cardIds: () => Array.from(document.querySelectorAll('[data-column]')).map(c => ({
    column: c.getAttribute('data-column'),
    ids: Array.from(c.querySelectorAll('article[data-item]')).map(a => a.getAttribute('data-item')),
  })),
  titleOf: (a) => ((a.querySelector('h3')||{}).textContent||'').trim(),
  btn: (root, text) => Array.from(root.querySelectorAll('button')).find(b => b.textContent.trim() === text),
  dlg: () => document.querySelector('dialog[open]'),
};
return true`

async function clickEl(expr, tries = 60) {
  for (let i = 0; i < tries; i++) {
    if (await soft(`return !!(${expr})`)) {
      await js(`(${expr}).click(); return true`)
      return true
    }
    await sleep(150)
  }
  return false
}

async function setEl(expr, value, tries = 60) {
  const json = JSON.stringify(value)
  for (let i = 0; i < tries; i++) {
    if (await soft(`return !!(${expr})`)) {
      await js(`const el=${expr}; el.value=${json}; el.dispatchEvent(new Event('input',{bubbles:true})); return true`)
      return true
    }
    await sleep(150)
  }
  return false
}

const results = []

/** Report items in the live store whose `labels` is not an array (would crash Card). */
async function badItems(tag) {
  const out = await js(`
    const m = await import('/src/lib/kanban.svelte.ts');
    const k = m.kanban;
    return { n: k.items.length, bad: k.items.filter(i => !Array.isArray(i.labels)).map(i => ({ title: i.title, keys: Object.keys(i) })) }`).catch((e) => ({ err: String(e) }))
  if (out?.bad?.length) console.log(`  !! bad-items after ${tag}: ${JSON.stringify(out.bad)}`)
  return out
}
const pass = (k) => { results.push(['PASS', k]); console.log('PASS  ' + k) }
const fail = (k, extra = '') => { results.push(['FAIL', k]); console.log(`FAIL  ${k}${extra ? ' — ' + extra : ''}`) }

await send('Page.enable')
await send('Runtime.enable')
await send('Page.navigate', { url: APP_URL })

async function ensureReady() {
  for (let i = 0; i < 150; i++) {
    const ready = await soft(
      `return !!(window.__ui && document.querySelector('#ws-select') && document.querySelectorAll('#ws-select option').length > 0)`,
    )
    if (ready === true) return true
    await soft(HELPERS)
    await sleep(200)
  }
  return false
}
if (!(await ensureReady())) {
  console.log('HARNESS: page never became ready')
  console.log(await js('return document.body.innerText').catch(() => '(no text)'))
  ws.close()
  process.exit(2)
}

// --- explicit selection of MY workspace/board ------------------------------
async function selectValue(sel, value) {
  return await js(`
    const s = document.querySelector(${JSON.stringify(sel)});
    if (!s) return false;
    if (!Array.from(s.options).some(o => o.value === ${JSON.stringify(value)})) return false;
    s.value = ${JSON.stringify(value)};
    s.dispatchEvent(new Event('change', { bubbles: true }));
    return true;`)
}
let okSel = await waitJs(
  `return Array.from((document.querySelector('#ws-select')||{options:[]}).options).some(o => o.value === ${JSON.stringify(WS)})`,
  80,
)
if (okSel) okSel = await selectValue('#ws-select', WS)
// board list loads after workspace change
let okB = false
if (okSel) {
  okB = await waitJs(
    `return Array.from((document.querySelector('#board-select')||{options:[]}).options).some(o => o.value === ${JSON.stringify(BOARD)})`,
    80,
  )
  if (okB) okB = await selectValue('#board-select', BOARD)
}
const boardShown = await waitJs(
  `return !!(window.__ui && window.__ui.col('todo') && document.body.innerText.includes(${JSON.stringify('fe2-board-' + TS)}))`,
  100,
)
okSel && okB && boardShown
  ? pass(`select: workspace/board selected explicitly; board columns render (3 default columns)`)
  : fail('select', `ws=${okSel} board=${okB} rendered=${boardShown}`)

// capture the badge state
const badge = await js(`const e=document.querySelector('main header span.inline-flex'); return e?e.textContent.trim():'(none)'`)
console.log('  connection badge after load:', badge)

// =========================================================================
// 1. ADD ITEM via the per-column form (Todo)
// =========================================================================
PHASE = 'add-item'
const ADD_TITLE = `ui-added-${TS}`
await clickEl(`window.__ui.btn(window.__ui.col('todo'), '+ Add item')`)
await waitJs(`return !!window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, 40)
await setEl(`window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, ADD_TITLE)
await setEl(`window.__ui.col('todo').querySelector('textarea')`, 'added via the real M3b form')
await clickEl(`window.__ui.btn(window.__ui.col('todo'), 'Add')`)
{
  const domOk = await waitJs(
    `return window.__ui.cards().some(a => window.__ui.titleOf(a) === ${JSON.stringify(ADD_TITLE)})`,
    80,
  )
  const dbTitle = sql1(`select title from items where board_id='${BOARD}' and title='${ADD_TITLE}'`)
  const dbCol = sql1(`select column_id from items where board_id='${BOARD}' and title='${ADD_TITLE}'`)
  const ev = sql1(`select count(*) from events where board_id='${BOARD}' and type='item.created' and payload->'item'->>'title'='${ADD_TITLE}'`)
  domOk && dbTitle === ADD_TITLE && dbCol === COL1 && ev === '1'
    ? pass('add item: card in DOM + row in Postgres (todo) + item.created event')
    : fail('add item', `dom=${domOk} title=${dbTitle} col=${dbCol} events=${ev}`)
  await badItems('add')
}
const ADD_ID = sql1(`select id from items where board_id='${BOARD}' and title='${ADD_TITLE}'`)
const CARD = `document.querySelector('[data-item="${ADD_ID}"]')`

// =========================================================================
// 2. EDIT title + description
// =========================================================================
PHASE = 'edit'
await clickEl(`window.__ui.btn(${CARD}, 'edit')`)
await waitJs(`return !!(${CARD})?.querySelector('input')`, 40)
await setEl(`(${CARD}).querySelector('input')`, `ui-edited-${TS}`)
await setEl(`(${CARD}).querySelector('textarea')`, 'now with a description')
await clickEl(`window.__ui.btn(${CARD}, 'Save')`)
{
  const domOk = await waitJs(`return /ui-edited-${TS}/.test((${CARD}?.innerText)||'')`, 80)
  const row = sql1(`select title || '|' || coalesce(description,'') from items where id='${ADD_ID}'`)
  const ev = sql1(`select count(*) from events where payload->'item'->>'id'='${ADD_ID}' and type='item.updated' and payload->'item'->>'description'='now with a description'`)
  domOk && row === `ui-edited-${TS}|now with a description` && ev === '1'
    ? pass('edit: title+description in DOM, persisted, and carried on item.updated event')
    : fail('edit', `dom=${domOk} row=${row} ev=${ev}`)
}

// =========================================================================
// 3. CLOSE then REOPEN
// =========================================================================
PHASE = 'close'
await clickEl(`window.__ui.btn(${CARD}, 'close')`)
{
  const domOk = await waitJs(`return /closed/i.test((${CARD}?.innerText)||'')`, 60)
  const dbClosed = sql1(`select (closed_at is not null) from items where id='${ADD_ID}'`)
  domOk && dbClosed === 't' ? pass('close: badge in DOM + closed_at set in Postgres') : fail('close', `dom=${domOk} db=${dbClosed}`)
}
await clickEl(`window.__ui.btn(${CARD}, 'reopen')`)
{
  const domOk = await waitJs(`return !/closed/i.test((${CARD}?.innerText)||'')`, 60)
  const dbOpen = sql1(`select (closed_at is null) from items where id='${ADD_ID}'`)
  domOk && dbOpen === 't' ? pass('reopen: badge cleared + closed_at NULL in Postgres') : fail('reopen', `dom=${domOk} db=${dbOpen}`)
}

// =========================================================================
// 4. MOVE UP / MOVE DOWN (↑/↓) — add a second card and swap
// =========================================================================
PHASE = 'move-up-down'
const SECOND = `ui-second-${TS}`
{
  // second card goes to the TOP; then use ↓ on it to push it below the first.
  await clickEl(`window.__ui.btn(window.__ui.col('todo'), '+ Add item')`)
  await waitJs(`return !!window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, 40)
  await setEl(`window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, SECOND)
  await clickEl(`window.__ui.btn(window.__ui.col('todo'), 'Add')`)
  await waitJs(`return window.__ui.cards().some(a => window.__ui.titleOf(a) === ${JSON.stringify(SECOND)})`, 80)
}
const orderIn = (col) => sql(`select title from items where board_id='${BOARD}' and column_id='${col}' order by position::numeric, created_at`)
const dbOrder0 = orderIn(COL1).split('\n')
const SECOND_ID = sql1(`select id from items where board_id='${BOARD}' and title='${SECOND}'`)
{
  // ↓ on SECOND (currently top) → should fall below the edited card
  let moved = false
  for (let i = 0; i < 40 && !moved; i++) {
    if (await soft(`return !!(document.querySelector('[data-item="${SECOND_ID}"]'))`)) {
      await js(`const a=document.querySelector('[data-item="${SECOND_ID}"]'); window.__ui.btn(a,'↓').click(); return true`)
      moved = await waitJs(
        `const ids=window.__ui.cardIds().find(c=>c.column==='todo').ids; return ids.indexOf(${JSON.stringify(SECOND_ID)}) > ids.indexOf(${JSON.stringify(ADD_ID)})`,
        20,
      )
    }
    if (!moved) await sleep(150)
  }
  const dbOrder = orderIn(COL1).split('\n')
  const iA = dbOrder.indexOf(`ui-edited-${TS}`)
  const iS = dbOrder.indexOf(SECOND)
  moved && iS > iA && iS >= 0 && iA >= 0
    ? pass('move down: DOM order swapped + Postgres order matches (numeric position)')
    : fail('move down', `dom=${moved} dbOrder=${JSON.stringify(dbOrder)}`)
  // ↑ on SECOND → back on top
  let up = false
  for (let i = 0; i < 40 && !up; i++) {
    if (await soft(`return !!(document.querySelector('[data-item="${SECOND_ID}"]'))`)) {
      await js(`const a=document.querySelector('[data-item="${SECOND_ID}"]'); window.__ui.btn(a,'↑').click(); return true`)
      up = await waitJs(
        `const ids=window.__ui.cardIds().find(c=>c.column==='todo').ids; return ids.indexOf(${JSON.stringify(SECOND_ID)}) < ids.indexOf(${JSON.stringify(ADD_ID)})`,
        20,
      )
    }
    if (!up) await sleep(150)
  }
  const dbOrder2 = orderIn(COL1).split('\n')
  up && dbOrder2.indexOf(SECOND) < dbOrder2.indexOf(`ui-edited-${TS}`)
    ? pass('move up: DOM order swapped back + Postgres order matches')
    : fail('move up', `dom=${up} dbOrder=${JSON.stringify(dbOrder2)}`)
  console.log('  db order before:', JSON.stringify(dbOrder0), '→ after ↓/↑:', JSON.stringify(dbOrder2))
  await badItems('move-up-down')
}

// =========================================================================
// 5. MOVE TO COLUMN by dragging the card onto the column (todo → doing)
// =========================================================================
{
  PHASE = 'move-to-column'
  let moved = false
  for (let i = 0; i < 60 && !moved; i++) {
    const fired = await dragCardToColumn(send, ADD_ID, 'doing')
    if (fired) {
      moved = await waitJs(
        `const c=window.__ui.cardIds().find(x=>x.column==='doing'); return !!c && c.ids.includes(${JSON.stringify(ADD_ID)})`,
        20,
      )
    }
    if (!moved) await sleep(150)
  }
  const dbCol = sql1(`select column_id from items where id='${ADD_ID}'`)
  const ev = sql1(`select count(*) from events where type='item.moved' and payload->'item'->>'id'='${ADD_ID}'`)
  moved && dbCol === COL2 && Number(ev) >= 1
    ? pass('move to column: card in Doing column + column_id persisted + item.moved event')
    : fail('move to column', `dom=${moved} col=${dbCol} ev=${ev}`)
}

// =========================================================================
// 6. TRANSFER dialog (to_agent + note)
// =========================================================================
PHASE = 'transfer'
await clickEl(`window.__ui.btn(${CARD}, 'transfer')`)
await waitJs(`return !!window.__ui.dlg()?.querySelector('input')`, 40)
await setEl(`window.__ui.dlg().querySelector('input')`, 'be-2')
await setEl(`window.__ui.dlg().querySelector('textarea')`, 'take this please')
await clickEl(`window.__ui.btn(window.__ui.dlg(), 'Transfer')`)
{
  const domOk = await waitJs(`return ((${CARD}?.innerText)||'').includes('@be-2')`, 60)
  const owner = sql1(`select owner_agent from items where id='${ADD_ID}'`)
  const handoff = sql1(`select body from comments where item_id='${ADD_ID}' and body like '[handoff]%' order by created_at desc limit 1`)
  const author = sql1(`select author_name || '|' || author_type from comments where item_id='${ADD_ID}' and body like '[handoff]%' order by created_at desc limit 1`)
  domOk && owner === 'be-2' && handoff.startsWith('[handoff]') && author.startsWith('human')
    ? pass(`transfer: owner=be-2 in DOM+DB; handoff comment "${handoff}" (${author})`)
    : fail('transfer', `dom=${domOk} owner=${owner} handoff=${handoff} author=${author}`)
}
await soft(`window.__ui.dlg()?.close(); return true`)
await sleep(300)

// =========================================================================
// 7. COMMENTS drawer: lists [handoff] + posts a new human comment
// =========================================================================
PHASE = 'comments'
// Comments live in the item detail modal; open it via the card title (the
// card summary no longer has a 'comments' button).
await clickEl(`${CARD}.querySelector('h3 button')`)
await waitJs(`return !!window.__ui.dlg()?.querySelector('textarea')`, 40)
const listedHandoff = await waitJs(`return (window.__ui.dlg()?.innerText||'').includes('[handoff]')`, 60)
const authorName = await js(`return window.__ui.dlg().innerText.includes('human')`)
await setEl(`window.__ui.dlg().querySelector('textarea')`, 'looks good to me')
await clickEl(`window.__ui.btn(window.__ui.dlg(), 'Comment')`)
{
  const domOk = await waitJs(`return (window.__ui.dlg()?.innerText||'').includes('looks good to me')`, 60)
  const onServer = sql1(`select author_type || '|' || body from comments where item_id='${ADD_ID}' and body='looks good to me'`)
  const ev = sql1(`select count(*) from events where type='comment.added' and payload->'comment'->>'item_id'='${ADD_ID}'`)
  listedHandoff && authorName && domOk && onServer === 'human|looks good to me' && Number(ev) >= 1
    ? pass('comments: drawer lists [handoff] + posts new human comment (DB author_type=human, comment.added event)')
    : fail('comments', `listed=${listedHandoff} humanBadge=${authorName} dom=${domOk} server=${onServer} ev=${ev}`)
}
await soft(`window.__ui.dlg()?.close(); return true`)
await sleep(300)

// =========================================================================
// 8. LABEL FILTER — tag the edited card via API, then filter
// =========================================================================
{
  PHASE = 'label'
  const r = await fetch(`${API}/api/items/${ADD_ID}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ label_ids: [LABEL], agent: 'human' }),
  })
  const body = await r.json()
  const labeled = body?.item?.labels?.some((l) => l.id === LABEL)
  const labeledDb = sql1(`select count(*) from item_labels where item_id='${ADD_ID}' and label_id='${LABEL}'`)
  const chip = `Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()===${JSON.stringify('urgent-' + TS)})`
  const chipLive = await waitJs(`return !!(${chip})`, 60)
  const chipOnCard = await waitJs(`return ((${CARD}?.innerText)||'').includes(${JSON.stringify('urgent-' + TS)})`, 60)
  labeled && labeledDb === '1' && chipLive && chipOnCard
    ? pass('label wire-up: label chip + card label rendered (live via SSE) and item_labels row present')
    : fail('label wire-up', `api=${labeled} db=${labeledDb} chip=${chipLive} card=${chipOnCard}`)
  // apply the filter
  await clickEl(chip)
  const filtered = await waitJs(
    `const vis=window.__ui.cards().map(a=>a.getAttribute('data-item'));
     return vis.includes(${JSON.stringify(ADD_ID)}) && !vis.includes(${JSON.stringify(SECOND_ID)})`,
    40,
  )
  filtered ? pass('label filter: only the labelled card visible; unlabelled hidden') : fail('label filter')
  // clear
  await clickEl(`Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()==='All')`)
  await waitJs(`return window.__ui.cards().some(a=>a.getAttribute('data-item')===${JSON.stringify(SECOND_ID)})`, 40)
}

// =========================================================================
// 9. >8 KB description through the ADD form (R1 NOTIFY-8000 regression)
// =========================================================================
{
  PHASE = 'big-desc'
  const BIG = 'X'.repeat(8500)
  const BIG_TITLE = `ui-big-${TS}`
  await clickEl(`window.__ui.btn(window.__ui.col('todo'), '+ Add item')`)
  await waitJs(`return !!window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, 40)
  await setEl(`window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, BIG_TITLE)
  await setEl(`window.__ui.col('todo').querySelector('textarea')`, BIG)
  await clickEl(`window.__ui.btn(window.__ui.col('todo'), 'Add')`)
  const domOk = await waitJs(`return window.__ui.cards().some(a=>window.__ui.titleOf(a)===${JSON.stringify(BIG_TITLE)})`, 80)
  const bigId = sql1(`select id from items where board_id='${BOARD}' and title='${BIG_TITLE}'`)
  const len = sql1(`select length(description) from items where id='${bigId}'`)
  const evLen = sql1(`select length(payload->'item'->>'description') from events where type='item.created' and payload->'item'->>'id'='${bigId}'`)
  domOk && bigId && len === '8500' && evLen === '8500'
    ? pass('>8KB description: item created (len 8500) AND full payload delivered on item.created event (no 8000-byte truncation)')
    : fail('>8KB description', `dom=${domOk} id=${bigId} dbLen=${len} evPayloadLen=${evLen}`)
}

// =========================================================================
// hygiene
// =========================================================================
{
  const text = await js('return document.body.innerText')
  const actionError = /Action failed/.test(text)
  const errBanner = /Could not reach the API/.test(text)
  !actionError && !errBanner && exceptions.length === 0 && consoleErrors.length === 0
    ? pass('hygiene: no action/API errors, no page exceptions, no console errors')
    : fail('hygiene', `actionError=${actionError} apiError=${errBanner} exc=${exceptions.length} console=${consoleErrors.length}`)
}

console.log('\n=== console errors ===')
console.log(consoleErrors.length ? consoleErrors.join('\n') : '(none)')
console.log('=== exceptions ===')
const byPhase = new Map()
for (const e of exceptions) {
  const key = `${e.phase} :: ${e.msg}`
  byPhase.set(key, (byPhase.get(key) ?? 0) + 1)
}
console.log(byPhase.size ? [...byPhase].map(([k, n]) => `${n}x  ${k}`).join('\n') : '(none)')
console.log('\n=== SUMMARY ===')
for (const [s, k] of results) console.log(`${s}  ${k}`)
const failed = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - failed}/${results.length} passed`)

ws.close()
process.exit(failed ? 1 : 0)
