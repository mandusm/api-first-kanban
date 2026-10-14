// PB-104 closeout e2e: comments posted to the API render as SANITIZED
// markdown in the item detail view. Self-seeding like the other fe2-*/pb*-
// suites; isolated stack by default (see lib/harness.mjs).
//
// Flow covered:
//   a. seed workspace + board + one task item via the REAL REST API
//   b. POST a markdown comment (bold, heading, list, fenced code, https link)
//      through the API while the modal is CLOSED → open the modal and assert
//      the rendered DOM: <strong>/<h2>/<ul><li>/<pre><code>/<a href> present,
//      literal markers (*, #, ```) absent
//   c. POST an XSS-payload comment (script tag, event handler, javascript:
//      link, data: URL, iframe) → assert NO element/script anchor survives:
//      raw <script>/<iframe>/<style> nodes absent, javascript: never becomes
//      an <a href>, payload text appears only as inert escaped text
//   d. plain-text comment still renders sensibly (paragraph + hard break)
//
// Cleanup: the throwaway item is closed + archived via the API (workspace
// shell stays, no workspace DELETE endpoint — pb74 convention).
import {
  APP_URL, req, checks, seedWorkspace, selectWorkspaceAndBoard, waitAppReady, openItemModal,
} from './lib/harness.mjs'
import { connectCdpPage, makeEval } from './fe2-cdp.mjs'

const TS = Date.now() % 1000000
const AGENT = 'pb104-e2e'
const MARK = `pb104md-${TS}`

const { results, pass, fail, summary } = checks()

const { ws, board, columns } = await seedWorkspace({ name: `${MARK}-ws`, agent: AGENT })
const TODO = columns[0].id
const { item } = await req('POST', `/api/boards/${board.id}/items`, {
  title: `${MARK}-item`, kind: 'task', column_id: TODO, agent: AGENT,
})
console.error(`[t] seeded workspace=${ws.id} board=${board.id} item=${item.id}`)

// --- comments via the API (comments are stored raw; rendering is frontend) ---

const MD_BODY = [
  'Reviewer note for **PB-104**:',
  '',
  '## Sanitization',
  '- storage stays raw',
  '- rendering is frontend-only',
  '',
  '```js',
  'const safe = "<script>"',
  '```',
  'See [docs](https://example.com/api).',
].join('\n')
const mdComment = (await req('POST', `/api/items/${item.id}/comments`, { body: MD_BODY, agent: AGENT })).comment

const XSS_BODY = [
  '<script>window.__pwned=1</script>',
  '<iframe src="https://evil.example"></iframe>',
  '<img src=x onerror="window.__pwned=2">',
  '[click](javascript:alert(1))',
  '[x](data:text/html,<script>alert(1)</script>)',
  '<style>body{display:none}</style>',
].join('\n')
const xssComment = (await req('POST', `/api/items/${item.id}/comments`, { body: XSS_BODY, agent: AGENT })).comment

const PLAIN_BODY = 'plain line one\nplain line two'
await req('POST', `/api/items/${item.id}/comments`, { body: PLAIN_BODY, agent: AGENT })

// API truth: stored raw, contract unchanged.
{
  const { comments } = await req('GET', `/api/items/${item.id}/comments`)
  if (comments.find((c) => c.id === mdComment.id)?.body !== MD_BODY) fail('markdown comment body not stored raw')
  else pass('markdown comment body stored raw byte-exact')
  if (comments.find((c) => c.id === xssComment.id)?.body !== XSS_BODY) fail('xss comment body not stored raw')
  else pass('xss comment body stored raw byte-exact')
}

// --- live app over CDP -------------------------------------------------------

console.error('[t] connecting cdp')
const exceptions = []
const { send, onMessage } = await connectCdpPage({ url: APP_URL })
const { js, soft, waitJs } = makeEval(send)
onMessage((m) => {
  if (m.method === 'Runtime.exceptionThrown') exceptions.push(m.params.exceptionDetails?.exception?.description ?? '?')
})

if (!(await waitAppReady({ js, soft, waitJs, send }))) fail('app never booted')
if (!(await selectWorkspaceAndBoard({ js, waitJs, wsId: ws.id, boardId: board.id })))
  fail('our board never appeared in the board selector')
if (!(await openItemModal({ js, waitJs, itemId: item.id, readySel: 'dialog[open] form textarea' })))
  fail('detail modal never opened')

