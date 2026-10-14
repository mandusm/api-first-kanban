// Node-based unit checks for the hand-rolled Markdown renderer. The project has
// no test runner wired up (see web/package.json), so these use the built-in
// `node:test` runner with native TypeScript type-stripping (Node >= 23):
//   node --test src/lib/markdown.test.mjs
// Kept as .mjs so svelte-check's tsconfig (which includes src/**/*.ts|js|svelte)
// does not type-check it, while the imported ./markdown.ts is stripped by Node.

import assert from 'node:assert/strict'
import { test } from 'node:test'
import { renderMarkdown, renderMarkdownCard } from './markdown.ts'

const render = (src) => renderMarkdown(src)
const renderCard = (src) => renderMarkdownCard(src)

test('plain text is preserved', () => {
  assert.equal(render('hello world'), '<p>hello world</p>')
})

test('html-special characters are escaped, never emitted raw', () => {
  assert.equal(render('<script>alert(1)</script>'), '<p>&lt;script&gt;alert(1)&lt;/script&gt;</p>')
  const out = render('<img src=x onerror=alert(1)>')
  assert.ok(!out.includes('<img'), 'raw <img> must not survive')
  assert.ok(out.includes('&lt;img'))
})

test('no raw HTML passthrough for inline tags', () => {
  const out = render('a <b>bold</b> b')
  assert.ok(!out.includes('<b>'), 'raw <b> must not survive')
  assert.ok(out.includes('&lt;b&gt;'))
})

test('inline code is literal and escaped', () => {
  assert.equal(render('use `a < b` here'), '<p>use <code>a &lt; b</code> here</p>')
  // Markdown inside a code span is not transformed.
  assert.equal(render('`**not bold**`'), '<p><code>**not bold**</code></p>')
})

test('bold and italic', () => {
  assert.equal(render('**bold** and *italic*'), '<p><strong>bold</strong> and <em>italic</em></p>')
  assert.equal(render('__bold__ and _italic_'), '<p><strong>bold</strong> and <em>italic</em></p>')
})

test('headings h1-h3 only', () => {
  assert.equal(render('# One'), '<h1>One</h1>')
  assert.equal(render('## Two'), '<h2>Two</h2>')
  assert.equal(render('### Three'), '<h3>Three</h3>')
  assert.equal(render('#### Four'), '<p>#### Four</p>')
  assert.equal(render('#nospace'), '<p>#nospace</p>')
})

test('fenced code blocks preserve content and do not parse markdown', () => {
  const out = render('```\n<b>raw</b>\n# not a heading\n```')
  assert.equal(out, '<pre><code>&lt;b&gt;raw&lt;/b&gt;\n# not a heading</code></pre>')
})

test('unordered and ordered lists', () => {
  assert.equal(render('- a\n- b'), '<ul><li>a</li><li>b</li></ul>')
  assert.equal(render('1. a\n2. b'), '<ol><li>a</li><li>b</li></ol>')
})

test('hard line breaks inside a paragraph', () => {
  assert.equal(render('line one\nline two'), '<p>line one<br>line two</p>')
})

test('http(s) links get target and rel', () => {
  assert.equal(
    render('[docs](https://example.com/x)'),
    '<p><a href="https://example.com/x" target="_blank" rel="noopener noreferrer">docs</a></p>'
  )
  assert.ok(render('[x](http://example.com)').includes('target="_blank"'))
})

test('javascript: links are not linkified', () => {
  const out = render('[click](javascript:alert(1))')
  assert.ok(!out.includes('<a'), 'javascript: must not produce an anchor')
  // The literal marker survives as inert plain text inside a paragraph.
  assert.equal(out, '<p>[click](javascript:alert(1))</p>')
})

test('comment-style XSS vectors: iframe, style, data: URLs, mixed-case javascript:', () => {
  // No raw iframe/style tags (escape-first pipeline: no tag survives).
  for (const src of [
    '<iframe src="https://evil.example"></iframe>',
    '<style>body{background:url(javascript:alert(1))}</style>',
  ]) {
    const out = render(src)
    assert.ok(!out.includes('<iframe') && !out.includes('<style'), `no raw tag may survive: ${out}`)
    assert.ok(out.includes('&lt;'), 'dangerous markup must be escaped')
  }
  // data: URLs are outside the http(s)-only link subset → never an anchor.
  // Pinned exactly: the whole link marker stays literal escaped text.
  assert.equal(
    render('[x](data:text/html,<script>alert(1)</script>)'),
    '<p>[x](data:text/html,&lt;script&gt;alert(1)&lt;/script&gt;)</p>'
  )
  // Mixed-case / whitespace-padded scheme stays out of the link subset —
  // pinned exactly: literal text, no anchor, no attribute carrying a scheme.
  for (const href of ['JaVaScRiPt:alert(1)', 'java\nscript:alert(1)']) {
    const out = render(`[x](${href})`)
    // Whitespace-split scheme renders as a hard line break inside the
    // paragraph — still inert (no anchor, broken scheme).
    const expectedBody = href === 'java\nscript:alert(1)' ? 'java<br>script:alert(1)' : href
    assert.equal(out, `<p>[x](${expectedBody})</p>`)
    assert.ok(!out.includes('<a'), `scheme must not linkify: ${href}`)
  }
  // Event-handler attributes cannot survive anywhere in output.
  const out = render('<a href="#" onclick="alert(1)">y</a>')
  assert.ok(!out.includes('<a '), 'raw <a> must not survive')
  assert.ok(out.includes('&lt;a'), 'handler-bearing markup must be escaped')
})

