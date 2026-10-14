// PB-72 smoke: searchable label picker in the ItemDetailModal.
//
// Self-seeding like pb70-newlabel.mjs: creates a throwaway workspace + board +
// item + 10 labels with VARYING usage via the REAL REST API, then drives the
// live app through CDP (headless chrome, CDP_PORT 9224). Runs against the
// ISOLATED STACK (PB-68 convention): FE2_API=:18081, APP_URL=:5175,
// FE2_DB=kanban_test — never the live API.
//
// Usage seeding: 10 items; item j carries labels [L0..L(9-j)] (full-set
// REPLACE), so label Lk is used by 10-k items → usage ranking L0 > L1 > …
//
//   a. picker default (focus, empty query) shows exactly the top-8 by usage:
//      L0..L7 — L8/L9 (least used) are NOT listed
//   b. type-to-search narrows: query matching L2/L7 only lists those
//   c. search-before-create: typing an existing label's full name (L9, outside
//      the default top-8) shows its option row and NO create row; selecting it
//      attaches it — no duplicate-name error
//   d. create-if-empty: typing nonsense shows "Create '<text>'"; clicking
//      creates AND attaches the label (chip pressed, API truth verified)
//   e. exact-name guard past the top-8 window: a query matching >8 labels
//      whose exact name ranks OUTSIDE the top-8 must offer the label's row
//      and NOT "Create '<name>'" (that would hit the server's duplicate
//      rejection); selecting it attaches cleanly
//
// Cleanup: deletes the created labels; the throwaway workspace shell stays
// (no workspace DELETE endpoint), same convention as fe2-seed/pb67/pb70.
import {
  APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, openItemModal, apiLabelsOf,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb72smoke-${TS}`

// --- seed -----------------------------------------------------------------

const { ws, board } = await seedWorkspace({ name: NAME })
const column = (await req('POST', `/api/boards/${board.id}/columns`, { name: 'Todo' })).column
const labels = []
for (let k = 0; k < 10; k++) {
  labels.push((await req('POST', `/api/workspaces/${ws.id}/labels`, {
    name: `${NAME}-l${String(k).padStart(2, '0')}`, color: '#2563eb',
  })).label)
}
const items = []
for (let j = 0; j < 10; j++) {
  const item = (await req('POST', `/api/boards/${board.id}/items`, {
    title: `${NAME}-item${j}`, column_id: column.id, agent: 'human',
  })).item
  // label_ids REPLACE: item 0 stays unlabeled (clean slate for the picker);
  // item j≥1 gets the first 10-j labels → Lk used 10-k-1 times (L0=9 … L8=1,
  // L9=0), so the top-8 by usage is exactly L0..L7.
  await req('PATCH', `/api/items/${item.id}`, {
    label_ids: j === 0 ? [] : labels.slice(0, 10 - j).map((l) => l.id), agent: 'human',
  })
  items.push(item)
}
const item = items[0]
console.log(`seeded ws=${ws.id} labels=${labels.length} items=${items.length}`)

// Sanity: the PB-73 contract the picker consumes.
const top8 = (await req('GET', `/api/workspaces/${ws.id}/labels?top=8`)).labels
if (top8.length !== 8 || top8[0].id !== labels[0].id) fail(`API ?top=8 unexpected: ${top8.map((l) => l.name).join(',')}`)

const page = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(page.send)
try {
  // Select our workspace and board in the app.
  if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
    fail('our board never appeared in the board selector')
  if (!(await waitJs(`return document.body.innerText.includes('${board.name}')`))) fail('board never rendered')

  // Open the detail modal.
  if (!(await openItemModal({ js, waitJs, itemId: item.id, readySel: 'dialog[open] [data-labels] [data-label-picker-input]' })))
    fail('card/modal never rendered')

  const pickerRows = () => js(`
    return [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
      .map((o) => o.textContent.trim())
  `)

  // a. default list: open with empty query → top-8 by usage. (Click-open:
  //     headless pages never have window focus, so focus events don't fire.)
  await js(`
    const input = document.querySelector('dialog[open] [data-label-picker-input]')
    input.click()
    return true
  `)
  if (!(await waitJs(`return document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]').length >= 8`)))
    fail('default dropdown never showed options')
  {
    const names = (await pickerRows()).filter((t) => t.includes('-l'))
    const want = labels.slice(0, 8).map((l) => l.name)
    const missing = want.filter((n) => !names.some((t) => t.includes(n)))
    const extra = labels.slice(8).filter((l) => names.some((t) => t.includes(l.name)))
    if (missing.length > 0) fail(`default top-8 missing: ${missing.join(',')}`)
    if (extra.length > 0) fail(`default top-8 must not include least-used labels: ${extra.map((l) => l.name).join(',')}`)
    if (missing.length === 0 && extra.length === 0) console.log('PASS default list = top-8 by usage (L8/L9 excluded)')
  }

  // b. type-to-search narrows: L2's name (unique in the set) narrows to exactly
  //    its one row, and since it IS an exact match no create row is offered.
  const inputJs = (q) => js(`
    const input = document.querySelector('dialog[open] [data-label-picker-input]')
    input.focus()
    input.value = '${q}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)
  await inputJs(labels[2].name)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
    return rows.length === 1 && rows[0].textContent.includes('${labels[2].name}')
      && !document.querySelector('dialog[open] [data-label-create]')
  `)))
    fail(`search "${labels[2].name}" did not narrow to exactly the L2 row (no create row)`)
  else console.log('PASS type-to-search narrows (1 row for L2, no create row on exact match)')

  // c. search-before-create: full name of L9 (least used, not in top-8) →
  //    option row, no create row; clicking selects (attaches), no dup error.
  await inputJs(labels[9].name)
  if (!(await waitJs(`
    const opts = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
    return opts.length === 1 && opts[0].textContent.includes('${labels[9].name}')
      && !document.querySelector('dialog[open] [data-label-create]')
  `)))
    fail('exact existing name did not show its option / showed a create row (search-before-create broken)')
  await js(`
    document.querySelector('dialog[open] [data-label-picker] [role="option"]').click()
    return true
  `)
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.dataset.labelChip === '${labels[9].name}' && b.getAttribute('aria-pressed') === 'true')`)))
    fail('existing label was not selected/attached via search')
  {
    const apiItem = (await req('GET', `/api/items/${item.id}`)).item
    if (!(apiItem.labels ?? []).some((l) => l.id === labels[9].id)) fail('API label set lacks L9 after search-select')
    else console.log('PASS search-before-create: existing name SELECTED and attached (no duplicate created)')
  }

  // d. create-if-empty: nonsense query → create row → click → created+attached.
  const FRESH = `fresh-${TS}`
  await inputJs(FRESH)
  if (!(await waitJs(`return !!document.querySelector('dialog[open] [data-label-create]')`)))
    fail('create row never appeared for a nonsense query')
  await js(`document.querySelector('dialog[open] [data-label-create]').click(); return true`)
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.dataset.labelChip === '${FRESH}' && b.getAttribute('aria-pressed') === 'true')`)))
    fail('created label chip did not appear pressed after Create')
  {
    const apiItem = (await req('GET', `/api/items/${item.id}`)).item
    if (!(apiItem.labels ?? []).some((l) => l.name === FRESH)) fail('API label set lacks the created label')
    else console.log('PASS create-if-empty: label created AND attached (UI + API)')
  }

  // e. exact-name guard past the top-8 window (review N2): seed a label named
  //    exactly NAME plus 9 high-usage 'a' labels so the query NAME matches 20
  //    labels and the exact match ranks below the top-8 by usage.
  const aIds = []
  for (let k = 1; k <= 9; k++) {
    aIds.push((await req('POST', `/api/workspaces/${ws.id}/labels`, { name: `${NAME}-a${k}` })).label.id)
  }
  const exactLabel = (await req('POST', `/api/workspaces/${ws.id}/labels`, { name: NAME })).label
  const cur = (await req('GET', `/api/items/${item.id}`)).item.labels.map((l) => l.id)
  await req('PATCH', `/api/items/${item.id}`, { label_ids: [...cur, ...aIds], agent: 'human' })
  const win = (await req('GET', `/api/workspaces/${ws.id}/labels?q=${encodeURIComponent(NAME)}&top=8`)).labels
  if (win.some((l) => l.id === exactLabel.id)) console.log('note: exact label unexpectedly inside top-8 window; N2 step still exercises the guard')
  await inputJs(NAME)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
    return rows.some((o) => o.textContent.includes('${NAME} '))
      && !document.querySelector('dialog[open] [data-label-create]')
  `)))
    fail('exact name ranked outside top-8 offered Create instead of its row (N2 guard broken)')
  await js(`
    const row = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
      .find((o) => o.textContent.includes('${NAME} '))
    row.click()
    return true
  `)
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.dataset.labelChip === '${NAME}' && b.getAttribute('aria-pressed') === 'true')`)))
    fail('exact-name label outside the top-8 window was not selectable')
  {
    const hasErr = (await js(`return !!document.querySelector('dialog[open] [data-labels] p.text-rose-700')`))
    if (hasErr) fail('duplicate/error text appeared after selecting an existing label (N2)')
    const apiItem = (await req('GET', `/api/items/${item.id}`)).item
    const count = (apiItem.labels ?? []).filter((l) => l.id === exactLabel.id).length
    if (count !== 1) fail(`exact-name label attach wrong via API (count=${count})`)
    else console.log('PASS exact-name guard past top-8 window: row offered (no Create), attached once, no error')
  }

  console.log(process.exitCode ? 'SMOKE FAILED' : 'SMOKE OK')
} finally {
  try {
    const all = (await req('GET', `/api/workspaces/${ws.id}/labels`)).labels
    for (const l of all) await req('DELETE', `/api/labels/${l.id}`)
  } catch (e) { console.error(`cleanup: ${e.message}`) }
  page.ws.close()
}
