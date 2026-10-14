// SSE subscription to GET /api/events with auto-reconnect.
//
// The browser's EventSource reconnects on its own, but with an opaque schedule
// and no resync hook. Here we deliberately close the socket on error and
// re-open it ourselves so we control the exponential backoff and can tell the
// caller to resync (snapshot re-fetch) whenever the stream comes back.
//
// Comments (`: ping` heartbeats) are ignored by EventSource automatically.

import { eventsUrl } from './api'
import type { KanbanEvent } from './types'

export type EventStreamStatus = 'connecting' | 'open' | 'reconnecting' | 'closed'

export interface EventStreamOptions {
  boardId?: string
  workspaceId?: string
  /** Called for each parsed event. */
  onEvent: (event: KanbanEvent) => void
  /** Called on every (re)open — the resync point. */
  onOpen?: () => void
  onStatus?: (status: EventStreamStatus) => void
}

const BASE_DELAY_MS = 1_000
const MAX_DELAY_MS = 30_000

export class EventStream {
  private readonly options: EventStreamOptions
  private source: EventSource | null = null
  private timer: ReturnType<typeof setTimeout> | null = null
  private attempts = 0
  private stopped = false

  constructor(options: EventStreamOptions) {
    this.options = options
  }

  start(): void {
    this.stopped = false
    this.open()
  }

  close(): void {
    this.stopped = true
    this.clearTimer()
    this.source?.close()
    this.source = null
    this.options.onStatus?.('closed')
  }

  private open(): void {
    if (this.stopped) return
    this.options.onStatus?.(this.attempts === 0 ? 'connecting' : 'reconnecting')

    const url = eventsUrl({ boardId: this.options.boardId, workspaceId: this.options.workspaceId })
    const source = new EventSource(url)
    this.source = source

    source.onopen = () => {
      this.attempts = 0
      this.options.onStatus?.('open')
      // Resync: anything missed while the stream was down is fetched now.
      this.options.onOpen?.()
    }

    source.onmessage = (message: MessageEvent<string>) => {
      let parsed: unknown
      try {
        parsed = JSON.parse(message.data)
      } catch {
        return // ignore malformed frames rather than tearing down the stream
      }
      if (parsed && typeof parsed === 'object') {
        this.options.onEvent(parsed as KanbanEvent)
      }
    }

    source.onerror = () => {
      // Do not rely on EventSource's own retry: close it and back off ourselves.
      source.close()
      if (this.source === source) this.source = null
      this.scheduleReconnect()
    }
  }

  private scheduleReconnect(): void {
    if (this.stopped || this.timer !== null) return

    const exponential = Math.min(MAX_DELAY_MS, BASE_DELAY_MS * 2 ** this.attempts)
    // Full jitter keeps multiple clients from reconnecting in lockstep.
    const delay = Math.round(exponential * (0.5 + Math.random() * 0.5))
    this.attempts += 1
    this.options.onStatus?.('reconnecting')

    this.timer = setTimeout(() => {
      this.timer = null
      this.open()
    }, delay)
  }

  private clearTimer(): void {
    if (this.timer !== null) {
      clearTimeout(this.timer)
      this.timer = null
    }
  }
}
