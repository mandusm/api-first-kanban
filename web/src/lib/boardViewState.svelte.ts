// Board view state: the user-facing presentation knobs for the board page.
//
// Kept separate from KanbanStore's data responsibilities (workspace/board
// discovery, snapshot loading, SSE application, mutations). Every view knob
// shares one update shape — set the value, restart the per-column reveal
// window so it starts from the new cap, then persist the choice — and that
// shape lives here so the store never has to re-implement it per feature.
// The search box's controller (raw input text, 300ms debounce into the applied
// query, inline parse-error surface) is view state too and lives here (PB-82);
// the store hands in the callback that re-fetches the board when a query is
// committed, since fetching stays a data concern.

import type { ItemKind } from './types'
import { normalizeSearchQuery, isBlankQuery } from './searchQuery'

/**
 * Per-column card display limits offered by the board-header control. `'all'`
 * lifts the render cap entirely. Persisted under DISPLAY_LIMIT_KEY.
 */
export const DISPLAY_LIMIT_OPTIONS = [10, 25, 50, 'all'] as const
export type DisplayLimit = (typeof DISPLAY_LIMIT_OPTIONS)[number]

/** Default cards shown per column before any reveal. */
export const DEFAULT_DISPLAY_LIMIT: DisplayLimit = 25

const DISPLAY_LIMIT_KEY = 'kanban.displayLimit'

/**
 * View sort modes offered by the filter-row Sort control. `'position'` is the
 * canonical stored order; the created_at modes are a presentation lens applied
 * client-side to each column's visible window. `'activity'` orders by most
 * recent activity (PB-122: updated_at ∨ newest comment, newest first).
 */
export const SORT_OPTIONS = [
  { value: 'position', label: 'Position' },
  { value: 'newest', label: 'Newest first' },
  { value: 'oldest', label: 'Oldest first' },
  { value: 'activity', label: 'Recently updated' },
] as const
export type SortMode = (typeof SORT_OPTIONS)[number]['value']

/** Default view sort: the canonical stored position order. */
export const DEFAULT_SORT_MODE: SortMode = 'position'

const SORT_KEY = 'kanban.sortMode'

/**
 * Coerce a stored or form value to a known sort mode. Anything unrecognised
 * (a stale key, localStorage disabled returning null) falls back to position
 * order rather than an unexpected sort.
 */
export function parseSortMode(raw: string | null): SortMode {
  return SORT_OPTIONS.some((option) => option.value === raw)
    ? (raw as SortMode)
    : DEFAULT_SORT_MODE
}

function readStoredSortMode(): SortMode {
  try {
    return parseSortMode(localStorage.getItem(SORT_KEY))
  } catch {
    /* localStorage unavailable: default to position order */
    return DEFAULT_SORT_MODE
  }
}

/** localStorage key for the board-level "Show archived" toggle. */
const SHOW_ARCHIVED_KEY = 'kanban.showArchived'

function readStoredShowArchived(): boolean {
  try {
    return localStorage.getItem(SHOW_ARCHIVED_KEY) === 'true'
  } catch {
    /* localStorage unavailable: archived items stay hidden (the default) */
    return false
  }
}

/**
 * Coerce a stored or form value to a known display limit. Anything
 * unrecognised (a stale key, localStorage disabled returning null) falls back
 * to the default rather than rendering an unexpected number of cards.
 */
export function parseDisplayLimit(raw: string | null): DisplayLimit {
  if (raw === 'all') return 'all'
  const n = Number(raw)
  return (DISPLAY_LIMIT_OPTIONS as readonly unknown[]).includes(n)
    ? (n as DisplayLimit)
    : DEFAULT_DISPLAY_LIMIT
}

function readStoredDisplayLimit(): DisplayLimit {
  try {
    return parseDisplayLimit(localStorage.getItem(DISPLAY_LIMIT_KEY))
  } catch {
    /* localStorage unavailable (private mode, disabled): use the default */
    return DEFAULT_DISPLAY_LIMIT
  }
}

