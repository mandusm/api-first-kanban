// Does a freshly created item land in the SAME place in the client and in
// Postgres, without a reload? (Exercises the create response/event position
// vs renumbering.)
import { setTimeout as sleep } from 'node:timers/promises'
import { APP_URL, loadSeedEnv, makeSql } from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const env = loadSeedEnv()
const { BOARD, COL1, WS, TS } = env
const { sql } = makeSql()
const dbOrder = () => sql(`select title from items where board_id='${BOARD}' and column_id='${COL1}' order by position::numeric, created_at`).split('\n').filter(Boolean)

const { send } = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(send)
const HELPERS = `window.__ui={col:(n)=>document.querySelector('[data-column="'+n+'"]'),titleOf:(a)=>((a.querySelector('h3')||{}).textContent||'').trim(),btn:(r,t)=>Array.from(r.querySelectorAll('button')).find(b=>b.textContent.trim()===t)};return true`

async function ready() {
  for (let i = 0; i < 150; i++) {
    if (await soft(`return !!(window.__ui && document.querySelector('#ws-select') && document.querySelectorAll('#ws-select option').length>0)`)) return true
    await soft(HELPERS)
    await sleep(200)
  }
  return false
}
if (!(await ready())) { console.error('page never became ready'); process.exit(2) }
await js(`const s=document.querySelector('#ws-select');s.value=${JSON.stringify(WS)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return Array.from((document.querySelector('#board-select')||{options:[]}).options).some(o=>o.value===${JSON.stringify(BOARD)})`)
await js(`const s=document.querySelector('#board-select');s.value=${JSON.stringify(BOARD)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return !!window.__ui.col('todo')`, 100)
await sleep(800)

const domOrder = () => js(`return Array.from(window.__ui.col('todo').querySelectorAll('article[data-item]')).map(a=>window.__ui.titleOf(a))`)

const results = []
const pass = (k) => { results.push(['PASS', k]); console.log('PASS  ' + k) }
const fail = (k, extra = '') => { results.push(['FAIL', k]); console.log(`FAIL  ${k}${extra ? ' — ' + extra : ''}`) }

const base = await domOrder()
const dbBase = dbOrder()
console.log('baseline DOM:', JSON.stringify(base))
console.log('baseline DB :', JSON.stringify(dbBase))
base.length > 0 && JSON.stringify(base) === JSON.stringify(dbBase)
  ? pass('create-order: baseline DOM order matches Postgres order')
  : fail('create-order baseline', `dom=${JSON.stringify(base)} db=${JSON.stringify(dbBase)}`)

const T = `create-order-${TS}-${Date.now() % 100000}`
console.log(`\n--- add "${T}" via the UI form (no reload afterwards) ---`)
await js(`window.__ui.btn(window.__ui.col('todo'),'+ Add item').click();return true`)
await waitJs(`return !!window.__ui.col('todo').querySelector('input[placeholder="Item title"]')`, 40)
await js(`const el=window.__ui.col('todo').querySelector('input[placeholder="Item title"]');el.value=${JSON.stringify(T)};el.dispatchEvent(new Event('input',{bubbles:true}));return true`)
await js(`window.__ui.btn(window.__ui.col('todo'),'Add').click();return true`)
await waitJs(`return Array.from(window.__ui.col('todo').querySelectorAll('article[data-item]')).some(a=>window.__ui.titleOf(a)===${JSON.stringify(T)})`, 80)
await sleep(1500) // let the SSE item.created land too

const d = await domOrder()
const s = dbOrder()
const idx = (a) => a.indexOf(T)
console.log('DOM:', JSON.stringify(d))
console.log('DB :', JSON.stringify(s))
idx(d) === idx(s) && idx(d) !== -1
  ? pass('create-order: new item index identical in DOM and Postgres (no renumber divergence)')
  : fail('create-order index', `dom=${idx(d)} db=${idx(s)}`)
JSON.stringify(d) === JSON.stringify(s)
  ? pass('create-order: full column order matches after SSE settles')
  : fail('create-order full order', `dom=${JSON.stringify(d)} db=${JSON.stringify(s)}`)
console.log('positions in DB:', sql(`select position from items where board_id='${BOARD}' and column_id='${COL1}' order by position::numeric`).split('\n').join(' '))

console.log(`\n${results.length - results.filter(([st]) => st === 'FAIL').length}/${results.length} passed`)
process.exit(results.some(([st]) => st === 'FAIL') ? 1 : 0)
