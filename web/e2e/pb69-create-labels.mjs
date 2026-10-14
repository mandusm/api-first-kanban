// PB-69 smoke: labels on task creation.
//
// Self-seeding like pb72-picker.mjs: creates a throwaway workspace + board +
// columns + 12 labels with VARYING usage via the REAL REST API, then drives
// the live app through CDP (headless chrome, CDP_PORT 9224). Runs against the
// ISOLATED STACK (PB-68 convention): FE2_API=:18081, APP_URL=:5175,
// FE2_DB=kanban_test — never the live API.
//
// Usage seeding: 10 items; item j carries labels [L0..L(10-j)) (full-set
// REPLACE), so label Lk is used by 10-k items → usage ranking L0 > L1 > …
//
//   a. open the column create form → the label picker shows the top-8 by
//      usage by default; L8..L11 are NOT listed
//   b. select an EXISTING label (L3 via search) AND create a NEW one inline
//      ("Create '<text>'" row) — both end up selected before submit
//   c. submit → the created item EXISTS via the API with EXACTLY those two
//      label_ids (create-request label_ids), and chips render on the card
//
// Cleanup: deletes the created labels; the throwaway workspace shell stays
// (no workspace DELETE endpoint), same convention as fe2-seed/pb67/pb70/pb72.
import {
  APP_URL, req, fail, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb69smoke-${TS}`

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

const page = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(page.send)
try {
  // Select our workspace and board in the app.
  if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
  if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
    fail('our board never appeared in the board selector')
  if (!(await waitJs(`return document.body.innerText.includes('${board.name}')`))) fail('board never rendered')

  // Open the column create form.
  if (!(await waitJs(`return !!document.querySelector('[data-column="${column.name}"]')`))) fail('column never rendered')
  await js(`
    const col = document.querySelector('[data-column="${column.name}"]')
    const btn = [...col.querySelectorAll('button')].find((b) => b.textContent.trim() === '+ Add item')
    btn.click()
    return true
  `)
  if (!(await waitJs(`return !!document.querySelector('[data-column="${column.name}"] form')`)))
    fail('create form never rendered')
  await js(`
    const form = document.querySelector('[data-column="${column.name}"] form')
    form.querySelector('input[placeholder="Item title"]').value = '${NAME}-created'
    form.querySelector('input[placeholder="Item title"]')
      .dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)

  // a. open the picker in the create form: default list = top-8 by usage.
  const formSel = `[data-column="${column.name}"] form`
  await js(`
    const input = document.querySelector('${formSel} [data-label-picker-input]')
    input.click()
    return true
  `)
  if (!(await waitJs(`return document.querySelectorAll('${formSel} [data-label-picker] [role="option"]').length >= 8`)))
    fail('create-form picker never showed default options')
  {
    const names = await js(`
      return [...document.querySelectorAll('${formSel} [data-label-picker] [role="option"]')]
        .map((o) => o.textContent.trim())
    `)
    const want = labels.slice(0, 8).map((l) => l.name)
    const missing = want.filter((n) => !names.some((t) => t.includes(n)))
    const extra = labels.slice(8).filter((l) => names.some((t) => t.includes(l.name)))
    if (missing.length > 0) fail(`create-form default top-8 missing: ${missing.join(',')}`)
    if (extra.length > 0) fail(`create-form default list must not include least-used labels: ${extra.map((l) => l.name).join(',')}`)
    if (missing.length === 0 && extra.length === 0) console.log('PASS create-form picker default = top-8 by usage (L8..L11 excluded)')
  }

  // b1. select an EXISTING label: type L3's exact name → its row (no create
  //     row on an exact match) → click to select.
  const inputJs = (q) => js(`
    const input = document.querySelector('${formSel} [data-label-picker-input]')
    input.focus()
    input.value = '${q}'
    input.dispatchEvent(new Event('input', { bubbles: true }))
    return true
  `)
  await inputJs(labels[3].name)
  if (!(await waitJs(`
    const rows = [...document.querySelectorAll('${formSel} [data-label-picker] [role="option"]')]
    return rows.length === 1 && rows[0].textContent.includes('${labels[3].name}')
      && !document.querySelector('${formSel} [data-label-create]')
  `)))
    fail(`create-form search "${labels[3].name}" did not narrow to its single row (no create row)`)
  await js(`
    document.querySelector('${formSel} [data-label-picker] [role="option"]').click()
    return true
  `)
  // b2. create a NEW label inline: nonsense query → Create row → click.
  const FRESH = `fresh-${TS}`
  await inputJs(FRESH)
  if (!(await waitJs(`return !!document.querySelector('${formSel} [data-label-create]')`)))
    fail('create row never appeared in the create-form picker for a nonsense query')
  await js(`document.querySelector('${formSel} [data-label-create]').click(); return true`)
  // After picking, the picker resets to the default top list — both picks must
  // be staged in the form's bound value by now (verified via submit assertions).
  console.log('PASS staged: existing label L3 selected + new label created inline')

  // c. submit → item exists via the API with EXACTLY the two label_ids.
  // The inline label creation flips store.saving (createLabel), which disables
  // the submit button until the mutation settles — wait for it first.
  if (!(await waitJs(`return [...document.querySelectorAll('${formSel} button[type=submit]')].some((b) => !b.disabled)`)))
    fail('submit button never became enabled')
  await js(`
    const form = document.querySelector('${formSel}')
    const submit = [...form.querySelectorAll('button')].find((b) => b.type === 'submit')
    submit.click()
    return true
  `)
  if (!(await waitJs(`return !document.querySelector('${formSel}')`)))
    fail('create form never closed after submit')
  const created = (await req('GET', `/api/boards/${board.id}/items?column_id=${column.id}`)).items
    .find((i) => i.title === `${NAME}-created`)
  if (!created) { fail('created item not found via API'); process.exitCode = 1 }
  else {
    const apiItem = (await req('GET', `/api/items/${created.id}`)).item
    const got = (apiItem.labels ?? []).map((l) => l.id).sort()
    const want = [labels[3].id, (await req('GET', `/api/workspaces/${ws.id}/labels?q=${encodeURIComponent(FRESH)}`)).labels[0].id].sort()
    if (got.length !== want.length || !got.every((id, i) => id === want[i]))
      fail(`API label set mismatch: got ${got.length} labels, want exactly [L3, ${FRESH}]`)
    else console.log('PASS API: created item carries EXACTLY the two selected label_ids')
    // Chips render on the card (SSE + optimistic flow converge the card view).
    if (!(await waitJs(`
      const card = document.querySelector('[data-item="${created.id}"]')
      if (!card) return false
      const text = card.textContent
      return text.includes('${labels[3].name}') && text.includes('${FRESH}')
    `)))
      fail('created card does not render both label chips')
    else console.log('PASS chips render on the created card')
  }

  console.log(process.exitCode ? 'SMOKE FAILED' : 'SMOKE OK')
} finally {
  try {
    const all = (await req('GET', `/api/workspaces/${ws.id}/labels`)).labels
    for (const l of all) await req('DELETE', `/api/labels/${l.id}`)
  } catch (e) { console.error(`cleanup: ${e.message}`) }
  page.ws.close()
}
