// PB-121: board column strip horizontal scroll (keyboard + wheel + visible scrollbar).
//
// Bug (PB-120 epic): with >4 columns the strip overflows but is effectively
// unscrollable — invisible overlay scrollbar, not keyboard-accessible, wheel
// does nothing. Fix: labelled focusable region (tabindex=0 + role=region +
// Arrow/Home/End), deltaY→scrollLeft wheel translation (only when overflowing),
// thin scoped scrollbar styling (.board-column-strip in app.css).
//
// Self-seeding like pb75: throwaway workspace + boards via the REAL REST API
// (6 columns for overflow, 4 columns for the no-hijack board), then the live
// app through CDP (headless chrome, CDP_PORT 9224). ISOLATED STACK (PB-81
// convention): FE2_API=:18081, APP_URL=:5175 — never the live API.
//
// Checks:
//   a. strip.scrollWidth > clientWidth with 6 columns (overflow exists).
//   b. wheel deltaY over the strip increases scrollLeft (and wheel is
//      translated; page does not scroll simultaneously).
//   c. focusing the strip + ArrowRight scrolls; End jumps to the end.
//   d. the LAST column's right edge becomes visible after End.
//   e. with <=4 columns (no overflow): wheel/keys leave the strip's
//      scrollLeft at 0 and the page scroll intact (no hijack).
//
// Exit conventions: CDP socket closed before exit (fe2/pb75 style).
import {
  APP_URL, req, fail, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const NAME = `pb121-${TS}`
const { results, pass, fail: failCheck, summary } = checks()
const report = (ok, key, extra = '') => ok ? pass(key) : failCheck(key, extra)

// --- seed -----------------------------------------------------------------

const { ws } = await seedWorkspace({ name: NAME })
const mkBoard = async (n, tag) => {
  const names = Array.from({ length: n }, (_, i) => `${NAME}-${tag}-${i}`)
  const board = (await req('POST', `/api/workspaces/${ws.id}/boards`, { name: `${NAME}-${tag}-board`, columns: names })).board
  const cols = [...board.columns].sort((a, b) => a.position - b.position)
  return { board, cols }
}
const overflow = await mkBoard(6, 'ovf')
const snug = await mkBoard(4, 'snug')
console.log(`seeded ws=${ws.id} overflow-board=${overflow.board.id} (6 cols) snug-board=${snug.board.id} (4 cols)`)

// --- browser ---------------------------------------------------------------

const page = await connectCdpPage({ url: APP_URL })
// Wider viewport: at the default headless width even 4 columns overflow, which
// would make the no-hijack premise untestable. 1600px fits 4 columns (and 6
// still overflow, keeping check (a) meaningful).
await page.send('Emulation.setDeviceMetricsOverride', { width: 1600, height: 1000, deviceScaleFactor: 1, mobile: false })
page.onMessage((m) => {
  if (m.method === 'Runtime.consoleAPICalled' && m.params.type === 'error')
    console.error('page console error:', JSON.stringify(m.params.args?.map((a) => a.value ?? a.description)))
})
const { js, soft, waitJs } = makeEval(page.send)

if (!(await waitAppReady({ js, soft, waitJs, send: page.send }))) fail('app never booted')
if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: overflow.board.id })))
  fail('workspace/board selectors never showed our ids')
if (!(await waitJs(`return document.querySelectorAll('[data-column]').length === 6`, 200)))
  fail('board never rendered 6 columns')

// Make the page itself scrollable so the no-hijack check is meaningful
// (otherwise "page did not scroll" is trivially true).
await js(`
  const sp = document.createElement('div')
  sp.id = 'pb121-tall-spacer'
  sp.style.height = '3000px'
  document.body.appendChild(sp)
  return true
`)

const STRIP = `document.querySelector('.board-column-strip')`

// (0) labelled focusable region affordances.
{
  const attrs = await js(`
    const s = ${STRIP}
    return { tab: s.getAttribute('tabindex'), role: s.getAttribute('role'),
             label: s.getAttribute('aria-label') }
  `)
  report(attrs?.tab === '0' && attrs?.role === 'region' && !!attrs?.label,
    'strip is a labelled focusable region (tabindex=0, role=region, aria-label)')
}

// (a) overflow exists.
{
  const o = await js(`
    const s = ${STRIP}
    return { sw: s.scrollWidth, cw: s.clientWidth }
  `)
  report(o.sw > o.cw, 'strip overflows with 6 columns (scrollWidth > clientWidth)', `sw=${o.sw} cw=${o.cw}`)
}

