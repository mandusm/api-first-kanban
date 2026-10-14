// PB-74 closeout integration e2e: the full label lifecycle across features on
// ONE seeded board telling one story (PB-61 precedent for the epic closeout).
//
// Self-seeding like pb69/pb72: creates a throwaway workspace + board + labels
// with VARYING usage via the REAL REST API, then drives the live app through
// CDP (headless chrome, CDP_PORT 9224). Runs against the ISOLATED STACK
// (PB-68 convention): FE2_API=:18081, APP_URL=:5175, FE2_DB=kanban_test —
// never the live API.
//
// Usage seeding: 10 items; item j carries labels [L0..L(10-j)) (full-set
// REPLACE), so label Lk is used by 10-k items → usage ranking L0 > L1 > …,
// and the pickers' default top-8 is exactly L0..L7 (L8..L11 least used).
//
// The story, on one board:
//   1. CREATE FORM (PB-69): open the column create form, pick an EXISTING
//      label via search (L3) + CREATE a new label on the fly inline, submit →
//      the item exists via the API with EXACTLY those two label_ids, chips
//      render on the card.
//   2. MODAL PICKER (PB-72/73 + PB-70): open the created item's detail modal —
//      a. picker default = top-8 by usage (L8..L11 NOT listed)
//      b. type-to-search narrows to the exact match (no create row)
//      c. search-before-create: L9 (outside top-8) is offered as a row, not
//         "Create …"; selecting attaches it (no duplicate 409)
//      d. create-if-empty (PB-70 flow): nonsense query → "Create '<text>'" →
//         label created AND attached (UI chip pressed + API truth)
//   3. CARDS: chips render on every labeled card.
//   4. FILTER (search row): LABEL IN ('<L0>') matches exactly the card(s)
//      carrying L0; clearing the query restores the board.
//   5. MODAL ADD/REMOVE PERSISTS (PB-67): add a chip, remove another, close
//      the modal, reload the page — UI and API still agree (REPLACE set).
//
// Cleanup: deletes the created labels; the throwaway workspace shell stays
// (no workspace DELETE endpoint), same convention as fe2-seed/pb67/pb70/pb72.
import {
  APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, openItemModal,
  closeModal as closeItemModal, apiLabelsOf,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb74story-${TS}`

// --- seed -----------------------------------------------------------------

const { ws, board } = await seedWorkspace({ name: NAME })
const column = (await req('POST', `/api/boards/${board.id}/columns`, { name: 'Todo' })).column
const labels = []
for (let k = 0; k < 12; k++) {
  labels.push((await req('POST', `/api/workspaces/${ws.id}/labels`, {
    name: `${NAME}-l${String(k).padStart(2, '0')}`, color: '#2563eb',
  })).label)
}
for (let j = 0; j < 10; j++) {
  const item = (await req('POST', `/api/boards/${board.id}/items`, {
    title: `${NAME}-seed${j}`, column_id: column.id, agent: 'human',
  })).item
  // label_ids REPLACE: item j≥1 gets the first 10-j labels → Lk used 10-k-1
  // times (L0=9 … L9=0, L10/L11=0), so the top-8 by usage is exactly L0..L7.
  await req('PATCH', `/api/items/${item.id}`, {
    label_ids: j === 0 ? [] : labels.slice(0, 10 - j).map((l) => l.id), agent: 'human',
  })
}
console.log(`seeded ws=${ws.id} labels=${labels.length} seed-items=10`)

// Sanity: the PB-73 contract both pickers consume.
const top8 = (await req('GET', `/api/workspaces/${ws.id}/labels?top=8`)).labels
if (top8.length !== 8 || top8[0].id !== labels[0].id || top8[0].usage_count === undefined)
  fail(`API ?top=8 contract unexpected: ${JSON.stringify(top8[0] ?? top8)}`)
else console.log('PASS API sanity: ?top=8 usage-ranked with usage_count (PB-73)')

const page = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(page.send)
const selectBoard = async () => {
  if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
    fail('our board never appeared in the board selector')
  if (!(await waitJs(`return document.body.innerText.includes('${board.name}')`))) fail('board never rendered')
}
await selectBoard()

const openModal = async (itemId) => {
  if (!(await openItemModal({ js, waitJs, itemId, readySel: 'dialog[open] [data-labels] [data-label-picker-input]' })))
    fail('card/modal never rendered')
}
const closeModal = async () => {
  if (!(await closeItemModal({ js, waitJs }))) fail('modal never closed')
}

try {
  // ---------------------------------------------------------------- 1. create form
  const colSel = `[data-column="${column.name}"]`
  if (!(await waitJs(`return !!document.querySelector('${colSel}')`))) fail('column never rendered')
  await js(`
    const col = document.querySelector('${colSel}')
    const btn = [...col.querySelectorAll('button')].find((b) => b.textContent.trim() === '+ Add item')
    btn.click()
    return true
  `)
  if (!(await waitJs(`return !!document.querySelector('${colSel} form')`))) fail('create form never rendered')
  await js(`
    const form = document.querySelector('${colSel} form')
    form.querySelector('input[placeholder="Item title"]').value = '${NAME}-created'
    form.querySelector('input[placeholder="Item title"]')
      .dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)
  const formSel = `${colSel} form`
  const formInputJs = (q) => js(`
    const input = document.querySelector('${formSel} [data-label-picker-input]')
    input.focus()
    input.value = '${q}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)

  // existing label via search: L3's exact name → its single row → select.
  if (!(await waitJs(`return !!document.querySelector('${formSel} [data-label-picker-input]')`)))
    fail('create-form picker input never rendered')
  await js(`document.querySelector('${formSel} [data-label-picker-input]').click(); return true`)
  if (!(await waitJs(`return document.querySelectorAll('${formSel} [data-label-picker] [role="option"]').length >= 8`)))
    fail('create-form picker never showed default options')
  await formInputJs(labels[3].name)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('${formSel} [data-label-picker] [role="option"]')]
    return rows.length === 1 && rows[0].textContent.includes('${labels[3].name}')
      && !document.querySelector('${formSel} [data-label-create]')
  `)))
    fail(`create-form search "${labels[3].name}" did not narrow to its single row`)
  await js(`document.querySelector('${formSel} [data-label-picker] [role="option"]').click(); return true`)

  // create a NEW label on the fly inside the create form: nonsense → Create row.
  const FRESH_FORM = `formfresh-${TS}`
  await formInputJs(FRESH_FORM)
  if (!(await waitJs(`return !!document.querySelector('${formSel} [data-label-create]')`)))
    fail('create-form picker never offered Create for a nonsense query')
  await js(`document.querySelector('${formSel} [data-label-create]').click(); return true`)

  // inline label creation flips store.saving — wait for submit to re-enable.
  if (!(await waitJs(`return [...document.querySelectorAll('${formSel} button[type=submit]')].some((b) => !b.disabled)`)))
    fail('create-form submit button never re-enabled')
  await js(`
    const submit = [...document.querySelector('${formSel}').querySelectorAll('button')]
      .find((b) => b.type === 'submit')
    submit.click()
    return true
  `)
  if (!(await waitJs(`return !document.querySelector('${formSel}')`))) fail('create form never closed after submit')

  const created = (await req('GET', `/api/boards/${board.id}/items?column_id=${column.id}`)).items
    .find((i) => i.title === `${NAME}-created`)
  if (!created) { fail('created item not found via API'); process.exitCode = 1 }
  const freshFormLabel = (await req('GET', `/api/workspaces/${ws.id}/labels?q=${encodeURIComponent(FRESH_FORM)}`)).labels[0]
  {
    const got = (await apiLabelsOf(created.id)).map((l) => l.id).sort()
    const want = [labels[3].id, freshFormLabel.id].sort()
    if (got.length !== want.length || !got.every((id, i) => id === want[i]))
      fail(`create form: API label set mismatch (want exactly [L3, ${FRESH_FORM}])`)
    else console.log('PASS create form: item created with EXACTLY the searched + on-the-fly labels (PB-69)')
    if (!(await waitJs(`
      const card = document.querySelector('[data-item="${created.id}"]')
      if (!card) return false
      const text = card.textContent
      return text.includes('${labels[3].name}') && text.includes('${FRESH_FORM}')
    `)))
      fail('created card does not render both label chips')
    else console.log('PASS chips render on the created card')
  }

  // ---------------------------------------------------------------- 2. modal picker
  await openModal(created.id)
  const dlg = 'dialog[open]'
  const inputJs = (q) => js(`
    const input = document.querySelector('${dlg} [data-label-picker-input]')
    input.focus()
    input.value = '${q}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)

  // a. default = top-8 by usage.
  await js(`document.querySelector('${dlg} [data-label-picker-input]').click(); return true`)
  if (!(await waitJs(`return document.querySelectorAll('${dlg} [data-label-picker] [role="option"]').length >= 8`)))
    fail('modal picker default dropdown never showed options')
  {
    const names = await js(`
      return [...document.querySelectorAll('${dlg} [data-label-picker] [role="option"]')]
        .map((o) => o.textContent.trim())
    `)
    const want = labels.slice(0, 8).map((l) => l.name)
    const missing = want.filter((n) => !names.some((t) => t.includes(n)))
    const extra = labels.slice(8).filter((l) => names.some((t) => t.includes(l.name)))
    if (missing.length > 0) fail(`modal default top-8 missing: ${missing.join(',')}`)
    if (extra.length > 0) fail(`modal default list must not include least-used labels: ${extra.map((l) => l.name).join(',')}`)
    if (missing.length === 0 && extra.length === 0)
      console.log('PASS modal picker default = top-8 by usage (L8..L11 excluded) (PB-72/73)')
  }

  // b. type-to-search narrows to the exact match.
  await inputJs(labels[2].name)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('${dlg} [data-label-picker] [role="option"]')]
    return rows.length === 1 && rows[0].textContent.includes('${labels[2].name}')
      && !document.querySelector('${dlg} [data-label-create]')
  `)))
    fail(`modal type-to-search did not narrow to L2's single row`)
  else console.log('PASS modal type-to-search narrows (exact match, no create row)')

  // c. search-before-create: L9 is outside the default top-8 but exists —
  //    its row (not Create) must be offered; selecting attaches it.
  await inputJs(labels[9].name)
  if (!(await waitJs(`
    const opts = [...document.querySelectorAll('${dlg} [data-label-picker] [role="option"]')]
    return opts.length === 1 && opts[0].textContent.includes('${labels[9].name}')
      && !document.querySelector('${dlg} [data-label-create]')
  `)))
    fail('L9 (existing, outside top-8) was not offered as a row — search-before-create broken')
  await js(`document.querySelector('${dlg} [data-label-picker] [role="option"]').click(); return true`)
  {
    // the modal only renders chips for ATTACHED labels — wait for the attach
    // PATCH to land before asserting API truth (onPickerPick is async).
    if (!(await waitJs(`return [...document.querySelectorAll('${dlg} [data-labels] button')].some(b => b.dataset.labelChip === '${labels[9].name}')`)))
      fail('L9 chip never appeared after search-before-create select')
    const ids = (await apiLabelsOf(created.id)).map((l) => l.id)
    if (!ids.includes(labels[9].id)) fail('L9 not attached via search-before-create (API)')
    else console.log('PASS search-before-create: L9 found via search and attached (no duplicate)')
  }

  // d. create-if-empty (PB-70 flow through the PB-72 picker).
  const FRESH_MODAL = `modalfresh-${TS}`
  await inputJs(FRESH_MODAL)
  if (!(await waitJs(`return !!document.querySelector('${dlg} [data-label-create]')`)))
    fail('modal picker never offered Create for a nonsense query')
  await js(`document.querySelector('${dlg} [data-label-create]').click(); return true`)
  if (!(await waitJs(`return [...document.querySelectorAll('${dlg} [data-labels] button')].some(b => b.dataset.labelChip === '${FRESH_MODAL}' && b.getAttribute('aria-pressed') === 'true')`)))
    fail('created label chip did not appear pressed after Create')
  {
    const ls = await apiLabelsOf(created.id)
    if (!ls.some((l) => l.name === FRESH_MODAL)) fail('API label set lacks the created label')
    else console.log('PASS create-if-empty: label created AND attached from the modal (PB-70/72)')
  }
  await closeModal()

  // ---------------------------------------------------------------- 4. filter by label
  const L0Cards = (await req('GET', `/api/boards/${board.id}`)).items
    .filter((i) => (i.labels ?? []).some((l) => l.id === labels[0].id)).map((i) => i.id)
  {
    const input = `input[data-search-input]`
    await js(`
      const i = document.querySelector('${input}')
      i.value = "LABEL IN ('${labels[0].name}')"
      i.dispatchEvent(new Event('input', { bubbles: true }))
      return true
    `)
    if (!(await waitJs(`
      return [...document.querySelectorAll('article[data-item]')]
        .every((c) => ${JSON.stringify(L0Cards)}.includes(c.getAttribute('data-item')))
        && document.querySelectorAll('article[data-item]').length === ${L0Cards.length}
    `)))
      fail(`filter LABEL IN ('${labels[0].name}') did not reduce the board to exactly the L0 cards`)
    else console.log(`PASS filter by label: LABEL IN shows exactly the ${L0Cards.length} L0 card(s)`)
    await js(`
      const i = document.querySelector('${input}')
      i.value = ''
      i.dispatchEvent(new Event('input', { bubbles: true }))
      return true
    `)
    if (!(await waitJs(`return document.querySelectorAll('article[data-item]').length > ${L0Cards.length}`)))
      fail('clearing the filter did not restore the board')
    else console.log('PASS clearing the filter restores the full board')
  }

  // ---------------------------------------------------------------- 5. modal add/remove persists
  await openModal(created.id)
  {
    // add L1 via its chip in the picker list (open picker, click L1's row).
    await js(`document.querySelector('${dlg} [data-label-picker-input]').click(); return true`)
    if (!(await waitJs(`return document.querySelectorAll('${dlg} [data-label-picker] [role="option"]').length >= 8`)))
      fail('picker dropdown never reopened for the add step')
    await js(`
      const row = [...document.querySelectorAll('${dlg} [data-label-picker] [role="option"]')]
        .find((o) => o.textContent.includes('${labels[1].name}'))
      row.click()
      return true
    `)
    if (!(await waitJs(`return [...document.querySelectorAll('${dlg} [data-labels] button')].find(b => b.dataset.labelChip==='${labels[1].name}')?.getAttribute('aria-pressed') === 'true'`)))
      fail('L1 chip did not become pressed after add')
    // remove L3 via its chip — the modal renders chips only for ATTACHED
    // labels (aria-pressed is always "true"), so removal makes the chip
    // DISAPPEAR (post-PB-72 chip model; pb67's unpressed assertions predate it).
    await js(`
      const chip = [...document.querySelectorAll('${dlg} [data-labels] button')]
        .find((b) => b.dataset.labelChip === '${labels[3].name}')
      chip.click()
      return true
    `)
    if (!(await waitJs(`return ![...document.querySelectorAll('${dlg} [data-labels] button')].some(b => b.dataset.labelChip==='${labels[3].name}')`)))
      fail('L3 chip did not disappear after remove')
    if (!(await waitJs(`return [...document.querySelectorAll('${dlg} [data-labels] button')].some(b => b.dataset.labelChip==='${labels[1].name}')`)))
      fail('L1 chip disappeared after the remove PATCH (REPLACE semantics broken)')
    await closeModal()
    // persistence: reload the page and re-check UI + API.
    await js(`location.reload(); return true`)
    await selectBoard()
    const ids = (await apiLabelsOf(created.id)).map((l) => l.id).sort()
    const wantLs = [labels[1].id, labels[9].id, freshFormLabel.id,
      (await req('GET', `/api/workspaces/${ws.id}/labels?q=${encodeURIComponent(FRESH_MODAL)}`)).labels[0].id].sort()
    if (ids.length !== wantLs.length || !ids.every((id, i) => id === wantLs[i]))
      fail(`persistence: API label set after add/remove/reload wrong (${ids.length} labels)`)
    else if (!(await waitJs(`
      const card = document.querySelector('[data-item="${created.id}"]')
      if (!card) return false
      const text = card.textContent
      return text.includes('${labels[1].name}') && text.includes('${FRESH_MODAL}')
        && text.includes('${FRESH_FORM}') && !text.includes('${labels[3].name}')
    `)))
      fail('persistence: reloaded card chips do not match the add/remove outcome')
    else console.log('PASS modal add/remove persists across reload — UI and API agree (PB-67)')
  }

  console.log(process.exitCode ? 'PB-74 E2E FAILED' : 'PB-74 E2E OK — the full label story holds on one board')
} finally {
  try {
    const all = (await req('GET', `/api/workspaces/${ws.id}/labels`)).labels
    for (const l of all) await req('DELETE', `/api/labels/${l.id}`)
  } catch (e) { console.error(`cleanup: ${e.message}`) }
  page.ws.close()
}
