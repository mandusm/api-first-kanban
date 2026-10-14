// PB-67 smoke: label management in the ItemDetailModal.
//
// Updated for PB-72: the modal renders chips only for ATTACHED labels
// (aria-pressed is always "true"), so addition = chip appears, removal =
// chip disappears; the searchable picker drives both. The pre-PB-72
// "unpressed chip" model no longer exists.
//
// Self-seeding like the other fe2-* live scripts: creates a throwaway
// workspace + board + item + two labels via the REAL REST API, then drives
// the live app through CDP (chrome :9224; isolated stack by default):
//   a. open the item's detail modal
//   b. add label A via the picker (click its option row) -> chip appears,
//      API label set updated
//   c. add label B the same way
//   d. remove label A by clicking its attached chip -> chip disappears,
//      beta stays, API label set matches (REPLACE semantics, SSE-synced)
//
// Cleanup: the throwaway workspace is deleted via the API at the end.
// PB-81: stack defaults + req/fail/seed/modal helpers come from the shared
// harness (isolated stack by default; FE2_STACK=live opts into the live stack).
import {
  API, APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, openItemModal,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb67smoke-${TS}`

const { ws, board } = await seedWorkspace({ name: NAME })
const column = (await req('POST', `/api/boards/${board.id}/columns`, { name: 'Todo' })).column
const item = (await req('POST', `/api/boards/${board.id}/items`, {
  title: `${NAME}-item`, column_id: column.id, agent: 'human',
})).item
const labelA = (await req('POST', `/api/workspaces/${ws.id}/labels`, { name: 'alpha', color: '#dc2626' })).label
const labelB = (await req('POST', `/api/workspaces/${ws.id}/labels`, { name: 'beta', color: '#2563eb' })).label
console.log(`seeded ws=${ws.id} item=${item.id} labels=${labelA.id},${labelB.id}`)

const page = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(page.send)
try {
  // Wait for the app to load its workspaces, then pick ours; the board list
  // loads asynchronously, so select our board in the board dropdown too.
  if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
    fail('our board never appeared in the board selector')
  // Wait for our board to become the active one.
  if (!(await waitJs(`return document.body.innerText.includes('${board.name}')`))) fail('board never rendered')

  // Open the detail modal by clicking the item's card.
  if (!(await openItemModal({ js, waitJs, itemId: item.id }))) fail('card/modal never rendered')

  // b. add label A via the picker: open the dropdown, click alpha's option
  //    row (the picker only lists existing labels here — no create needed).
  await js(`
    document.querySelector('dialog[open] [data-label-picker-input]').click()
    return true
  `)
  if (!(await waitJs(`return document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]').length > 0`)))
    fail('picker dropdown never showed options')
  await js(`
    const row = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
      .find((o) => o.textContent.includes('alpha'))
    if (!row) throw new Error('alpha option row not found')
    row.click()
    return true
  `)
  // Attached chips are the only chips: wait for the alpha chip to APPEAR.
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.textContent.trim()==='alpha')`)))
    fail('alpha chip never appeared after picker add')
  let apiItem = (await req('GET', `/api/items/${item.id}`)).item
  if (!(apiItem.label_ids ?? apiItem.labels?.map((l) => l.id))?.includes(labelA.id))
    fail('API label_ids did not gain labelA after add')
  else console.log('PASS add: chip appeared + API label set contains alpha')

  // c. add label B the same way.
  await js(`
    document.querySelector('dialog[open] [data-label-picker-input]').click()
    return true
  `)
  if (!(await waitJs(`return document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]').length > 0`)))
    fail('picker dropdown never reopened for beta')
  await js(`
    const row = [...document.querySelectorAll('dialog[open] [data-label-picker] [role="option"]')]
      .find((o) => o.textContent.includes('beta'))
    if (!row) throw new Error('beta option row not found')
    row.click()
    return true
  `)
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.textContent.trim()==='beta')`)))
    fail('beta chip never appeared after picker add')
  // d. removal — click alpha's attached chip; SSE should already have
  //    converged the modal (no reload). Post-PB-72 the chip DISAPPEARS
  //    (only attached labels render), it does not become unpressed.
  await js(`
    const chip = [...document.querySelectorAll('dialog[open] [data-labels] button')].find(b => b.textContent.trim() === 'alpha')
    if (!chip) throw new Error('alpha chip not found')
    chip.click()
    return true
  `)
  if (!(await waitJs(`return ![...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.textContent.trim()==='alpha')`)))
    fail('alpha chip did not disappear after remove')
  if (!(await waitJs(`return [...document.querySelectorAll('dialog[open] [data-labels] button')].some(b => b.textContent.trim()==='beta')`)))
    fail('beta chip disappeared after the remove PATCH (REPLACE semantics broken in UI)')
  apiItem = (await req('GET', `/api/items/${item.id}`)).item
  const ids = apiItem.labels?.map((l) => l.id) ?? []
  if (!ids.includes(labelB.id) || ids.includes(labelA.id))
    fail(`API label set wrong after remove: ${ids.join(',')}`)
  else console.log('PASS remove: SSE-synced UI shows exactly [beta] and API labels match (alpha removed, beta kept)')

  console.log(process.exitCode ? 'SMOKE FAILED' : 'SMOKE OK')
} finally {
  // No DELETE endpoints exist for workspaces, boards or items, so the
  // throwaway data cannot be fully removed; clean what the API allows
  // (labels) and leave the pb67smoke-* workspace shell for inspection,
  // same convention as fe2-seed.
  try {
    await req('DELETE', `/api/labels/${labelA.id}`)
    await req('DELETE', `/api/labels/${labelB.id}`)
  } catch (e) { console.error(`cleanup: ${e.message}`) }
  page.ws.close()
}
