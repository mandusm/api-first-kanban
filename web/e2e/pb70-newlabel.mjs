// PB-70 smoke: on-the-fly label creation in the ItemDetailModal.
//
// Updated for PB-72: the "+" inline form was absorbed by the searchable
// LabelSelect picker — creation now happens via the picker's "Create '<text>'"
// row (search-before-create: only offered when no existing label matches).
// Self-seeding like pb67-labels.mjs: creates a throwaway workspace + board +
// two items + one pre-existing label via the REAL REST API, then drives the
// live app through CDP (headless chrome, CDP_PORT 9224). Runs against the
// ISOLATED STACK (PB-68 convention): FE2_API=:18081, APP_URL=:5175,
// FE2_DB=kanban_test — never the live API.
//
//   a. open the item's detail modal
//   b. click the "+" button → inline form appears → type a name → Create
//   c. the new label's chip appears on the item (created AND attached)
//   d. API GET shows the label on the item (label set REPLACE flow)
//   e. the same label is reusable on a second item via the picker
//
// Cleanup: deletes the created labels; the throwaway workspace shell stays
// (no workspace DELETE endpoint), same convention as fe2-seed/pb67.
import {
  APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, openItemModal,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb70smoke-${TS}`
const NEW = `fresh-${TS}`

const { ws, board } = await seedWorkspace({ name: NAME })
const column = (await req('POST', `/api/boards/${board.id}/columns`, { name: 'Todo' })).column
const item = (await req('POST', `/api/boards/${board.id}/items`, {
  title: `${NAME}-item`, column_id: column.id, agent: 'human',
})).item
const item2 = (await req('POST', `/api/boards/${board.id}/items`, {
  title: `${NAME}-item2`, column_id: column.id, agent: 'human',
})).item
const labelA = (await req('POST', `/api/workspaces/${ws.id}/labels`, { name: 'alpha', color: '#dc2626' })).label
console.log(`seeded ws=${ws.id} items=${item.id},${item2.id} label=${labelA.id}`)

const page = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(page.send)
try {
  // Select our workspace and board in the app.
  if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
    fail('our board never appeared in the board selector')
  if (!(await waitJs(`return document.body.innerText.includes('${board.name}')`))) fail('board never rendered')

  // a. open the detail modal of item 1.
  if (!(await openItemModal({ js, waitJs, itemId: item.id }))) fail('card/modal never rendered')

  // b. type a fresh name into the label picker → "Create '<text>'" row appears.
  if (!(await waitJs(`return !!document.querySelector('dialog[open] [data-labels] [data-label-picker-input]')`)))
    fail('label picker input never rendered')
  await js(`
    const input = document.querySelector('dialog[open] [data-labels] [data-label-picker-input]')
    input.focus()
    input.value = '${NEW}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)
  if (!(await waitJs(`return !!document.querySelector('dialog[open] [data-labels] [data-label-create]')`)))
    fail('create row never appeared for a fresh label name')
  await js(`document.querySelector('dialog[open] [data-labels] [data-label-create]').click(); return true`)

  // c. the form closes and the new chip appears, pressed (attached).
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.textContent.trim() === '${NEW}' && b.getAttribute('aria-pressed') === 'true')`)))
    fail('new label chip did not appear pressed after Create')

  // d. API truth: the item now carries exactly [alpha? no] — the new label.
  const apiItem = (await req('GET', `/api/items/${item.id}`)).item
  const apiLabels = (await req('GET', `/api/workspaces/${ws.id}/labels`)).labels
  const created = apiLabels.find((l) => l.name === NEW)
  if (!created) {
    fail(`created label "${NEW}" missing from workspace labels via API`)
  } else {
    const ids = apiItem.labels?.map((l) => l.id) ?? []
    if (!ids.includes(created.id)) fail(`API label set of item 1 lacks the created label: ${ids.join(',')}`)
    else console.log(`PASS create+attach: chip pressed in UI; API item labels = ${apiItem.labels.map((l) => l.name).join(',')}`)
    if (!created.color) fail('created label has no color (palette pick expected)')
  }

  // e. the same label is usable on a second item via the picker.
  await js(`
    document.querySelector('dialog[open] button[title="Close"]').click()
    return true
  `)
  if (!(await waitJs(`return !!document.querySelector('[data-item="${item2.id}"]')`))) fail('card 2 never rendered')
  await js(`
    const card = document.querySelector('[data-item="${item2.id}"]')
    const open = card?.querySelector('button[title="Open item details"]') ?? card
    open.click()
    return true
  `)
  if (!(await waitJs(`return !!document.querySelector('dialog[open] [data-labels]')`))) fail('modal 2 never rendered')
  // PB-72: the chip cloud is gone — attach via the picker (type the exact name,
  // click its option row; search-before-create guarantees no create row).
  await js(`
    const input = document.querySelector('dialog[open] [data-labels] [data-label-picker-input]')
    input.focus()
    input.value = '${NEW}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
    return rows.length === 1 && rows[0].textContent.includes('${NEW}')
      && !document.querySelector('dialog[open] [data-label-create]')
  `)))
    fail('picker did not narrow to the existing label (create row should be absent)')
  await js(`
    document.querySelector('dialog[open] [data-label-picker] [role="option"]').click()
    return true
  `)
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].find(b => b.textContent.trim()==='${NEW}')?.getAttribute('aria-pressed') === 'true'`)))
    fail('created label could not be attached to a second item')
  const apiItem2 = (await req('GET', `/api/items/${item2.id}`)).item
  if (!(apiItem2.labels ?? []).some((l) => l.name === NEW)) fail('API label set of item 2 lacks the created label')
  else console.log('PASS reuse: created label attached to a second item (UI + API)')

  console.log(process.exitCode ? 'SMOKE FAILED' : 'SMOKE OK')
} finally {
  try {
    const labels = (await req('GET', `/api/workspaces/${ws.id}/labels`)).labels
    for (const l of labels) await req('DELETE', `/api/labels/${l.id}`)
  } catch (e) { console.error(`cleanup: ${e.message}`) }
  page.ws.close()
}
