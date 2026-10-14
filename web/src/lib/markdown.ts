// Minimal, dependency-free Markdown renderer for user-authored text — the
// item description and comment bodies. Two output modes share one
// escape-first pipeline and syntax tokenizer: renderMarkdown emits full
// block HTML (the detail modal), renderMarkdownCard emits compact
// phrasing-only HTML for the board's line-clamped summary cards.
//
// XSS-safe by construction: the source is HTML-escaped in full *before* any
// transform runs, and every tag in the output is emitted by this module, so
// user text can never introduce markup. The supported subset is deliberately
// small — h1–h3, bold, italic, inline code, fenced code blocks, flat
// ordered/unordered lists, hard line breaks (a single newline inside a
// paragraph), and http(s) links. Anything outside the subset renders as plain
// escaped text. There is no raw-HTML passthrough. The output is intended for
// Svelte's `{@html}`.

const HTML_ESCAPES: Record<string, string> = {
  '&': '&amp;',
  '<': '&lt;',
  '>': '&gt;',
  '"': '&quot;',
  "'": '&#39;',
}

/** Escape every HTML-significant character. Always the first transform. */
export function escapeHtml(text: string): string {
  // Normalize CRLF first: a stray \r would survive into the rendered output
  // (review P2 — heading/list text kept a trailing carriage return).
  return text.replace(/\r\n?/g, '\n').replace(/[&<>"']/g, (ch) => HTML_ESCAPES[ch] ?? ch)
}

// Only http(s) destinations are linkified; anything else (notably
// `javascript:`) is left as literal escaped text. The href is already escaped,
// so it cannot break out of the attribute.
const LINK_RE = /\[([^\]]+)\]\((https?:\/\/[^\s)]+)\)/gi
// `__`/`_` emphasis uses word-ish boundaries so underscores inside URLs
// (e.g. `http://a_b_c`) are not mistaken for emphasis markers.
const BOLD_STAR_RE = /\*\*([^*]+)\*\*/g
const BOLD_UNDER_RE = /(?<![A-Za-z0-9])__([^_]+)__(?![A-Za-z0-9])/g
const ITALIC_STAR_RE = /\*([^*]+)\*/g
const ITALIC_UNDER_RE = /(?<![A-Za-z0-9])_([^_]+)_(?![A-Za-z0-9])/g

function renderEmphasis(text: string): string {
  return text
    .replace(BOLD_STAR_RE, '<strong>$1</strong>')
    .replace(BOLD_UNDER_RE, '<strong>$1</strong>')
    .replace(ITALIC_STAR_RE, '<em>$1</em>')
    .replace(ITALIC_UNDER_RE, '<em>$1</em>')
}

function renderLinks(text: string): string {
  return text.replace(
    LINK_RE,
    (_match, label: string, href: string) =>
      `<a href="${href}" target="_blank" rel="noopener noreferrer">${renderEmphasis(label)}</a>`
  )
}

