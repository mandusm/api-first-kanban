// Focused, deterministic move up/down test. One click, then verify DOM vs
// Postgres; finally a reload to check the snapshot order equals DB order.
import { setTimeout as sleep } from 'node:timers/promises'
import { APP_URL, loadSeedEnv, makeSql } from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const env = loadSeedEnv()
const { BOARD, COL1, WS, TS } = env
const { sql } = makeSql()
const dbOrder = () => sql(`select title from items where board_id='${BOARD}' and column_id='${COL1}' order by position::numeric, created_at`).split('\n').filter(Boolean)

const { send } = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(send)
const HELPERS = `window.__ui={col:(n)=>document.querySelector('[data-column="'+n+'"]'),cards:()=>Array.from(document.querySelectorAll('article[data-item]')),titleOf:(a)=>((a.querySelector('h3')||{}).textContent||'').trim(),btn:(r,t)=>Array.from(r.querySelectorAll('button')).find(b=>b.textContent.trim()===t),dlg:()=>document.querySelector('dialog[open]')};return true`

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
const report = async (label) => {
  const d = await domOrder()
  const s = dbOrder()
  console.log(`${label}\n  DOM: ${JSON.stringify(d)}\n  DB : ${JSON.stringify(s)}\n  ${JSON.stringify(d) === JSON.stringify(s) ? 'MATCH ✓' : 'DIVERGE ✗'}`)
  return { d, s }
}

console.log('--- baseline after fresh reload ---')
const base = await report('baseline')
base.d.length > 0 && JSON.stringify(base.d) === JSON.stringify(base.s)
  ? pass('moves: baseline DOM order matches Postgres order')
  : fail('moves baseline', `dom=${JSON.stringify(base.d)} db=${JSON.stringify(base.s)}`)

// move the LAST item up one slot (single click)
const lastTitle = base.d[base.d.length - 1]
console.log(`\n--- click ↑ once on the last item (${lastTitle}) ---`)
await js(`const cards=window.__ui.col('todo').querySelectorAll('article[data-item]');const a=cards[cards.length-1];window.__ui.btn(a,'↑').click();return true`)
await sleep(1500)
const r1 = await report('after one ↑')
const upOk = r1.d[r1.d.length - 2] === lastTitle && JSON.stringify(r1.d) === JSON.stringify(r1.s)
upOk
  ? pass('moves: ↑ moves the last item up one slot; DOM and Postgres agree')
  : fail('moves up', `dom=${JSON.stringify(r1.d)} db=${JSON.stringify(r1.s)}`)

// move the FIRST item down one slot (single click)
const firstTitle = r1.d[0]
console.log(`\n--- click ↓ once on the first item (${firstTitle}) ---`)
await js(`const a=window.__ui.col('todo').querySelector('article[data-item]');window.__ui.btn(a,'↓').click();return true`)
await sleep(1500)
const r2 = await report('after one ↓')
const downOk = r2.d[1] === firstTitle && JSON.stringify(r2.d) === JSON.stringify(r2.s)
downOk
  ? pass('moves: ↓ moves the first item down one slot; DOM and Postgres agree')
  : fail('moves down', `dom=${JSON.stringify(r2.d)} db=${JSON.stringify(r2.s)}`)

// reload and re-check that the client's snapshot order equals the DB order
console.log('\n--- reload, assert snapshot order == DB order ---')
await send('Page.navigate', { url: APP_URL })
await ready()
await js(`const s=document.querySelector('#ws-select');s.value=${JSON.stringify(WS)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return Array.from((document.querySelector('#board-select')||{options:[]}).options).some(o=>o.value===${JSON.stringify(BOARD)})`)
await js(`const s=document.querySelector('#board-select');s.value=${JSON.stringify(BOARD)};s.dispatchEvent(new Event('change',{bubbles:true}));return true`)
await waitJs(`return !!window.__ui.col('todo')`, 100)
await sleep(800)
const r3 = await report('after reload')
r3.d.length > 0 && JSON.stringify(r3.d) === JSON.stringify(r3.s)
  ? pass('moves: after reload the snapshot order equals the Postgres order')
  : fail('moves reload order', `dom=${JSON.stringify(r3.d)} db=${JSON.stringify(r3.s)}`)

console.log(`\n${results.length - results.filter(([st]) => st === 'FAIL').length}/${results.length} passed`)
process.exit(results.some(([st]) => st === 'FAIL') ? 1 : 0)
