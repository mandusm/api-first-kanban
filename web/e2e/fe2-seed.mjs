// Seed fresh live-suite data via the REAL REST API and write the seed env file
// consumed by the fe2-* CDP scripts (path: FE2_SEED, default /tmp/fe2-seed.env).
//
// The shared dev DB is never reset, so every run seeds a fresh workspace/board
// with unique TS-suffixed names; the env file pins the ids the scripts use.
// PB-81: the API default comes from the shared harness (isolated stack
// :18081 by default; FE2_STACK=live or FE2_API opts into another stack).
import { API, FE2_SEED as OUT } from './lib/harness.mjs'

const TS = Date.now() % 1000000
const post = async (path, body) => {
  const r = await fetch(`${API}${path}`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!r.ok) throw new Error(`${path} -> ${r.status}: ${await r.text()}`)
  return r.json()
}

const { workspace: ws } = await post('/api/workspaces', { name: `fe2-ws-${TS}`, description: 'fe2 live suite seed' })
const created = await post(`/api/workspaces/${ws.id}/boards`, { name: `fe2-board-${TS}` })
const columns = [...created.board.columns].sort((a, b) => a.position - b.position)
if (columns.length < 2) throw new Error(`expected >=2 default columns, got ${columns.length}`)
const { label } = await post(`/api/workspaces/${ws.id}/labels`, { name: `urgent-${TS}`, color: '#ef4444' })

const env = [
  `TS=${TS}`,
  `WS=${ws.id}`,
  `BOARD=${created.board.id}`,
  `COL1=${columns[0].id}`,
  `COL2=${columns[1].id}`,
  `LABEL=${label.id}`,
  '',
].join('\n')
const { writeFileSync } = await import('node:fs')
writeFileSync(OUT, env)
console.log(`seeded: workspace=${ws.id} board=${created.board.id} (${created.board.name})`)
console.log(`wrote ${OUT}:\n${env}`)
