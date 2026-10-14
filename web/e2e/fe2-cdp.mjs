// Shared CDP plumbing for the fe2-* live scripts (plain Node, no puppeteer).
//
// Lessons baked in here:
// - A stale headless chrome accepts the WebSocket but never answers CDP
//   sends. If the handshake or first navigation doesn't resolve within a few
//   seconds, kill chrome, wipe its profile dir, start a fresh instance, and
//   retry (bounded, self-healing).
// - pkill -f must never see its own pattern in the same shell's cmdline, and
//   kill/launch must use separate shells (the launch cmdline contains the
//   literal debug port).
// - Never issue two Page.navigations back to back: a navigate while another
//   navigation is in flight wedges the CDP session.
// - Evaluations run in an async IIFE so both sync and awaited code work.
import { setTimeout as sleep } from 'node:timers/promises'
import { execFileSync } from 'node:child_process'

export const CDP_PORT = process.env.CDP_PORT || '9224'
export const CHROME_PROFILE = '/tmp/fe2-chrome-profile'

export function withTimeout(p, ms, label) {
  const timer = new Promise((_, j) => setTimeout(() => j(new Error(label + ' timed out')), ms))
  // A rejection after the race has settled must not crash the process.
  timer.catch(() => {})
  return Promise.race([p, timer])
}

function healChrome() {
  const sh = (c) => execFileSync('bash', ['-lc', c], { encoding: 'utf8' })
  // [4] bracket trick: the pkill shell's own cmdline must not match.
  sh(`pkill -f 'remote-debugging-port=922[4]' 2>/dev/null || true; sleep 1; rm -rf ${CHROME_PROFILE}`)
  sh(`setsid google-chrome --headless=new --remote-debugging-port=${CDP_PORT} --user-data-dir=${CHROME_PROFILE} --no-first-run --no-default-browser-check about:blank > /tmp/fe2-chrome.log 2>&1 & sleep 2`)
}

/**
 * Connect to the first page target, self-healing a wedged chrome if needed.
 * Returns { ws, send, onMessage }; `send` is the bound CDP command sender and
 * `onMessage(fn)` registers an observer for every non-reply CDP message
 * (console errors, exceptions, ...).
 */
export async function connectCdpPage(opts = {}) {
  const pend = new Map()
  const listeners = []
  let ws = null
  let nextId = 0
  const send = (method, params = {}) => {
    const id = ++nextId
    return new Promise((resolve) => {
      pend.set(id, resolve)
      ws.send(JSON.stringify({ id, method, params }))
    })
  }
  const onCdpMessage = (ev) => {
    const m = JSON.parse(ev.data)
    if (m.id && pend.has(m.id)) {
      pend.get(m.id)(m)
      pend.delete(m.id)
      return
    }
    for (const l of listeners) l(m)
  }
  for (let attempt = 1; ; attempt++) {
    try {
      const tgt = await withTimeout(fetch(`http://127.0.0.1:${CDP_PORT}/json/list`).then((r) => r.json()), 5000, 'json/list')
      const page = tgt.find((t) => t.type === 'page')
      if (!page?.webSocketDebuggerUrl) throw new Error('no CDP page target')
      ws = new WebSocket(page.webSocketDebuggerUrl)
      ws.addEventListener('message', onCdpMessage)
      await withTimeout(
        new Promise((r, j) => {
          ws.addEventListener('open', r, { once: true })
          ws.addEventListener('error', j, { once: true })
        }),
        5000,
        'ws open',
      )
      await withTimeout(send('Page.enable'), 5000, 'Page.enable')
      await withTimeout(send('Runtime.enable'), 5000, 'Runtime.enable')
      if (opts.url) await withTimeout(send('Page.navigate', { url: opts.url }), 10000, 'Page.navigate')
      return { ws, send, onMessage: (fn) => listeners.push(fn) }
    } catch (e) {
      try { ws?.close() } catch { /* already dead */ }
      pend.clear()
      if (attempt >= 4) throw e
      console.error(`[t] CDP unresponsive (${e.message}); healing chrome and retrying`)
      healChrome()
    }
  }
}

/** Evaluate/poll helpers bound to a CDP `send`. Code runs in an async IIFE. */
export function makeEval(send) {
  const js = async (code) => {
    const r = await send('Runtime.evaluate', {
      expression: `(async () => { ${code} })()`,
      returnByValue: true,
      awaitPromise: true,
    })
    const ex = r.result?.exceptionDetails
    if (ex) throw new Error('page: ' + (ex.exception?.description ?? ex.text))
    return r.result?.result?.value
  }
  const soft = (c) => js(c).catch(() => false)
  const waitJs = async (code, tries = 80, delay = 150) => {
    for (let i = 0; i < tries; i++) {
      if (await soft(code)) return true
      await sleep(delay)
    }
    return false
  }
  return { js, soft, waitJs }
}

/**
 * Drive the card's native drag & drop end to end from the page: dispatch a
 * dragenter → dragover → drop sequence on the target column carrying the item
 * id in `text/plain` (exactly what the card's dragstart stores and the column's
 * drop handler reads). Using the synthetic DragEvent API exercises the same
 * handlers a real pointer drag would; it is deterministic and does not depend
 * on CDP drag interception being enabled. Returns false if either element is
 * missing. The caller should poll for the card to appear in the target column,
 * since the move is an async API round-trip.
 */
export async function dragCardToColumn(send, itemId, columnName) {
  const r = await send('Runtime.evaluate', {
    expression: `(() => {
      const card = document.querySelector('[data-item="${itemId}"]');
      const col = document.querySelector('[data-column="${columnName}"]');
      if (!card || !col) return false;
      const dt = new DataTransfer();
      dt.setData('text/plain', ${JSON.stringify(itemId)});
      for (const type of ['dragenter', 'dragover', 'drop']) {
        col.dispatchEvent(new DragEvent(type, { bubbles: true, cancelable: true, dataTransfer: dt }));
      }
      return true;
    })()`,
    returnByValue: true,
  })
  return r.result?.result?.value === true
}