// Comment bodies render inside the Comments <ul> as .markdown-body divs.
const commentBodies = () =>
  js(`return Array.from(document.querySelectorAll('dialog[open] ul li .markdown-body')).map((el) => el.innerHTML)`)

if (!(await waitJs(`return document.querySelectorAll('dialog[open] ul li .markdown-body').length >= 3`)))
  fail('three comment bodies never rendered in the modal')
const bodies = (await commentBodies()) ?? []
const mdHtml = bodies.find((h) => h.includes('<strong>')) ?? ''

// b. markdown comment renders as markup
if (!mdHtml) fail('markdown comment did not render any <strong>')
else pass('markdown comment rendered as markup')
mdHtml.includes('<strong>PB-104</strong>') ? pass('bold rendered (<strong>)') : fail('bold not rendered')
const H2_RE=/<h2[^>]*>Sanitization<\/h2>/
H2_RE.test(mdHtml) ? pass('heading rendered (<h2>)') : fail('heading not rendered')
mdHtml.includes('<ul>') && mdHtml.includes('<li>storage stays raw</li>') ? pass('list rendered (<ul><li>)') : fail('list not rendered')
mdHtml.includes('<pre><code>') && mdHtml.includes('<script>') === false
  ? pass('fenced code rendered, inner HTML escaped')
  : fail('fenced code rendering wrong')
const A_RE=/<a href="https:\/\/example.com\/api" target="_blank" rel="noopener noreferrer">/
A_RE.test(mdHtml)
  ? pass('https link rendered with rel=noopener')
  : fail('https link rendering wrong')
mdHtml.includes('*') || mdHtml.includes('##') || mdHtml.includes('```')
  ? fail('literal markdown markers leaked into rendered output')
  : pass('no literal markdown markers in rendered output')

// c. XSS comment renders inert
const xssHtml = bodies.find((h) => h.includes('javascript:alert(1)')) ?? ''
if (!xssHtml) fail('xss comment body not found in rendered comments')
else {
  const dialogProbe = await js(`return {
    pwned: window.__pwned ?? null,
    scripts: document.querySelectorAll('dialog[open] script, dialog[open] iframe, dialog[open] style').length,
    jsHrefs: Array.from(document.querySelectorAll('dialog[open] a')).map((a) => a.getAttribute('href')).filter((h) => h && (h.toLowerCase().startsWith('javascript:') || h.toLowerCase().startsWith('data:'))).length,
    imgs: document.querySelectorAll('dialog[open] img').length,
  }`)
  dialogProbe.pwned === null ? pass('no script executed (window.__pwned unset)') : fail('script executed!')
  dialogProbe.scripts === 0 ? pass('no script/iframe/style elements in the modal') : fail(`dangerous elements present: ${dialogProbe.scripts}`)
  dialogProbe.jsHrefs === 0 ? pass('no javascript:/data: href anchors') : fail(`dangerous anchors: ${dialogProbe.jsHrefs}`)
  dialogProbe.imgs === 0 ? pass('no <img> injected (onerror payload inert)') : fail('<img> injected')
  xssHtml.includes('&lt;script&gt;') ? pass('script tag present only as escaped text') : fail('script tag text not escaped')
  !xssHtml.includes('<a') ? pass('javascript:/data: links never became anchors') : fail(`an anchor leaked from a dangerous link: ${xssHtml}`)
}

// d. plain text still sensible
const plainHtml = bodies.find((h) => h.includes('plain line one')) ?? ''
plainHtml === '<p>plain line one<br>plain line two</p>'
  ? pass('plain-text comment renders as paragraph + hard break')
  : fail(`plain-text rendering unexpected: ${plainHtml}`)

exceptions.length === 0 ? pass('no runtime exceptions during the flow') : fail(`page exceptions: ${exceptions[0]}`)

// --- cleanup ---------------------------------------------------------------
await req('PATCH', `/api/items/${item.id}`, { closed: true, agent: AGENT }).catch(() => {})
await req('POST', `/api/items/${item.id}/archive`, { agent: AGENT }).catch(() => {})
console.error('[t] cleaned up: item closed + archived')

console.error(`\n=== page exceptions ===\n${exceptions.length ? `${exceptions.length}x ${[...new Set(exceptions)].join(' | ')}` : '(none)'}`)

process.exit(await summary())
