// Shared e2e harness (PB-81): the req/fail/reporting helpers, seed prologue,
// boot-wait + workspace/board selection (with the pb74 boot-race hardening),
// the psql helper (formerly triplicated with diverging flags), and the stack
// defaults. Suites import from here instead of re-typing local copies.
//
// CDP plumbing stays in fe2-cdp.mjs (historical convention); it is re-exported
// here so a suite only needs one import.
//
// Stack defaults (PB-81): suites DEFAULT TO THE ISOLATED STACK (FE2_API=:18081,
// APP_URL=:5175, FE2_DB=kanban_test) so a bare `node <suite>.mjs` can never
// write into the live board DB. Live-stack use is an explicit opt-in:
//   FE2_STACK=live          → live defaults (:18080 / :5173 / kanban DB)
//   FE2_API / APP_URL / FE2_DB / FE2_DATABASE_URL   → explicit override (wins)
import { setTimeout as sleep } from 'node:timers/promises'
import { execFileSync } from 'node:child_process'
import { readFileSync } from 'node:fs'
import { connectCdpPage, dragCardToColumn, makeEval } from '../fe2-cdp.mjs'

export { connectCdpPage, dragCardToColumn, makeEval }

// --- stack defaults ----------------------------------------------------------

export const LIVE = process.env.FE2_STACK === 'live'
export const API = process.env.FE2_API || (LIVE ? 'http://127.0.0.1:18080' : 'http://127.0.0.1:18081')
export const APP_URL = process.env.APP_URL || (LIVE ? 'http://127.0.0.1:5173/' : 'http://127.0.0.1:5175/')
export const FE2_DB = process.env.FE2_DB || (LIVE ? 'kanban' : 'kanban_test')
export const FE2_DATABASE_URL = process.env.FE2_DATABASE_URL
  || (LIVE
    ? 'postgres://kanban:kanban@127.0.0.1:54329/kanban?sslmode=disable'
    : 'postgres://kanban:kanban@127.0.0.1:54329/kanban_test?sslmode=disable')
export const FE2_SEED = process.env.FE2_SEED || '/tmp/fe2-seed.env'

// --- REST client -------------------------------------------------------------