test('attribute injection through a link URL cannot break out', () => {
  const out = render('[x](http://e.com/"onmouseover="alert(1))')
  assert.ok(!out.includes('"onmouseover'), 'quote must not survive raw in the attribute')
})

test('link label supports emphasis', () => {
  assert.equal(
    render('[**bold**](https://example.com)'),
    '<p><a href="https://example.com" target="_blank" rel="noopener noreferrer"><strong>bold</strong></a></p>'
  )
})

test('underscores inside a URL are not treated as emphasis', () => {
  assert.equal(
    render('https://example.com/a_b_c'),
    '<p>https://example.com/a_b_c</p>'
  )
})

test('mixed document renders coherently', () => {
  const out = render('# Title\n\n- one\n- two\n\nSome **bold** and a [link](https://e.com).')
  assert.equal(
    out,
    '<h1>Title</h1><ul><li>one</li><li>two</li></ul>' +
      '<p>Some <strong>bold</strong> and a <a href="https://e.com" target="_blank" rel="noopener noreferrer">link</a>.</p>'
  )
})

// --- renderMarkdownCard: card-summary (phrasing-only) mode ---

test('card mode renders the inline subset without a paragraph wrapper', () => {
  assert.equal(
    renderCard('**b** *i* `c` [x](https://a)'),
    '<strong>b</strong> <em>i</em> <code>c</code> ' +
      '<a href="https://a" target="_blank" rel="noopener noreferrer">x</a>'
  )
})

test('card mode renders headings as bold instead of literal markers', () => {
  assert.equal(renderCard('# Heading'), '<strong>Heading</strong>')
  assert.equal(renderCard('## Two'), '<strong>Two</strong>')
  assert.equal(renderCard('### Three'), '<strong>Three</strong>')
})

test('card mode renders lists as compact bullets and numbers', () => {
  assert.equal(renderCard('- a\n- b'), '• a<br>• b')
  assert.equal(renderCard('* a\n* b'), '• a<br>• b')
  assert.equal(renderCard('1. a\n2. b'), '1. a<br>2. b')
})

test('card mode renders fenced code as a code span with line breaks', () => {
  assert.equal(renderCard('```\nx\ny\n```'), '<code>x<br>y</code>')
})

test('card mode separates blocks with hard line breaks', () => {
  assert.equal(
    renderCard('## Goal\n\nDo the thing.\n\n- a\n- b'),
    '<strong>Goal</strong><br>Do the thing.<br>• a<br>• b'
  )
  // A paragraph keeps its consecutive lines as breaks (like the modal).
  assert.equal(renderCard('line one\nline two'), 'line one<br>line two')
})

test('card mode emits phrasing content only — no block tags', () => {
  const out = renderCard('# H\n\n- a\n\n```\nc\n```\n\ntext')
  assert.ok(!/<(p|ul|ol|li|h[1-3]|pre)[\s>/]/.test(out), `no block tags may appear: ${out}`)
})

test('card mode keeps out-of-subset syntax literal, consistent with the modal', () => {
  assert.equal(renderCard('#### Four'), '#### Four')
  assert.equal(renderCard('#nospace'), '#nospace')
})

test('card mode escapes XSS payloads (no tag survives)', () => {
  const out = renderCard('<script>alert(1)</script>')
  assert.ok(!out.includes('<script'), 'raw <script> must not survive')
  assert.equal(out, '&lt;script&gt;alert(1)&lt;/script&gt;')
  const img = renderCard('<img src=x onerror=alert(1)>')
  assert.ok(!img.includes('<img'), 'raw <img> must not survive')
  assert.ok(img.includes('&lt;img'))
})

test('card mode does not linkify javascript: and escapes quote injection', () => {
  assert.ok(!renderCard('[click](javascript:alert(1))').includes('<a'))
  const out = renderCard('[x](http://e.com/"onmouseover="alert(1))')
  assert.ok(!out.includes('"onmouseover'), 'quote must not survive raw in the attribute')
})