/** Inline rules for already-escaped text. Code spans are literal. */
function renderInline(escaped: string): string {
  return escaped
    .split(/(`[^`\n]+`)/g)
    .map((part) =>
      part.length > 1 && part.startsWith('`') && part.endsWith('`')
        ? `<code>${part.slice(1, -1)}</code>`
        : renderLinks(renderEmphasis(part))
    )
    .join('')
}

function isFence(line: string): boolean {
  return /^\s*```/.test(line)
}

function bulletText(line: string): string | null {
  const m = /^\s*[-*+]\s+(.*)$/.exec(line)
  return m ? m[1]! : null
}

function orderedText(line: string): string | null {
  const m = /^\s*\d+\.\s+(.*)$/.exec(line)
  return m ? m[1]! : null
}

/** Match an h1–h3 heading line: `# Text`. h4+ is out of the subset. */
function headingOf(line: string): { level: number; text: string } | null {
  const m = /^(#{1,3})\s+(.*)$/.exec(line)
  return m ? { level: m[1]!.length, text: m[2]! } : null
}

function isHeading(line: string): boolean {
  return headingOf(line) !== null
}

function isBullet(line: string): boolean {
  return bulletText(line) !== null
}

function isOrdered(line: string): boolean {
  return orderedText(line) !== null
}

function isBlockStart(line: string): boolean {
  return isFence(line) || isHeading(line) || isBullet(line) || isOrdered(line)
}

/** Consume consecutive list items starting at `start`; texts stay escaped. */
function collectList(
  lines: string[],
  start: number,
  ordered: boolean,
): { texts: string[]; next: number } {
  const texts: string[] = []
  let i = start
  while (i < lines.length) {
    const text = ordered ? orderedText(lines[i]!) : bulletText(lines[i]!)
    if (text === null) break
    texts.push(text)
    i++
  }
  return { texts, next: i }
}

function renderList(lines: string[], start: number, ordered: boolean): { html: string; next: number } {
  const { texts, next } = collectList(lines, start, ordered)
  const tag = ordered ? 'ol' : 'ul'
  const items = texts.map((t) => `<li>${renderInline(t)}</li>`).join('')
  return { html: `<${tag}>${items}</${tag}>`, next }
}

/**
 * Render the supported Markdown subset to a safe HTML string. The source is
 * escaped first; every emitted tag is generated here, never taken from input.
 */
export function renderMarkdown(source: string): string {
  const lines = escapeHtml(source).split('\n')
  const out: string[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]!
    if (isFence(line)) {
      const code: string[] = []
      i++
      while (i < lines.length && !isFence(lines[i]!)) {
        code.push(lines[i]!)
        i++
      }
      i++ // consume the closing fence (or the end of input)
      out.push(`<pre><code>${code.join('\n')}</code></pre>`)
      continue
    }
    const heading = headingOf(line)
    if (heading) {
      out.push(`<h${heading.level}>${renderInline(heading.text)}</h${heading.level}>`)
      i++
      continue
    }
    if (bulletText(line) !== null) {
      const list = renderList(lines, i, false)
      out.push(list.html)
      i = list.next
      continue
    }
    if (orderedText(line) !== null) {
      const list = renderList(lines, i, true)
      out.push(list.html)
      i = list.next
      continue
    }
    if (line.trim() === '') {
      i++
      continue
    }
    // Paragraph: consecutive plain lines, joined with hard line breaks.
    const paragraph: string[] = []
    while (i < lines.length && lines[i]!.trim() !== '' && !isBlockStart(lines[i]!)) {
      paragraph.push(lines[i]!)
      i++
    }
    out.push(`<p>${paragraph.map(renderInline).join('<br>')}</p>`)
  }
  return out.join('')
}

/**
 * Card-summary mode: the full Markdown subset rendered as compact,
 * phrasing-only HTML for the board's 2-line-clamped summary cards. Block
 * constructs render compactly — headings → bold, bullets → "•", ordered items
 * numbered, fences → code spans — instead of degrading to literal markers,
 * but no block elements are emitted, so the caller's single <p> and its
 * line-clamp CSS keep working. Blocks are separated by hard line breaks; a
 * paragraph's consecutive lines keep their breaks too. Out-of-subset syntax
 * (h4+, raw HTML) stays literal, consistent with renderMarkdown. Same
 * escape-first pipeline as renderMarkdown, so it is XSS-safe by construction.
 */
export function renderMarkdownCard(source: string): string {
  const lines = escapeHtml(source).split('\n')
  const out: string[] = []
  let i = 0
  while (i < lines.length) {
    const line = lines[i]!
    if (isFence(line)) {
      const code: string[] = []
      i++
      while (i < lines.length && !isFence(lines[i]!)) {
        code.push(lines[i]!)
        i++
      }
      i++ // consume the closing fence (or the end of input)
      out.push(`<code>${code.join('<br>')}</code>`)
      continue
    }
    const heading = headingOf(line)
    if (heading) {
      out.push(`<strong>${renderInline(heading.text)}</strong>`)
      i++
      continue
    }
    if (isBullet(line)) {
      const { texts, next } = collectList(lines, i, false)
      out.push(texts.map((t) => `• ${renderInline(t)}`).join('<br>'))
      i = next
      continue
    }
    if (isOrdered(line)) {
      const { texts, next } = collectList(lines, i, true)
      out.push(texts.map((t, n) => `${n + 1}. ${renderInline(t)}`).join('<br>'))
      i = next
      continue
    }
    if (line.trim() === '') {
      i++
      continue
    }
    // Paragraph: consecutive plain lines, kept as hard line breaks.
    const paragraph: string[] = []
    while (i < lines.length && lines[i]!.trim() !== '' && !isBlockStart(lines[i]!)) {
      paragraph.push(lines[i]!)
      i++
    }
    out.push(paragraph.map(renderInline).join('<br>'))
  }
  return out.join('<br>')
}
