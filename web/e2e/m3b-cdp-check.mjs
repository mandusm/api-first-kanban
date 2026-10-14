// CDP-driven end-to-end check of the M3b mutation UI against the stateful stub.
// Node built-in WebSocket; no puppeteer/playwright.
//
// Two robustness rules learned the hard way:
//  1. The SPA briefly clears the board while re-selecting a workspace, and Vite
//     reloads once after dep pre-bundling — so every lookup retries.
//  2. Cards are addressed by their stable [data-item] id, never by title,
//     because edit mode replaces the <h3> with an <input>.
import { setTimeout as sleep } from 'node:timers/promises'

const CDP = process.env.CDP_PORT || '9224'
const APP = process.env.APP_URL || 'http://127.0.0.1:5195/'
const STUB = 'http://127.0.0.1:18098'

async function firstPageTarget() {
  for (let i = 0; i < 80; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${CDP}/json/list`)
      const targets = await res.json()
      const page = targets.find((t) => t.type === 'page')
      if (page?.webSocketDebuggerUrl) return page.webSocketDebuggerUrl
    } catch {
      /* not up */
    }
    await sleep(250)
  }
  throw new Error('no CDP page target')
}

const ws = new WebSocket(await firstPageTarget())
await new Promise((r, j) => {
  ws.addEventListener('open', r, { once: true })
  ws.addEventListener('error', j, { once: true })
})

let nextId = 0
const pending = new Map()
const consoleErrors = []
const exceptions = []
ws.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data)
  if (m.id && pending.has(m.id)) {
    pending.get(m.id)(m)
    pending.delete(m.id)
    return
  }
  if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error') {
    consoleErrors.push(m.params.args.map((a) => a.value ?? a.description ?? '').join(' '))
  }
  if (m.method === 'Runtime.exceptionThrown') {
    exceptions.push(m.params.exceptionDetails?.exception?.description ?? JSON.stringify(m.params))
  }
})
const send = (method, params = {}) =>
  new Promise((resolve) => {
    const id = ++nextId
    pending.set(id, resolve)
    ws.send(JSON.stringify({ id, method, params }))
  })

async function js(code) {
  const r = await send('Runtime.evaluate', {
    expression: `(() => { ${code} })()`,
    returnByValue: true,
    awaitPromise: true,
  })
  if (r.result?.exceptionDetails) throw new Error('page: ' + r.result.exceptionDetails.exception?.description)
  return r.result?.result?.value
}
const soft = (code) => js(code).catch(() => false)

/** Drive the card's drag & drop from the page: dragenter → dragover → drop on
 *  the target column with the item id in `text/plain` (what the card's dragstart
 *  stores and the column's drop handler reads). Deterministic; the caller polls
 *  for the async move to land. */
async function dragCardToColumn(itemId, columnName) {
  return js(`
    const card = document.querySelector('[data-item="${itemId}"]');
    const col = document.querySelector('[data-column="${columnName}"]');
    if (!card || !col) return false;
    const dt = new DataTransfer();
    dt.setData('text/plain', ${JSON.stringify(itemId)});
    for (const type of ['dragenter', 'dragover', 'drop']) {
      col.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
    }
    return true
  `)
}

async function waitJs(code, tries = 80) {
  for (let i = 0; i < tries; i++) {
    if (await soft(code)) return true
    await sleep(150)
  }
  return false
}

const HELPERS = `
window.__ui = {
  col: (name) => document.querySelector('[data-column="' + name + '"]'),
  cards: () => Array.from(document.querySelectorAll('article[data-item]')),
  btn: (root, text) => Array.from(root.querySelectorAll('button'))
      .find(b => b.textContent.trim() === text),
  dlg: () => document.querySelector('dialog[open]'),
};
return true`

/** Retry until the element exists, then click. Safe to retry: a throw means
 *  lookup failed, so nothing was clicked. */
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
      await js(`const el = ${expr}; el.value = ${json}; el.dispatchEvent(new Event('input', { bubbles: true })); return true`)
      return true
    }
    await sleep(150)
  }
  return false
}

const state = async () => (await fetch(STUB + '/__state')).json()

await send('Page.enable')
await send('Runtime.enable')
await send('Page.navigate', { url: APP })

async function ensureHelpers() {
  for (let i = 0; i < 120; i++) {
    const ready = await soft(
      `return !!(window.__ui && window.__ui.col && window.__ui.col('Todo') && window.__ui.cards().length > 0)`,
    )
    if (ready === true) return true
    await soft(HELPERS)
    await sleep(200)
  }
  return false
}

if (!(await ensureHelpers())) {
  console.log('HARNESS: could not reach a rendered, instrumented board')
  ws.close()
  process.exit(2)
}
await sleep(900) // let the post-reload workspace selection settle

const checks = {}
const pass = (k) => (checks[k] = true)
const fail = (k, extra = '') => {
  checks[k] = false
  console.log(`  ! ${k}${extra ? ': ' + extra : ''}`)
}

// 0. baseline
;(await waitJs(`return document.body.innerText.includes('Wire SSE reconnect') && !!window.__ui.col('Doing')`, 40))
  ? pass('baseline: board, 3 columns and cards render')
  : fail('baseline')

// 1. ADD ITEM via the per-column form
await clickEl(`window.__ui.btn(window.__ui.col('Todo'), '+ Add item')`)
await waitJs(`return !!window.__ui.col('Todo').querySelector('input[placeholder="Item title"]')`, 40)
await setEl(`window.__ui.col('Todo').querySelector('input[placeholder="Item title"]')`, 'Added by human')
await clickEl(`window.__ui.btn(window.__ui.col('Todo'), 'Add')`)

let addedId = null
{
  const inDom = await waitJs(
    `return Array.from(document.querySelectorAll('article[data-item]')).some(a => (a.querySelector('h3')||{}).textContent?.trim() === 'Added by human')`,
    60,
  )
  const s = await state()
  const item = s.items.find((i) => i.title === 'Added by human')
  addedId = item?.id ?? null
  inDom && item && item.column_id === 'c1'
    ? pass('add item: card in DOM + persisted server-side')
    : fail('add item', `dom=${inDom} item=${!!item}`)
}
const CARD = `document.querySelector('[data-item="${addedId}"]')`

// 2. CLOSE (and the card keeps its identity through the re-render)
await clickEl(`window.__ui.btn(${CARD}, 'close')`)
{
  const inDom = await waitJs(`return /CLOSED/.test((${CARD}?.innerText) || '')`, 60)
  const s = await state()
  const closed = s.items.find((i) => i.id === addedId)?.closed === true
  inDom && closed ? pass('close: badge in DOM + closed persisted') : fail('close', `dom=${inDom} server=${closed}`)
}

// 3. EDIT title/description
await clickEl(`window.__ui.btn(${CARD}, 'edit')`)
await waitJs(`return !!(${CARD})?.querySelector('input')`, 40)
await setEl(`(${CARD}).querySelector('input')`, 'Edited title')
await setEl(`(${CARD}).querySelector('textarea')`, 'now with a description')
await clickEl(`window.__ui.btn(${CARD}, 'Save')`)
{
  const inDom = await waitJs(`return /Edited title/.test((${CARD}?.innerText) || '')`, 60)
  const s = await state()
  const item = s.items.find((i) => i.id === addedId)
  const ok = item?.title === 'Edited title' && item?.description === 'now with a description'
  inDom && ok ? pass('edit: title + description saved and persisted') : fail('edit', `dom=${inDom} server=${JSON.stringify(item)}`)
}

// 4. TRANSFER dialog
await clickEl(`window.__ui.btn(${CARD}, 'transfer')`)
await waitJs(`return !!window.__ui.dlg()?.querySelector('input')`, 40)
await setEl(`window.__ui.dlg().querySelector('input')`, 'be-2')
await setEl(`window.__ui.dlg().querySelector('textarea')`, 'take this please')
await clickEl(`window.__ui.btn(window.__ui.dlg(), 'Transfer')`)
{
  const inDom = await waitJs(`return ((${CARD}?.innerText) || '').includes('@be-2')`, 60)
  const s = await state()
  const item = s.items.find((i) => i.id === addedId)
  const handoff = s.comments.find((c) => c.body.startsWith('[handoff]') && c.item_id === addedId)
  inDom && item?.owner_agent === 'be-2' && handoff
    ? pass('transfer: owner updated + [handoff] comment posted')
    : fail('transfer', `dom=${inDom} owner=${item?.owner_agent} handoff=${!!handoff}`)
}
await soft(`window.__ui.dlg()?.close(); return true`)
await sleep(300)

// 5. COMMENTS drawer (inside the item detail modal, opened via title click)
await clickEl(`${CARD}.querySelector('h3 button')`)
await waitJs(`return !!window.__ui.dlg()?.querySelector('textarea')`, 40)
const listedHandoff = await waitJs(`return (window.__ui.dlg()?.innerText || '').includes('[handoff]')`, 40)
await setEl(`window.__ui.dlg().querySelector('textarea')`, 'looks good to me')
await clickEl(`window.__ui.btn(window.__ui.dlg(), 'Comment')`)
{
  const inDom = await waitJs(`return (window.__ui.dlg()?.innerText || '').includes('looks good to me')`, 60)
  const s = await state()
  const onServer = s.comments.some((c) => c.body === 'looks good to me' && c.author_type === 'human')
  listedHandoff && inDom && onServer
    ? pass('comments: drawer lists handoff + posts new human comment')
    : fail('comments', `listed=${listedHandoff} dom=${inDom} server=${onServer}`)
}
await soft(`window.__ui.dlg()?.close(); return true`)
await sleep(300)

// 6. MOVE by dragging the card onto the Doing column (Todo -> Doing)
{
  let moved = false
  for (let i = 0; i < 60 && !moved; i++) {
    const fired = await dragCardToColumn(addedId, 'Doing').catch(() => false)
    if (fired) moved = await waitJs(`return !!window.__ui.col('Doing').querySelector('[data-item="${addedId}"]')`, 12)
    if (!moved) await sleep(200)
  }
  const s = await state()
  const onServer = s.items.find((i) => i.id === addedId)?.column_id === 'c2'
  moved && onServer ? pass('move: card lands in Doing + persisted') : fail('move', `dom=${moved} server=${onServer}`)
}

// 7. LABEL FILTER (infra) hides the bug-labelled card
await clickEl(`Array.from(document.querySelectorAll('button')).find(x => x.textContent.trim() === 'infra')`)
{
  const ok = await waitJs(`
    const titles = Array.from(document.querySelectorAll('article[data-item]'))
      .map(a => (a.querySelector('h3')||{}).textContent?.trim());
    return !titles.includes('Wire SSE reconnect') && titles.includes('Board layout');`, 40)
  ok ? pass('label filter: only infra cards visible') : fail('label filter')
}

// 8. hygiene
{
  const finalText = await js('return document.body.innerText')
  const actionError = /Action failed/.test(finalText)
  !actionError && exceptions.length === 0 && consoleErrors.length === 0
    ? pass('hygiene: no action errors, exceptions or console errors')
    : fail('hygiene', `actionError=${actionError} exc=${exceptions.length} console=${consoleErrors.length}`)
  console.log('\n=== rendered text (final, filtered) ===\n' + finalText)
}

console.log(
  [
    '=== CHECKS ===',
    ...Object.entries(checks).map(([k, v]) => `${v ? 'PASS' : 'FAIL'}  ${k}`),
    '',
    '=== console errors ===',
    consoleErrors.length ? consoleErrors.join('\n') : '(none)',
    '=== exceptions ===',
    exceptions.length ? exceptions.join('\n') : '(none)',
  ].join('\n'),
)

ws.close()
process.exit(Object.values(checks).every(Boolean) ? 0 : 1)