/**
 * The board's view state as a Svelte 5 runes store. Holds the user's view
 * knobs — card filters, sort, per-column display limit and reveal window, and
 * the board-level "Show archived" toggle — plus the setters that centralize
 * "set value → restart every column's reveal window → persist". KanbanStore
 * owns one instance and delegates, so its own responsibilities stay with the
 * board data.
 */
/**
 * Debounce for the search box: the input applies its query this long after the
 * last keystroke, so typing a query is one board fetch instead of one per key.
 */
const SEARCH_DEBOUNCE_MS = 300

export class BoardViewState {
  /** Label id to filter cards by, or null for all. */
  labelFilter = $state<string | null>(null)

  /** Priority value (0..3) to filter cards by, or null for all. */
  priorityFilter = $state<number | null>(null)

  /**
   * Item kind (milestone / epic / task) to filter cards by, or null for all.
   * Session-only, like the label/priority filters: none of them persist.
   */
  kindFilter = $state<ItemKind | null>(null)

  /**
   * Server-side search query (the v1 query language, docs/search-query.md),
   * sent as `q` on the board fetch so the server returns only matching items.
   * Session-only, like the other filters. A blank (or whitespace-only) query
   * means no search: no `q` is sent and the board behaves exactly as before.
   */
  searchQuery = $state('')

  /**
   * Raw text in the search box, edited on every keystroke. Debounced into
   * `searchQuery` (which drives the fetch); session-only, like the other
   * filters. Kept separate so the box stays responsive while requests coalesce
   * and so clearing the applied query can clear the box in one step.
   */
  searchInput = $state('')

  /**
   * Server-side parse error for the active search query (the §6 error
   * envelope's message, verbatim), shown inline next to the search box. Kept
   * apart from the store's `error` so an invalid query annotates the board
   * instead of blanking it. Set by KanbanStore when the board read rejects a
   * query with a 400.
   */
  searchError = $state<string | null>(null)

  /** Pending debounce timer for searchInput -> searchQuery. */
  private searchTimer: ReturnType<typeof setTimeout> | null = null

  /** Called when a newly committed query needs a board re-fetch. */
  private readonly onSearchCommit: () => void

  constructor(onSearchCommit: () => void = () => {}) {
    this.onSearchCommit = onSearchCommit
  }

  /**
   * View sort for every column's visible window. `'position'` (default) keeps
   * the canonical stored order; `'newest'`/`'oldest'` reorder loaded cards by
   * created_at. Persisted to localStorage, and never changes stored positions,
   * drag-drop, or the ↑/↓ arrows (which reorder stored positions).
   */
  sortMode = $state<SortMode>(readStoredSortMode())

  /**
   * Per-column card display limit (render cap). A column never renders more
   * loaded cards than this until the user reveals more; `'all'` lifts the cap.
   * Persisted to localStorage so the choice survives reloads.
   */
  displayLimit = $state<DisplayLimit>(readStoredDisplayLimit())

  /**
   * Loaded cards the user revealed beyond `displayLimit`, per column. Reset
   * whenever the limit or a filter changes so the visible window always starts
   * from the current limit.
   */
  columnReveal = $state<Record<string, number>>({})

  /**
   * Board-level "Show archived" toggle. Off (default): the server excludes
   * archived items and the store drops any that arrive live, so archived cards
   * never render. On: the board is fetched with include_archived=true and
   * archived cards render dimmed. Persisted to localStorage.
   */
  includeArchived = $state<boolean>(readStoredShowArchived())

  /**
   * True when any card filter (label, priority, kind or search query) is
   * active. Used by the board view to show the "All" control as selected and
   * to distinguish an empty column from one with no matching items.
   */
  hasActiveFilters(): boolean {
    return (
      this.labelFilter !== null ||
      this.priorityFilter !== null ||
      this.kindFilter !== null ||
      this.hasSearchQuery()
    )
  }

  /** True when a non-blank search query is active (so `q` will be sent). */
  hasSearchQuery(): boolean {
    return !isBlankQuery(this.searchQuery)
  }

  /** Reveal another slice of already-loaded cards in a column's window. */
  revealMore(columnId: string, amount: number): void {
    this.columnReveal = {
      ...this.columnReveal,
      [columnId]: (this.columnReveal[columnId] ?? 0) + amount,
    }
  }

