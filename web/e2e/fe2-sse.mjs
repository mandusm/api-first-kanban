// Live SSE checks against the REAL API: incremental item events, R3a label.created,
// and reconnect / snapshot resync (API restart, out-of-band DB insert).
import { setTimeout as sleep } from 'node:timers/promises'
import { execFileSync } from 'node:child_process'
import {
  API, APP_URL, FE2_DATABASE_URL, checks, loadSeedEnv, makeSql,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const env = loadSeedEnv()
const { BOARD, COL1, WS, TS } = env
// Binary the script (re)starts for the reconnect/resync check. In the
// isolated e2e stack (FE2_API + FE2_DB, see README) the pid file and the
// restart DSN/addr are overridden so the test instance (not the live one) is
// bounced.
const API_BIN = process.env.FE2_API_BIN || '/tmp/kan-api'
const API_PID_FILE = process.env.FE2_API_PID || '/tmp/fe2-api.pid'
const API_PORT = new URL(API).port || '18081'
const API_DSN = FE2_DATABASE_URL
const { sql } = makeSql()
const sh = (c) => execFileSync('bash', ['-lc', c], { encoding: 'utf8' })

const { results, pass, fail } = checks()

// --- CDP plumbing (shared, with stale-chrome self-heal) ---------------------
console.error('[t] connecting cdp')
const exceptions = []
const { ws, send, onMessage } = await connectCdpPage({ url: APP_URL })
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
})
const { js, soft, waitJs } = makeEval(send)
const HELPERS = `window.__ui={col:(n)=>document.querySelector('[data-column="'+n+'"]'),titleOf:(a)=>((a.querySelector('h3')||{}).textContent||'').trim(),btn:(r,t)=>Array.from(r.querySelectorAll('button')).find(b=>b.textContent.trim()===t)};return true`
const badge = () => js(`const e=document.querySelector('main header span.inline-flex');return e?e.textContent.trim():'(none)'`)
const domTitles = () => js(`return Array.from(document.querySelectorAll('article[data-item]')).map(a=>((a.querySelector('h3')||{}).textContent||'').trim())`)

console.error('[t] ws open');
async function ready() { for (let i = 0; i < 150; i++) { if (await soft(`return !!(window.__ui && document.querySelector('#ws-select') && document.querySelectorAll('#ws-select option').length>0)`)) return true; await soft(HELPERS); await sleep(200) } return false }
console.error('[t] first ready()'); await ready(); console.error('[t] ready done')
await js(`const s=document.querySelector('#ws-select');s.value=${JSON.stringify(WS)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return Array.from((document.querySelector('#board-select')||{options:[]}).options).some(o=>o.value===${JSON.stringify(BOARD)})`)
await js(`const s=document.querySelector('#board-select');s.value=${JSON.stringify(BOARD)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return !!window.__ui.col('todo')`, 100)
console.error('[t] board rendered, waiting for live badge');
const live0 = await waitJs(`const e=document.querySelector('main header span.inline-flex');return e && e.textContent.trim()==='live'`, 120)
console.log('badge at start:', await badge(), '| live reached:', live0)

