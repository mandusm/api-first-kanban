// CDP-based render + live-update + reconnect/resync check for the M3a UI.
// Uses Node's built-in WebSocket (Node 22+); no puppeteer/playwright needed.
import { setTimeout as sleep } from 'node:timers/promises'

const CDP = process.env.CDP_PORT || '9223'
const APP = process.env.APP_URL || 'http://127.0.0.1:5196/'

async function firstPageTarget() {
  for (let i = 0; i < 80; i++) {
    try {
      const res = await fetch(`http://127.0.0.1:${CDP}/json/list`)
      const targets = await res.json()
      const page = targets.find((t) => t.type === 'page')
      if (page?.webSocketDebuggerUrl) return page.webSocketDebuggerUrl
    } catch {
      /* chrome not up yet */
    }
    await sleep(250)
  }
  throw new Error('no CDP page target')
}

const ws = new WebSocket(await firstPageTarget())
await new Promise((resolve, reject) => {
  ws.addEventListener('open', resolve, { once: true })
  ws.addEventListener('error', reject, { once: true })
})

let nextId = 0
const pending = new Map()
const consoleErrors = []
const exceptions = []

ws.addEventListener('message', (ev) => {
  const msg = JSON.parse(ev.data)
  if (msg.id && pending.has(msg.id)) {
    pending.get(msg.id)(msg)
    pending.delete(msg.id)
    return
  }
  if (msg.method === 'Runtime.consoleAPICalled' && msg.params.type === 'error') {
    consoleErrors.push(msg.params.args.map((a) => a.value ?? a.description ?? '').join(' '))
  }
  if (msg.method === 'Runtime.exceptionThrown') {
    exceptions.push(msg.params.exceptionDetails?.exception?.description ?? JSON.stringify(msg.params))
  }
})

const send = (method, params = {}) =>
  new Promise((resolve) => {
    const id = ++nextId
    pending.set(id, resolve)
    ws.send(JSON.stringify({ id, method, params }))
  })

const text = async () => {
  const r = await send('Runtime.evaluate', {
    expression: 'document.body.innerText',
    returnByValue: true,
  })
  return r.result?.result?.value ?? ''
}

const waitFor = async (needle, tries = 60) => {
  let last = ''
  for (let i = 0; i < tries; i++) {
    last = await text()
    if (last.includes(needle)) return last
    await sleep(250)
  }
  return last
}

await send('Page.enable')
await send('Runtime.enable')
await send('Page.navigate', { url: APP })

const initial = await waitFor('Wire SSE reconnect')
// Snapshot taken while the incremental SSE item is present, before the drop.
const afterEvent = await waitFor('LIVE-EVENT-APPLIED')
// After the drop + reconnect + resync, the server snapshot wins.
const afterResync = await waitFor('RESYNC-AFTER-RECONNECT', 80)

const checks = {
  'board name rendered': initial.includes('M3 Demo Board'),
  'workspace options rendered': initial.includes('Agent Workspace') && initial.includes('Second Workspace'),
  'board options rendered': initial.includes('M3 Demo Board'),
  'column names rendered': ['Todo', 'Doing', 'Done'].every((c) => initial.includes(c)),
  'card title rendered': initial.includes('Wire SSE reconnect'),
  'card description rendered': initial.includes('backoff + resync'),
  'owner rendered as @agent': initial.includes('@be-1') && initial.includes('@fe-1'),
  'unassigned owner rendered': initial.includes('unassigned'),
  'label pills rendered': initial.includes('bug') && initial.includes('infra'),
  'closed card badge rendered': /CLOSED/.test(initial),
  'connection badge live': /^live$/m.test(initial),
  '-- live --': true,
  'SSE item applied incrementally': afterEvent.includes('LIVE-EVENT-APPLIED'),
  'SSE item owner applied': afterEvent.includes('@agent-x'),
  '-- reconnect/resync --': true,
  'resync revealed server-side change': afterResync.includes('RESYNC-AFTER-RECONNECT'),
  'resync replaced snapshot (stale event item gone)': !afterResync.includes('LIVE-EVENT-APPLIED'),
  'still live after reconnect': /^live$/m.test(afterResync),
  '-- hygiene --': true,
  'no uncaught exceptions': exceptions.length === 0,
  'no console errors': consoleErrors.length === 0,
}

const report = [
  '=== CHECKS ===',
  ...Object.entries(checks).map(([k, v]) =>
    k.startsWith('--') ? `\n${k.replace(/--/g, '').trim().toUpperCase()}` : `${v ? 'PASS' : 'FAIL'}  ${k}`,
  ),
  '',
  '=== console errors ===',
  consoleErrors.length ? consoleErrors.join('\n') : '(none)',
  '=== exceptions ===',
  exceptions.length ? exceptions.join('\n') : '(none)',
  '',
  '=== rendered text: after resync ===',
  afterResync,
].join('\n')

console.log(report)
ws.close()
process.exit(Object.values(checks).every(Boolean) ? 0 : 1)