  toggleLabelFilter(labelId: string): void {
    this.labelFilter = this.labelFilter === labelId ? null : labelId
    // A filter changes which loaded cards are visible, so start the window over.
    this.resetReveal()
  }

  togglePriorityFilter(priority: number): void {
    this.priorityFilter = this.priorityFilter === priority ? null : priority
    this.resetReveal()
  }

  toggleKindFilter(kind: ItemKind): void {
    this.kindFilter = this.kindFilter === kind ? null : kind
    // A filter changes which loaded cards are visible, so start the window over.
    this.resetReveal()
  }

  /**
   * Set the server-side search query. A different query returns a different
   * result set, so restart every reveal window. Blank input is normalized to
   * the empty query so whitespace-only text is indistinguishable from no
   * search (the backend treats both as "match all").
   */
  setSearchQuery(query: string): void {
    const normalized = normalizeSearchQuery(query)
    if (this.searchQuery === normalized) return
    this.searchQuery = normalized
    this.resetReveal()
  }

  /**
   * Clear every filter — label, priority, kind and search (the board's "All"
   * button). KanbanStore additionally drops the raw search-box text and any
   * inline parse error while delegating here.
   */
  clearFilters(): void {
    this.labelFilter = null
    this.priorityFilter = null
    this.kindFilter = null
    this.searchQuery = ''
    this.resetSearchInput()
    this.resetReveal()
  }

  /**
   * Update the search-box text and schedule the debounced apply. The box stays
   * responsive while the request is coalesced; the previous timer is cancelled
   * so only the final text of a burst is fetched.
   */
  setSearchInput(text: string): void {
    this.searchInput = text
    if (this.searchTimer !== null) clearTimeout(this.searchTimer)
    this.searchTimer = setTimeout(() => {
      this.searchTimer = null
      this.applySearch()
    }, SEARCH_DEBOUNCE_MS)
  }

  /**
   * Commit the debounced search text. A commit whose normalized query is
   * unchanged is a no-op, so deleting back to the applied text never refetches.
   * A changed query notifies the store (via the constructor callback) so the
   * board is re-fetched and the server returns only matching items.
   */
  private applySearch(): void {
    const normalized = normalizeSearchQuery(this.searchInput)
    if (normalized === this.searchQuery) return
    this.setSearchQuery(normalized)
    this.searchError = null
    this.onSearchCommit()
  }

  /** Drop the raw search text, pending debounce and inline parse error. */
  resetSearchInput(): void {
    if (this.searchTimer !== null) {
      clearTimeout(this.searchTimer)
      this.searchTimer = null
    }
    this.searchInput = ''
    this.searchError = null
  }

  /**
   * Set the per-column render cap, persist it, and restart every column's
   * reveal so the new limit is visible immediately.
   */
  setDisplayLimit(limit: DisplayLimit): void {
    this.displayLimit = limit
    this.resetReveal()
    try {
      localStorage.setItem(DISPLAY_LIMIT_KEY, String(limit))
    } catch {
      /* non-fatal: the limit still applies for this session */
    }
  }

  /**
   * Set the view sort, persist it, and restart every column's reveal so the
   * window always starts from the current render cap (KB-20 semantics).
   */
  setSortMode(mode: SortMode): void {
    if (this.sortMode === mode) return
    this.sortMode = mode
    // A new order changes which loaded cards fall inside the render cap.
    this.resetReveal()
    try {
      localStorage.setItem(SORT_KEY, mode)
    } catch {
      /* non-fatal: the sort still applies for this session */
    }
  }

  /**
   * Flip the "Show archived" flag, persist it, and restart every column's
   * reveal (the visible set changes entirely). Returns whether it changed, so
   * the caller can trigger the board reconcile fetch — a data concern that
   * stays with KanbanStore.
   */
  setIncludeArchived(show: boolean): boolean {
    if (this.includeArchived === show) return false
    this.includeArchived = show
    this.resetReveal()
    try {
      localStorage.setItem(SHOW_ARCHIVED_KEY, String(show))
    } catch {
      /* non-fatal: the choice still applies for this session */
    }
    return true
  }

  /** Restart every column's reveal window. */
  private resetReveal(): void {
    this.columnReveal = {}
  }
}