// --- 1. LIVE item.created (mutate via API only; no client action, no reload) ----
{
  const T = `live-created-${TS}-${Date.now() % 100000}`
  await fetch(`${API}/api/boards/${BOARD}/items`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: T, column_id: COL1, agent: 'human' }),
  })
  const appeared = await waitJs(`return Array.from(document.querySelectorAll('article[data-item]')).some(a=>((a.querySelector('h3')||{}).textContent||'').trim()===${JSON.stringify(T)})`, 80)
  const db = sql(`select count(*) from items where board_id='${BOARD}' and title='${T}'`)
  appeared && db === '1' ? pass('live SSE item.created: card appears with no reload and no client action') : fail('live item.created', `dom=${appeared} db=${db}`)
}
// --- 2. LIVE item.updated (title change via API) --------------------------------
{
  const id1 = sql(`select id from items where board_id='${BOARD}' order by created_at desc limit 1`)
  const T2 = `live-updated-${TS}`
  await fetch(`${API}/api/items/${id1}`, {
    method: 'PATCH', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ title: T2, agent: 'human' }),
  })
  const seen = await waitJs(`return Array.from(document.querySelectorAll('article[data-item]')).some(a=>((a.querySelector('h3')||{}).textContent||'').trim()===${JSON.stringify(T2)})`, 80)
  seen ? pass('live SSE item.updated: title change reflected live') : fail('live item.updated')
}
// --- 3. LIVE item.moved: move newest item to the other column ------------------
{
  const id1 = sql(`select id from items where board_id='${BOARD}' order by created_at desc limit 1`)
  const col2 = sql(`select id from columns where board_id='${BOARD}' order by position limit 1 offset 1`)
  await fetch(`${API}/api/items/${id1}/move`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ column_id: col2, index: 0, agent: 'human' }),
  })
  const inDoing = await waitJs(`return !!document.querySelector('[data-column="doing"] article[data-item="${id1}"]')`, 80)
  const dbCol = sql(`select column_id from items where id='${id1}'`)
  inDoing && dbCol === col2 ? pass('live SSE item.moved: card relocates to the Doing column live') : fail('live item.moved', `dom=${inDoing} db=${dbCol}`)
}
// --- 4. R3a: label.created reaches a board-only subscriber ----------------------
{
  const L = `live-label-${TS}`
  const r = await fetch(`${API}/api/workspaces/${WS}/labels`, {
    method: 'POST', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ name: L, color: '#10b981' }),
  })
  const j = await r.json()
  const chipLive = await waitJs(`return !!Array.from(document.querySelectorAll('button')).find(b=>b.textContent.trim()===${JSON.stringify(L)})`, 80)
  chipLive ? pass('R3a live SSE label.created: new label filter chip appears without reload (board-only subscription)') : fail('R3a label.created live')
  globalThis.__labelId = j?.label?.id
}
// --- 5. Reconnect + snapshot resync ---------------------------------------------
{
  console.log('\n--- restarting the API (SIGTERM, then SIGKILL if it hangs) ---')
  const t0 = Date.now()
  sh(`kill -TERM $(cat ${API_PID_FILE}) 2>/dev/null || true`)
  let exited = false
  for (let i = 0; i < 24; i++) { try { sh(`kill -0 $(cat ${API_PID_FILE}) 2>/dev/null`); await sleep(500) } catch { exited = true; break } }
  const termMs = Date.now() - t0
  console.log(`  SIGTERM: process exited=${exited} after ~${termMs}ms`)
  if (!exited) { sh(`kill -KILL $(cat ${API_PID_FILE}) 2>/dev/null || true`); console.log('  -> SIGTERM did NOT terminate it (graceful shutdown hangs); used SIGKILL. BACKEND FINDING #3.') }
  const wentDown = await waitJs(`const e=document.querySelector('main header span.inline-flex');return e && e.textContent.trim()==='reconnecting'`, 200)
  wentDown ? pass('reconnect: badge drops to "reconnecting" when the API dies') : fail('reconnect badge', `badge=${await badge()}`)

  // Out-of-band row: no event is emitted, so only a snapshot refetch can reveal it.
  const T3 = `resync-${TS}-${Date.now() % 100000}`
  // key is NOT NULL + UNIQUE since the unique-item-keys feature, so the
  // out-of-band rows carry a synthetic unique key (titles embed the run TS).
  sql(`insert into items (board_id, column_id, title, key, position, created_by) values ('${BOARD}','${COL1}','${T3}','zz-${T3}', (select coalesce(max(position),0)+1 from items where column_id='${COL1}'), 'human')`)
  const presentBefore = (await domTitles()).includes(T3)
  console.log(`  inserted "${T3}" directly into Postgres while the API was down; visible in DOM before restart: ${presentBefore} (expected false)`)

  sh(`nohup env KANBAN_ADDR=127.0.0.1:${API_PORT} KANBAN_DATABASE_URL='${API_DSN}' ${API_BIN} > /tmp/fe2-api.log 2>&1 & echo $! > ${API_PID_FILE}; sleep 1`)
  const backLive = await waitJs(`const e=document.querySelector('main header span.inline-flex');return e && e.textContent.trim()==='live'`, 200)
  backLive ? pass('reconnect: badge returns to "live" after the API restarts') : fail('reconnect live', `badge=${await badge()}`)
  const resynced = await waitJs(`return Array.from(document.querySelectorAll('article[data-item]')).some(a=>((a.querySelector('h3')||{}).textContent||'').trim()===${JSON.stringify(T3)})`, 120)
  resynced ? pass('resync: out-of-band DB row appears after reconnect (snapshot refetch)') : fail('resync snapshot', 'row never appeared')
}

// --- 6. Defensive guard: malformed SSE entity → snapshot resync, no crash ------
{
  // Out-of-band row with NO event emitted: only a full snapshot refetch can
  // reveal it, so its appearance proves the resync actually ran.
  const T4 = `guard-resync-${TS}-${Date.now() % 100000}`
  sql(`insert into items (board_id, column_id, title, key, position, created_by) values ('${BOARD}','${COL1}','${T4}','zz-${T4}', (select coalesce(max(position),0)+1 from items where column_id='${COL1}'), 'human')`)

  // Malformed live entity delivered through the REAL SSE path: insert the
  // event row directly (append-only table), then pg_notify the reference
  // envelope ({"event_id":N}) the hub hydrates by PK and fans out. The item
  // entity is present but `labels` is not an array and `closed` is missing —
  // exactly the shape that used to crash Card rendering.
  const victim = sql(`select id from items where board_id='${BOARD}' order by created_at desc limit 1`)
  const payload = JSON.stringify({
    type: 'item.updated', agent: 'human', workspace_id: WS, board_id: BOARD,
    item: { id: victim, board_id: BOARD, column_id: COL1, title: 'malformed-sse-entity', position: '1' },
  })
  const evtId = sql(`insert into events (workspace_id, board_id, type, agent, payload) values ('${WS}','${BOARD}','item.updated','human','${payload.replace(/'/g, "''")}') returning id`)
  sql(`select pg_notify('kanban_events', json_build_object('event_id', ${evtId})::text)`)
  console.log(`  injected malformed item.updated event #${evtId} (item without labels/closed) via pg_notify`)

  const resynced = await waitJs(`return Array.from(document.querySelectorAll('article[data-item]')).some(a=>((a.querySelector('h3')||{}).textContent||'').trim()===${JSON.stringify(T4)})`, 120)
  const store = await js(`
    return import('/src/lib/kanban.svelte.ts').then((m) => {
      const k = m.kanban
      return { n: k.items.length, bad: k.items.filter(i => !Array.isArray(i.labels) || typeof i.closed !== 'boolean').length }
    })`,
  ).catch((e) => ({ err: String(e) }))
  const clean = !exceptions.length
  resynced && store?.n > 0 && store?.bad === 0 && clean
    ? pass('guard: malformed SSE item (no labels/closed) triggers snapshot resync; store stays well-formed, no crash')
    : fail('guard: malformed SSE entity resync', `resync=${resynced} store=${JSON.stringify(store)} exc=${exceptions.length}`)
}

console.log('\n=== page exceptions ===')
console.log(exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)')
console.log('\n=== SUMMARY ===')
const f = results.filter(([s]) => s === 'FAIL').length
console.log(`\n${results.length - f}/${results.length} passed`)
ws.close()
process.exit(f ? 1 : 0)