// (b) wheel deltaY → scrollLeft (page must NOT scroll with it).
{
  await js(`${STRIP}.scrollLeft = 0; window.scrollTo(0, 0); return true`)
  const before = await js(`return { sl: ${STRIP}.scrollLeft, sy: window.scrollY }`)
  await js(`
    const s = ${STRIP}
    const r = s.getBoundingClientRect()
    s.dispatchEvent(new WheelEvent('wheel', {
      bubbles: true, cancelable: true, deltaY: 240, deltaX: 0,
      clientX: r.x + r.width / 2, clientY: r.y + r.height / 2,
    }))
    return true
  `)
  await js(`return new Promise((res) => setTimeout(res, 120))`)
  const after = await js(`return { sl: ${STRIP}.scrollLeft, sy: window.scrollY }`)
  report(after.sl > before.sl, 'wheel deltaY over strip increases scrollLeft', `sl ${before.sl} -> ${after.sl}`)
  report(after.sy === 0 && before.sy === 0, 'wheel over strip does not scroll the page (contained)')
}

// (c) keyboard: focus + ArrowRight scrolls; End jumps to end.
{
  await js(`${STRIP}.scrollLeft = 0; return true`)
  await js(`${STRIP}.focus(); return true`)
  const arrow = await js(`
    const s = ${STRIP}
    const before = s.scrollLeft
    s.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }))
    return { before, after: s.scrollLeft }
  `)
  report(arrow.after > arrow.before, 'ArrowRight on focused strip scrolls', `sl ${arrow.before} -> ${arrow.after}`)

  const end = await js(`
    const s = ${STRIP}
    s.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true, cancelable: true }))
    const max = s.scrollWidth - s.clientWidth
    return { sl: s.scrollLeft, max }
  `)
  report(end.sl >= end.max - 2, 'End key jumps to the end', `sl=${end.sl} max=${end.max}`)
}

// (d) last column reachable: right edge inside the strip's viewport.
{
  const vis = await js(`
    const s = ${STRIP}
    const cols = s.querySelectorAll('[data-column]')
    const last = cols[cols.length - 1]
    const lr = last.getBoundingClientRect()
    const sr = s.getBoundingClientRect()
    return { left: lr.right - sr.left, cw: s.clientWidth }
  `)
  report(vis.left <= vis.cw + 1 && vis.left > 0, 'last column right edge visible after End',
    `right=${vis.left} clientWidth=${vis.cw}`)
}

// (f) PB-123 focused scrolling: wheel dispatched with target INSIDE a column
// must NOT change strip.scrollLeft (no intercept, no preventDefault — native
// page scroll happens instead, out of suite scope; scrollLeft unchanged is the
// observable).
{
  await js(`${STRIP}.scrollLeft = 0; window.scrollTo(0, 0); return true`)
  const res = await js(`
    const s = ${STRIP}
    const col = s.querySelector('[data-column]')
    const target = col.querySelector('h3') || col
    const before = s.scrollLeft
    // Note the dispatched event is NOT trusted, so the browser's native
    // scroll handling will not run regardless — asserting scrollLeft is
    // unchanged after the handler runs is exactly the observable we want:
    // with the fix, the handler returns before any preventDefault/scrollLeft
    // mutation when the target is inside a column.
    target.dispatchEvent(new WheelEvent('wheel', {
      bubbles: true, cancelable: true, deltaY: 240, deltaX: 0,
    }))
    return { before, after: s.scrollLeft, target: target.tagName }
  `)
  report(res.after === res.before,
    'wheel over a column does not change strip scrollLeft (focused scrolling)',
    `target=${res.target} sl ${res.before} -> ${res.after}`)
}

// (e) no hijack with <=4 columns (no overflow).
{
  await js(`
    const sel = document.querySelector('#board-select')
    sel.value = '${snug.board.id}'
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    return true
  `)
  if (!(await waitJs(`return document.querySelectorAll('[data-column]').length === 4`)))
    fail('snug board never rendered 4 columns')

  await js(`
    const s = ${STRIP}
    s.scrollLeft = 0
    window.scrollTo(0, 400)
    return true
  `)
  const before = await js(`return { sl: ${STRIP}.scrollLeft, sy: window.scrollY }`)
  await js(`
    const s = ${STRIP}
    s.dispatchEvent(new WheelEvent('wheel', { bubbles: true, cancelable: true, deltaY: 240, deltaX: 0 }))
    s.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight', bubbles: true, cancelable: true }))
    s.dispatchEvent(new KeyboardEvent('keydown', { key: 'End', bubbles: true, cancelable: true }))
    return true
  `)
  await js(`return new Promise((res) => setTimeout(res, 120))`)
  const after = await js(`return { sl: ${STRIP}.scrollLeft, sy: window.scrollY }`)
  report(after.sl === 0, 'no overflow: wheel/keys leave strip scrollLeft at 0', `sl=${after.sl}`)
  report(after.sy >= before.sy, 'no overflow: page scroll left intact (no hijack)', `sy ${before.sy} -> ${after.sy}`)
}

// --- exit ------------------------------------------------------------------

await new Promise((res) => { try { page.ws.close(); } catch { /* already closed */ } res(null) })
process.exitCode = summary()
