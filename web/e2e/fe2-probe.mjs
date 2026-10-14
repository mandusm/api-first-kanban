// Probe the live client store via Vite's module singleton (no source changes).
import { APP_URL } from './lib/harness.mjs'
const CDP = '9224'
async function pageTarget() {
  const res = await fetch(`http://127.0.0.1:${CDP}/json/list`)
  const t = await res.json()
  return t.find((x) => x.type === 'page' && x.url.startsWith(APP_URL.replace(/\/$/, ''))).webSocketDebuggerUrl
}
const ws = new WebSocket(await pageTarget())
await new Promise((r, j) => {
  ws.addEventListener('open', r, { once: true })
  ws.addEventListener('error', j, { once: true })
})
let id = 0
const pend = new Map()
ws.addEventListener('message', (ev) => {
  const m = JSON.parse(ev.data)
  if (m.id && pend.has(m.id)) { pend.get(m.id)(m); pend.delete(m.id) }
})
const send = (method, params = {}) => new Promise((res) => { const i = ++id; pend.set(i, res); ws.send(JSON.stringify({ id: i, method, params })) })
async function js(code) {
  const r = await send('Runtime.evaluate', { expression: `(async () => { ${code} })()`, returnByValue: true, awaitPromise: true })
  if (r.result?.exceptionDetails) throw new Error(JSON.stringify(r.result.exceptionDetails.exception?.description ?? r.result.exceptionDetails))
  return r.result?.result?.value
}

const out = await js(`
  const m = await import('/src/lib/kanban.svelte.ts');
  const k = m.kanban;
  return {
    boardId: k.boardId,
    workspaceId: k.workspaceId,
    itemCount: k.items.length,
    bad: k.items.filter(i => !Array.isArray(i.labels)).map(i => ({ title: i.title, keys: Object.keys(i), labels: i.labels })),
    sample: k.items.slice(0,3).map(i => ({ title: i.title, labels: (i.labels||[]).length })),
    labelFilter: k.labelFilter,
  }
`)
console.log(JSON.stringify(out, null, 2))
ws.close()