export const req = async (method, path, body) => {
  const r = await fetch(`${API}${path}`, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  const text = await r.text()
  if (!r.ok) throw new Error(`${method} ${path} -> ${r.status}: ${text}`)
  return text ? JSON.parse(text) : {}
}

// Minimal failure reporter (exit-code style suites: pb67/69/70/72/74/75).
export const fail = (msg) => { console.error(`FAIL ${msg}`); process.exitCode = 1 }

// Results-table reporter (fe2-* suites): pass/fail accumulate a SUMMARY block.
export const checks = () => {
  const results = []
  const pass = (k) => { results.push(['PASS', k]); console.log('PASS  ' + k) }
  const fail = (k, extra = '') => { results.push(['FAIL', k]); console.log(`FAIL  ${k}${extra ? ' — ' + extra : ''}`) }
  const summary = () => {
    console.log('\n=== SUMMARY ===')
    for (const [s, k] of results) console.log(`${s}  ${k}`)
    const f = results.filter(([s]) => s === 'FAIL').length
    console.log(`\n${results.length - f}/${results.length} passed`)
    return f
  }
  return { results, pass, fail, summary }
}

// --- psql (DB is read/patched through the api-first-kanban-db container) ------

/**
 * makeSql({ db, tab }) → { sql, sql1 }.
 * sql(q): run one query, trimmed output. sql1(q): first line only.
 * Default output parses with -tAq; pass tab:true for tab-separated multi-column
 * rows (the fe2-cdp-a convention).
 */
export function makeSql({ db = FE2_DB, tab = false } = {}) {
  const flags = tab ? ['-tA', '-F', '\t'] : ['-tAq']
  const sql = (q) => execFileSync(
    'podman',
    ['exec', '-i', 'api-first-kanban-db', 'psql', '-U', 'kanban', '-d', db, ...flags, '-c', q],
    { encoding: 'utf8' },
  ).trim()
  const sql1 = (q) => sql(q).split('\n')[0] ?? ''
  return { sql, sql1 }
}

// --- seed prologue + fe2-seed.env ---------------------------------------------

/**
 * Seed a throwaway workspace (+ board with the given name, optional explicit
 * column names) via the REAL REST API. Returns { ws, board, columns } where
 * columns are the board's default columns sorted by position (empty when
 * explicit columns were requested).
 */
export async function seedWorkspace({ name, agent, columns } = {}) {
  const ws = (await req('POST', '/api/workspaces', { name, ...(agent ? { agent } : {}) })).workspace
  const board = (await req('POST', `/api/workspaces/${ws.id}/boards`, {
    name: `${name}-board`, ...(columns ? { columns } : {}),
  })).board
  const cols = [...(board.columns ?? [])].sort((a, b) => a.position - b.position)
  return { ws, board, columns: cols }
}

/** Parse the FE2_SEED env file written by fe2-seed.mjs → { WS, BOARD, COL1, … } */
export function loadSeedEnv(file = FE2_SEED) {
  return Object.fromEntries(
    readFileSync(file, 'utf8').split('\n').filter(Boolean).map((l) => l.split('=')),
  )
}

// --- boot-wait + workspace/board selection ------------------------------------

/**
 * Wait for the SPA to boot far enough that the workspace selector is
 * populated. Carries the pb74 boot-race hardening: a freshly healed chrome can
 * race the SPA boot, so if the selector never populates the page is reloaded
 * ONCE and the wait retried. Pass `send` (CDP sender) to enable that reload.
 */
export async function waitAppReady({ js, soft, waitJs, send, inject, tries = 150 }) {
  const ready = async () => {
    if (inject) await soft(inject)
    return soft(`return !!(document.querySelector('#ws-select') || document.querySelectorAll('select').length > 0) && document.querySelectorAll('select option').length > 0`)
  }
  for (let i = 0; i < tries; i++) {
    if (await ready()) return true
    await sleep(200)
  }
  if (js && send) {
    // pb74 hardening: reload once and retry before giving up.
    await js(`location.reload(); return true`)
    for (let i = 0; i < tries; i++) {
      if (await ready()) return true
      await sleep(200)
    }
  }
  return false
}

/**
 * Select OUR workspace + board in the app (works with the #ws-select /
 * #board-select ids and with a generic select lookup). Waits for our board to
 * appear in the board selector after the workspace change. Returns false when
 * either selector never showed our ids; the caller reports the failure.
 */
export async function selectWorkspaceAndBoard({ js, waitJs, wsId, boardId }) {
  await js(`
    const sel = document.querySelector('#ws-select')
      ?? [...document.querySelectorAll('select')].find((s) =>
        [...s.options].some((o) => o.value === '${wsId}'))
    if (!sel) throw new Error('workspace selector not found')
    sel.value = '${wsId}'
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    return true
  `)
  if (!(await waitJs(`return [...document.querySelectorAll('#board-select option')].some(o => o.value === '${boardId}')`)))
    return false
  await js(`
    const sel = document.querySelector('#board-select')
    sel.value = '${boardId}'
    sel.dispatchEvent(new Event('change', { bubbles: true }))
    return true
  `)
  return true
}

// --- item detail modal ---------------------------------------------------------

/**
 * Open an item's detail modal by clicking its card (or the card's
 * "Open item details" button) and wait for the given ready selector
 * (default: the modal's labels section). Returns false when the card never
 * rendered or the modal never showed.
 */
export async function openItemModal({ js, waitJs, itemId, readySel = 'dialog[open] [data-labels]' }) {
  if (!(await waitJs(`return !!document.querySelector('[data-item="${itemId}"]')`))) return false
  await js(`
    const card = document.querySelector('[data-item="${itemId}"]')
    const open = card?.querySelector('button[title="Open item details"]') ?? card
    open.click()
    return true
  `)
  return waitJs(`return !!document.querySelector('${readySel}')`)
}

/** Close the open modal via its Close button (fallback: dialog.close()) and wait for it to be gone. */
export async function closeModal({ js, waitJs }) {
  await js(`
    const dlg = document.querySelector('dialog[open]')
    dlg.querySelector('button[aria-label="Close"]')?.click() ?? dlg.close()
    return true
  `)
  return waitJs(`return !document.querySelector('dialog[open]')`)
}

/** API truth for an item's label set (the modal/picker flows assert against this). */
export const apiLabelsOf = async (itemId) => (await req('GET', `/api/items/${itemId}`)).item.labels ?? []
