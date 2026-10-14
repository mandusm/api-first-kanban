// Board data as a Svelte 5 runes store.
//
// A single class holds the board page's DATA responsibilities:
// workspace/board discovery, the active board snapshot, paginated loading,
// mutations, and live SSE event application. The user's view state (filters,
// sort, display limit, reveal windows, "Show archived", the search box's
// input/debounce/error controller) lives in BoardViewState; the store owns
// one instance and delegates to it.
// Components import one store instance; no state library involved.

import * as api from './api'
import { BoardViewState, type DisplayLimit, type SortMode } from './boardViewState.svelte'
import { kindOf, parentKindsFor } from './format'
import type { Board, Column, Comment, Item, ItemCreateInput, ItemDetail, ItemKind, ItemPatchInput, KanbanEvent, Label, Relation, RelationKind, Workspace } from './types'

function byPosition<T extends { position: number }>(a: T, b: T): number {
  return a.position - b.position
}

/**
 * Items are ordered by their fractional position, which arrives as a decimal
 * string — compare numerically (never lexically, where "10" < "2").
 * created_at breaks ties so ordering stays deterministic if positions ever
 * collide.
 */
function byItemPosition(a: Item, b: Item): number {
  const pa = Number(a.position)
  const pb = Number(b.position)
  if (pa !== pb) return pa - pb
  return a.created_at.localeCompare(b.created_at)
}

/**
 * First page size for a column's initial board load (KB-17). Chosen so a
 * typical small board fits in one page and therefore gains no Load more
 * control; larger columns are loaded progressively on demand.
 */
const COLUMN_PAGE_SIZE = 50

export class KanbanStore {
  // discovery
  workspaces = $state<Workspace[]>([])
  boards = $state<Board[]>([])
  workspaceId = $state<string | null>(null)
  boardId = $state<string | null>(null)

  // active board snapshot
  board = $state<Board | null>(null)
  columns = $state<Column[]>([])
  items = $state<Item[]>([])
  labels = $state<Label[]>([])

  // status
  loading = $state(false)
  error = $state<string | null>(null)
  /** Last mutation failure, kept separate so a failed write never blanks the board. */
  actionError = $state<string | null>(null)
  saving = $state(false)
  live = $state(false)
  reconnecting = $state(false)
  lastEventAt = $state<string | null>(null)

  /**
   * Most recent comment touched (SSE or a local write), surfaced so an open
   * item-detail modal can merge it into the thread without a refetch.
   */
  recentComments = $state<Comment[]>([])

  /**
   * Live "blocked by" index: item id -> ids of its incoming `blocks`
   * relations. Seeded from item details on board load, then maintained by
   * relation.created / relation.deleted SSE events so card badges stay current.
   */
  blockedBy = $state<Record<string, string[]>>({})

  /**
   * Bumped whenever a relation changes (hydration, SSE, or a local write). An
   * open detail modal watches it to refetch its edges without polling.
   */
  relationsVersion = $state(0)

  /**
   * Bumped whenever an item on the active board changes — any applied
   * item.* SSE event or a local item write (upsertItem). An open detail
   * modal watches it to refetch the item's children (and the rest of the
   * detail payload) without polling; an SSE echo after a local write may
   * bump twice, which coalesces into at most one extra refetch.
   */
  itemsVersion = $state(0)

  /**
   * Board view state (filters, sort, per-column display limit and reveal
   * window, "Show archived", and the search-box controller). The state itself
   * lives in BoardViewState; the store owns one instance and delegates every
   * view-knob change to it, passing the board re-fetch as the search-commit
   * callback (fetching stays a data concern).
   */
  view = new BoardViewState(() => {
    void this.refresh()
  })

  // View-knob reads, surfaced on the store so components (and the e2e probes
  // that read `kanban.<knob>` directly) keep their existing spelling.
  get labelFilter(): string | null {
    return this.view.labelFilter
  }
  get priorityFilter(): number | null {
    return this.view.priorityFilter
  }
  get kindFilter(): ItemKind | null {
    return this.view.kindFilter
  }
  get sortMode(): SortMode {
    return this.view.sortMode
  }
  get displayLimit(): DisplayLimit {
    return this.view.displayLimit
  }
  get includeArchived(): boolean {
    return this.view.includeArchived
  }
  get columnReveal(): Record<string, number> {
    return this.view.columnReveal
  }
  get searchQuery(): string {
    return this.view.searchQuery
  }

  /**
   * Raw text in the search box (kept in BoardViewState since PB-82); surfaced
   * here so components (and the e2e probes that read `kanban.<knob>` directly)
   * keep their existing spelling.
   */
  get searchInput(): string {
    return this.view.searchInput
  }

  /** Inline parse error for the active search query (lives in BoardViewState). */
  get searchError(): string | null {
    return this.view.searchError
  }

  /**
   * next_cursor per column: a string means server-side pages beyond the loaded
   * items exist, null means the column is fully loaded. A column absent from
   * the map is treated as fully loaded.
   */
  columnCursors = $state<Record<string, string | null>>({})

  /** Total item count per column from the snapshot, used by the Load more label. */
  columnTotals = $state<Record<string, number>>({})

  /**
   * Live items created (or unarchived) in a column that still has unloaded
   * pages are held out of the store (keyset pages only move forward, so they
   * cannot be merged into the loaded window) and kept here per column. Their
   * count feeds the Load more button; tracking them by id lets an
   * item.archived event drop the exact held-out row instead of guessing.
   */
  columnPendingItems = $state<Record<string, Item[]>>({})

  /** Column whose next page is in flight, or null. Disables that one button. */
  loadingMoreColumn = $state<string | null>(null)

  /**
   * True while the board snapshot is being re-fetched to reconcile a "Show
   * archived" toggle. Drives a transient column pulse so the toggle never
   * looks like it did nothing. Ordinary resyncs do not set it.
   */
  reconciling = $state(false)

  /** Single-flight gate for resyncs; a trailing resync coalesces a burst. */
  private snapshotInFlight = false
  private snapshotQueued = false

  /**
   * Monotonic id for the latest board load. Every async continuation of a
   * load (snapshot read, per-column page fetches, relation hydration) captures
   * the id at its start and discards its result if a newer load has begun, so
   * a slow initial load can never overwrite a resync (or vice versa).
   */
  private loadSeq = 0

  get selectedWorkspace(): Workspace | null {
    return this.workspaces.find((w) => w.id === this.workspaceId) ?? null
  }

  get selectedBoard(): Board | null {
    return this.boards.find((b) => b.id === this.boardId) ?? null
  }

  /**
   * All loaded items after the archive filter: archived items are hidden unless
   * the "Show archived" toggle is on. Used for board-level counts.
   */
  get visibleItems(): Item[] {
    return this.includeArchived
      ? this.items
      : this.items.filter((item) => item.archived_at == null)
  }

  /**
   * Open items of a column, in position order. Closed items are kept in place;
   * archived items are hidden unless the "Show archived" toggle is on.
   */
  itemsForColumn(columnId: string): Item[] {
    return this.items
      .filter(
        (item) =>
          item.column_id === columnId && (this.includeArchived || item.archived_at == null),
      )
      .sort(byItemPosition)
  }

  /**
   * Items of a column after the active label, priority and kind filters, then
   * reordered for the active view sort. Filters are AND-combined: a card must
   * satisfy every active filter to show.
   */
  visibleItemsForColumn(columnId: string): Item[] {
    let items = this.itemsForColumn(columnId)
    if (this.labelFilter) {
      items = items.filter((item) => item.labels.some((label) => label.id === this.labelFilter))
    }
    if (this.priorityFilter !== null) {
      items = items.filter((item) => item.priority === this.priorityFilter)
    }
    if (this.kindFilter !== null) {
      items = items.filter((item) => kindOf(item) === this.kindFilter)
    }
    return this.applySort(items)
  }

  /**
   * Reorder a column's filtered loaded cards for the active view sort.
   * `'position'` returns them untouched; the created_at modes sort by timestamp
   * (newest first / oldest first), falling back to position so equal timestamps
   * stay deterministic. `'activity'` (PB-122) sorts newest-activity first by
   * activity_at (updated_at ∨ newest comment), with the same created_at/position
   * tiebreak. The result feeds the render cap, so the newest/oldest
   * cards surface first rather than only the first position-ordered slice.
   */
  private applySort(items: Item[]): Item[] {
    if (this.sortMode === 'position') return items
    if (this.sortMode === 'activity') {
      return [...items].sort((a, b) => {
        const byActivity = b.activity_at.localeCompare(a.activity_at)
        if (byActivity !== 0) return byActivity
        const byCreated = b.created_at.localeCompare(a.created_at)
        if (byCreated !== 0) return byCreated
        return byItemPosition(a, b)
      })
    }
    const direction = this.sortMode === 'newest' ? -1 : 1
    return [...items].sort((a, b) => {
      const byCreated = a.created_at.localeCompare(b.created_at)
      if (byCreated !== 0) return direction * byCreated
      return byItemPosition(a, b)
    })
  }

  /**
   * Loaded items of a column that match the active filters and fall within the
   * render cap (display limit plus any revealed extras).
   */
  shownItemsForColumn(columnId: string): Item[] {
    return this.visibleItemsForColumn(columnId).slice(0, this.renderCap(columnId))
  }

  /**
   * Loaded-but-not-yet-shown cards in a column: the count behind a "Show N
   * more" control. Zero once the visible window covers every filtered loaded
   * card, or when the limit is `'all'`.
   */
  hiddenLoadedCount(columnId: string): number {
    return Math.max(
      0,
      this.visibleItemsForColumn(columnId).length - this.shownItemsForColumn(columnId).length,
    )
  }

  /**
   * How many loaded cards one "Show N more" click reveals: the display limit,
   * clamped to what remains hidden. Zero when nothing is hidden.
   */
  columnRevealCount(columnId: string): number {
    const hidden = this.hiddenLoadedCount(columnId)
    if (hidden === 0) return 0
    return this.displayLimit === 'all' ? hidden : Math.min(this.displayLimit, hidden)
  }

  /**
   * True while a column still has something to reveal (hidden loaded cards) or
   * load (a next server page). False means the column shows every filtered
   * item and is fully loaded, so its progressive control disappears.
   */
  columnHasControl(columnId: string): boolean {
    return this.hiddenLoadedCount(columnId) > 0 || this.columnHasMore(columnId)
  }

  /**
   * The single per-column progressive action: reveal the next slice of already
   * loaded cards when any are hidden, otherwise fetch the next server page.
   */
  showMoreOrLoad(columnId: string): void {
    if (this.hiddenLoadedCount(columnId) > 0) {
      this.view.revealMore(columnId, this.columnRevealCount(columnId))
      return
    }
    void this.loadMore(columnId)
  }

  /**
   * Set the per-column render cap (delegates the reset + persistence to the
   * view state).
   */
  setDisplayLimit(limit: DisplayLimit): void {
    this.view.setDisplayLimit(limit)
  }

  /**
   * Show or hide archived items on the board.
   *
   * The flag is flipped synchronously, so already-loaded cards re-filter on the
   * next render (archived cards hide, or already-loaded archived cards reveal
   * dimmed) without waiting on the network. The board is then re-fetched with
   * include_archived so the server reconciles the snapshot; the fetch replaces
   * `items` only once its response arrives, so the board never blanks in the
   * meantime. `reconciling` stays true across that fetch (and any coalesced
   * trailing resync) to drive the transient column indication.
   */
  setIncludeArchived(show: boolean): void {
    if (!this.view.setIncludeArchived(show)) return
    if (!this.boardId) return
    this.reconciling = true
    void this.refresh()
  }

  /** Effective render cap for a column: the display limit plus revealed extras. */
  private renderCap(columnId: string): number {
    if (this.displayLimit === 'all') return Infinity
    return this.displayLimit + (this.columnReveal[columnId] ?? 0)
  }

  /** True when the item has at least one incoming `blocks` relation. */
  isBlocked(itemId: string): boolean {
    return (this.blockedBy[itemId]?.length ?? 0) > 0
  }

  /**
   * True when the column has server-side pages beyond the items loaded so far
   * (a non-null next_cursor). A fully loaded column — every small board —
   * returns false and shows no Load more control.
   */
  columnHasMore(columnId: string): boolean {
    return typeof this.columnCursors[columnId] === 'string'
  }

  /**
   * Label count for the Load more button: the column's snapshot total plus
   * items created live while the column was not fully loaded.
   */
  columnLoadMoreCount(columnId: string): number {
    return (this.columnTotals[columnId] ?? 0) + this.pendingCount(columnId)
  }

  /** Held-out live items still waiting for a column's next page. */
  private pendingCount(columnId: string): number {
    return (this.columnPendingItems[columnId] ?? []).length
  }

  /** How many incoming `blocks` relations the item currently has. */
  blockedCount(itemId: string): number {
    return this.blockedBy[itemId]?.length ?? 0
  }

  /**
   * The parent item of `item`, or null for a top-level item. A dangling parent
   * id (parent not in the current board) resolves to null rather than throwing.
   */
  parentOf(item: Item): Item | null {
    const parentId = item.parent_item_id
    if (!parentId) return null
    return this.items.find((i) => i.id === parentId) ?? null
  }

  /**
   * Items that may be the parent of an item of `kind`, excluding `excludeId`
   * (the item being edited). The kind/parent rule forbids a parent of the same
   * or lower level, so no descendant can be offered and cycles are impossible;
   * self-exclusion is the only guard needed.
   */
  parentCandidates(kind: ItemKind, excludeId?: string): Item[] {
    const kinds = parentKindsFor(kind)
    if (kinds.length === 0) return []
    return this.items
      .filter((i) => i.id !== excludeId && kinds.includes(kindOf(i)))
      .sort(byItemPosition)
  }

  toggleLabelFilter(labelId: string): void {
    this.view.toggleLabelFilter(labelId)
  }

  togglePriorityFilter(priority: number): void {
    this.view.togglePriorityFilter(priority)
  }

  toggleKindFilter(kind: ItemKind): void {
    this.view.toggleKindFilter(kind)
  }

  /**
   * Set the view sort (delegates the reveal reset + persistence to the view
   * state).
   */
  setSortMode(mode: SortMode): void {
    this.view.setSortMode(mode)
  }

  /**
   * Clear every filter — label, priority, kind and search (the board's "All"
   * button) — plus the raw search-box text and any inline parse error.
   */
  clearFilters(): void {
    this.view.clearFilters()
  }

  /** True when any card filter (label, priority, kind or search) is active. */
  hasActiveFilters(): boolean {
    return this.view.hasActiveFilters()
  }

  /**
   * Update the search-box text and schedule the debounced apply (the controller
   * lives in BoardViewState; the callback passed to it re-fetches the board).
   */
  setSearchInput(text: string): void {
    this.view.setSearchInput(text)
  }

  // --- loading ------------------------------------------------------------

  /** Load workspaces, then auto-select the first workspace and board. */
  async loadWorkspaces(): Promise<void> {
    this.loading = true
    try {
      const { workspaces } = await api.listWorkspaces()
      this.workspaces = workspaces
      this.error = null

      if (!this.workspaceId || !workspaces.some((w) => w.id === this.workspaceId)) {
        const first = workspaces[0]
        if (first) await this.selectWorkspace(first.id)
        else this.resetBoard()
      }
    } catch (err) {
      this.error = api.errorMessage(err)
    } finally {
      this.loading = false
    }
  }

  async selectWorkspace(id: string): Promise<void> {
    this.workspaceId = id
    this.resetBoard()
    await this.loadBoards(id)
  }

  async loadBoards(workspaceId: string): Promise<void> {
    try {
      const { boards } = await api.listBoards(workspaceId)
      // A slower response for a workspace we already navigated away from.
      if (this.workspaceId !== workspaceId) return
      this.boards = boards
      this.error = null

      if (!this.boardId || !boards.some((b) => b.id === this.boardId)) {
        const first = boards[0]
        if (first) await this.selectBoard(first.id)
      }
    } catch (err) {
      this.error = api.errorMessage(err)
    }
  }

  async selectBoard(id: string): Promise<void> {
    this.boardId = id
    await this.loadBoard(id)
  }

  private async loadBoard(id: string): Promise<void> {
    this.loading = true
    try {
      await this.fetchBoard(id)
    } finally {
      this.loading = false
    }
  }

  /**
   * Re-fetch the active board. Used on SSE reconnect and whenever an event
   * cannot be applied incrementally.
   *
   * This is a PAGINATED resync: it never requests the full item list. For each
   * column it re-fetches pages from page 1 up to (bounded by) the number of
   * cards currently loaded, then resumes from the resulting cursor. Reveal
   * windows and the pending-count gating for unloaded items are preserved.
   *
   * Coalesced single-flight: a burst of resync triggers (e.g. malformed live
   * events) shares one in-flight load; at most one trailing resync runs after
   * it if requests arrived meanwhile, so N triggers never become N fetches.
   */
  async refresh(): Promise<void> {
    const boardId = this.boardId
    if (!boardId) return
    if (this.snapshotInFlight) {
      this.snapshotQueued = true
      return
    }
    this.snapshotInFlight = true
    try {
      await this.fetchBoard(boardId)
    } finally {
      this.snapshotInFlight = false
      if (this.snapshotQueued) {
        this.snapshotQueued = false
        // Await the trailing resync so a toggle's `reconciling` indication is
        // only cleared once the whole coalesced sequence has settled.
        await this.refresh()
      }
      // A "Show archived" reconcile (which set reconciling) is done once the
      // fetch — and any resync queued behind it — has settled. No-op otherwise.
      this.reconciling = false
    }
  }

  /**
   * Load the active board from the server.
   *
   * The board+columns read is LIGHTWEIGHT (`include_items=false`): it carries
   * the columns and their live `item_count` but no items. Items arrive only
   * through the per-column paginated endpoint (page 1 on load, 1..N on a
   * resync), so the board view never requests the full item list. `targets`
   * captures how many items are already loaded per column before any of them is
   * replaced; an initial load has none and therefore fetches exactly page 1.
   */
  private async fetchBoard(id: string): Promise<void> {
    // Bumped at the start of every load; continuations below discard their
    // result once a newer load (e.g. a resync racing the initial load) begins.
    const seq = ++this.loadSeq
    const searching = this.view.hasSearchQuery()
    const targets = this.loadedCountByColumn()
    try {
      const snapshot = await api.getBoard(id, {
        includeArchived: this.includeArchived,
        // Lazy path: a lightweight board+columns read (`items:[]`, with
        // per-column item_count), then per-column pages below. Searched path:
        // the server returns the full composite with the FILTERED items array,
        // so no include_items opt-out and no per-column requests.
        includeItems: searching ? undefined : false,
        q: searching ? this.view.searchQuery : undefined,
      })
      if (this.boardId !== id || seq !== this.loadSeq) return // stale response
      this.columns = [...snapshot.columns].sort(byPosition)
      // Every column carries its item_count — the Load more total (live
      // creations add on top via columnPendingItems).
      this.columnTotals = Object.fromEntries(
        snapshot.columns.map((c) => [c.id, c.item_count ?? 0]),
      )
      // Reset the cursor map; loadColumnPages fills each column's next_cursor as
      // its pages land, so no column shows a phantom Load more in the meantime.
      // Under search it stays empty: the queried view is a single, complete read.
      this.columnCursors = {}
      this.columnPendingItems = {}
      this.loadingMoreColumn = null
      if (searching) {
        // Filtering happens in-memory over the whole board snapshot, so the
        // response already holds every matching item: render it directly and
        // issue no /items requests while a query is active.
        this.items = snapshot.items.filter((item) => this.itemIsWellFormed(item))
      } else {
        // Keep the loaded cards of columns that still belong to this board
        // until their page replaces them; drop items of any other board.
        const columnIds = new Set(snapshot.columns.map((c) => c.id))
        this.items = this.items.filter((item) => columnIds.has(item.column_id))
        await this.loadColumnPages(id, seq, targets)
        if (this.boardId !== id || seq !== this.loadSeq) return
      }
      // Attach the board only once its items are in, so the board view never
      // flashes empty columns while the fetch is still in flight. On a resync
      // this simply re-confirms the already-shown board.
      this.board = snapshot.board
      // Badges need per-item relations, which neither the snapshot nor a page
      // carries; hydrate only the cards currently rendered, in the background.
      void this.hydrateRelations(id, seq)
      // Workspace labels back the label list; tolerate failure (board view
      // renders item-embedded labels regardless).
      if (this.workspaceId) {
        const workspaceId = this.workspaceId
        void api
          .listLabels(workspaceId)
          .then(({ labels }) => {
            if (this.boardId === id && seq === this.loadSeq) this.labels = labels
          })
          .catch(() => {
            /* non-fatal: labels are adornments */
          })
      }
      this.view.searchError = null
      this.error = null
    } catch (err) {
      if (seq !== this.loadSeq) return
      // A rejected search query comes back as a 400 on the board read. Surface
      // its parse message verbatim next to the search box and leave the board
      // (and its loaded cards) untouched; every other failure keeps the
      // existing full-board error behavior.
      if (err instanceof api.ApiError && err.status === 400 && this.view.hasSearchQuery()) {
        this.view.searchError = api.errorMessage(err)
        return
      }
      this.error = api.errorMessage(err)
    }
  }

  private resetBoard(): void {
    this.boardId = null
    this.board = null
    this.columns = []
    this.items = []
    this.labels = []
    // Clear the board filters (which also drops the raw search text and any
    // inline parse error) and restart every column's reveal window.
    this.view.clearFilters()
    this.blockedBy = {}
    this.columnCursors = {}
    this.columnTotals = {}
    this.columnPendingItems = {}
    this.loadingMoreColumn = null
    this.reconciling = false
  }

  /** Loaded items per column, used to bound a paginated resync. */
  private loadedCountByColumn(): Record<string, number> {
    const counts: Record<string, number> = {}
    for (const item of this.items) counts[item.column_id] = (counts[item.column_id] ?? 0) + 1
    return counts
  }

  /**
   * The loaded cards currently rendered: each column's filtered items within
   * its render cap. Used by relations hydration, which therefore never fans out
   * to every loaded (let alone every board) item.
   */
  private visibleWindowItems(): Item[] {
    const out: Item[] = []
    for (const column of this.columns) out.push(...this.shownItemsForColumn(column.id))
    return out
  }

  /**
   * Load pages 1..N of every column through the paginated endpoint, replacing
   * that column's loaded items. An initial load (`targets` empty, nothing
   * loaded) fetches exactly page 1 per column; a resync re-fetches pages until
   * the fetched count covers `targets[columnId]` — bounded by the loaded count,
   * so a column with three loaded pages re-fetches three pages and resumes from
   * page 3's cursor. Reveal windows are left untouched here. A failed fetch
   * keeps that column's existing loaded items and leaves it fully loaded, so a
   * transient error never empties the board or invents a Load more control.
   */
  private async loadColumnPages(
    boardId: string,
    seq: number,
    targets: Record<string, number>,
  ): Promise<void> {
    const columnIds = this.columns.map((c) => c.id)
    await Promise.all(
      columnIds.map(async (columnId) => {
        const target = targets[columnId] ?? 0
        const collected: Item[] = []
        let cursor: string | undefined
        let next: string | null = null
        try {
          for (;;) {
            const page = await api.listBoardItems(boardId, {
              column_id: columnId,
              limit: COLUMN_PAGE_SIZE,
              cursor,
              includeArchived: this.includeArchived,
            })
            if (this.boardId !== boardId || seq !== this.loadSeq) return
            collected.push(...page.items.filter((item) => this.itemIsWellFormed(item)))
            // Last page, or we have covered the number of cards already loaded.
            if (page.next_cursor == null) {
              next = null
              break
            }
            cursor = page.next_cursor
            if (collected.length >= target) {
              next = page.next_cursor
              break
            }
          }
        } catch {
          /* keep this column's existing loaded items; it stays fully loaded */
          return
        }
        this.replaceColumnItems(columnId, collected)
        this.columnCursors = { ...this.columnCursors, [columnId]: next }
      }),
    )
  }

  /**
   * Append the next page of one column (the Load more control). Append
   * semantics: fetched items merge by id, so a row already loaded is replaced,
   * never duplicated. next_cursor advances; null means the column is complete.
   */
  async loadMore(columnId: string): Promise<void> {
    const boardId = this.boardId
    const cursor = this.columnCursors[columnId]
    if (!boardId || typeof cursor !== 'string' || this.loadingMoreColumn === columnId) return
    // The active search query is known-bad (its parse error is shown inline);
    // paging would just repeat that 400 as a second, action-error banner.
    if (this.view.searchError) return
    this.loadingMoreColumn = columnId
    this.actionError = null
    try {
      const page = await api.listBoardItems(boardId, {
        column_id: columnId,
        limit: COLUMN_PAGE_SIZE,
        cursor,
        includeArchived: this.includeArchived,
      })
      if (this.boardId !== boardId) return
      this.mergeItems(page.items.filter((item) => this.itemIsWellFormed(item)))
      this.columnCursors = { ...this.columnCursors, [columnId]: page.next_cursor ?? null }
    } catch (err) {
      this.actionError = api.errorMessage(err)
    } finally {
      if (this.loadingMoreColumn === columnId) this.loadingMoreColumn = null
    }
  }

  // --- mutations ----------------------------------------------------------

  /**
   * Runs a mutation with shared error handling. Writes apply the server's
   * response to local state immediately; live SSE events (M2c) converge on the
   * same result, so this is correct with or without the stream.
   */
  private async mutate(run: () => Promise<void>): Promise<boolean> {
    this.saving = true
    this.actionError = null
    try {
      await run()
      return true
    } catch (err) {
      this.actionError = api.errorMessage(err)
      return false
    } finally {
      this.saving = false
    }
  }

  clearActionError(): void {
    this.actionError = null
  }

  addItem(
    columnId: string,
    title: string,
    options: {
      description?: string
      priority?: number
      kind?: ItemKind
      parentItemId?: string | null
      /** Full label set to assign on creation (PB-69). */
      labelIds?: string[]
    } = {},
  ): Promise<boolean> {
    const boardId = this.boardId
    if (!boardId) return Promise.resolve(false)
    return this.mutate(async () => {
      const input: ItemCreateInput = {
        title,
        description:
          options.description && options.description.length > 0 ? options.description : undefined,
        column_id: columnId,
        priority: options.priority,
        agent: api.DEFAULT_AGENT,
      }
      if (options.kind !== undefined) input.kind = options.kind
      if (options.parentItemId !== undefined) input.parent_item_id = options.parentItemId
      // PB-69: labels selected in the create form ride along on the create
      // request (full set — on creation REPLACE-vs-ADD is moot).
      if (options.labelIds !== undefined) input.label_ids = options.labelIds
      const item = await api.createItem(boardId, input)
      this.upsertItem(item)
    })
  }

  editItem(
    itemId: string,
    fields: {
      title?: string
      description?: string
      priority?: number
      kind?: ItemKind
      /** Present (even as null) changes the parent; absent keeps it. */
      parentItemId?: string | null
      /** Present replaces the item's full label set (label_ids REPLACE semantics). */
      labelIds?: string[]
    },
  ): Promise<boolean> {
    return this.mutate(async () => {
      const patch: ItemPatchInput = { agent: api.DEFAULT_AGENT }
      if (fields.title !== undefined) patch.title = fields.title
      if (fields.description !== undefined) patch.description = fields.description
      if (fields.priority !== undefined) patch.priority = fields.priority
      if (fields.kind !== undefined) patch.kind = fields.kind
      // Preserve the API's three-state parent semantics: only send the key when
      // the caller asked to change the parent, so an unrelated edit is a no-op.
      if ('parentItemId' in fields) patch.parent_item_id = fields.parentItemId ?? null
      if (fields.labelIds !== undefined) patch.label_ids = fields.labelIds
      const item = await api.updateItem(itemId, patch)
      this.upsertItem(item)
    })
  }

  /**
   * Create a workspace label (PB-70 on-the-fly creation from the detail
   * modal). Returns the new label, or null on failure — the caller surfaces
   * `actionError` inline. The label is upserted locally so the picker chip
   * appears immediately; the SSE label.created event converges other clients.
   */
  async createLabel(name: string, color: string): Promise<Label | null> {
    if (!this.workspaceId) {
      this.actionError = 'No active workspace.'
      return null
    }
    // Routed through mutate() like every other mutation (PB-82): shared
    // saving/actionError handling, identical error semantics. Returns the new
    // label, or null on failure — the caller surfaces `actionError` inline.
    let label: Label | null = null
    await this.mutate(async () => {
      label = await api.createLabel(this.workspaceId!, { name, color })
      this.upsertLabel(label)
    })
    return label
  }

  /** Replace the full label set of an item (the API's REPLACE semantics). */
  setItemLabels(itemId: string, labelIds: string[]): Promise<boolean> {
    return this.editItem(itemId, { labelIds })
  }

  /** PATCH just the priority of an item (0..3). */
  setItemPriority(itemId: string, priority: number): Promise<boolean> {
    return this.editItem(itemId, { priority })
  }

  toggleClosed(item: Item): Promise<boolean> {
    return this.mutate(async () => {
      const next = await api.updateItem(item.id, {
        closed: !item.closed,
        agent: api.DEFAULT_AGENT,
      })
      this.upsertItem(next)
    })
  }

  /** Archive an item: it leaves the board unless "Show archived" is on. */
  archiveItem(itemId: string): Promise<boolean> {
    return this.mutate(async () => {
      const item = await api.archiveItem(itemId, api.DEFAULT_AGENT)
      this.upsertItem(item)
    })
  }

  /** Unarchive an item: it becomes active again and returns to the board. */
  unarchiveItem(itemId: string): Promise<boolean> {
    return this.mutate(async () => {
      const item = await api.unarchiveItem(itemId, api.DEFAULT_AGENT)
      this.upsertItem(item)
    })
  }

  moveItem(itemId: string, columnId: string, index?: number): Promise<boolean> {
    return this.mutate(async () => {
      const item = await api.moveItem(itemId, {
        column_id: columnId,
        index,
        agent: api.DEFAULT_AGENT,
      })
      this.upsertItem(item)
    })
  }

  /** Reorder a column on the board: index 0 = first, past-the-end clamps. */
  moveColumn(columnId: string, index: number): Promise<boolean> {
    return this.mutate(async () => {
      const column = await api.moveColumn(columnId, { index, agent: api.DEFAULT_AGENT })
      // The API renumbers every sibling densely; the response only carries the
      // moved column. Recompute sibling positions locally for an instant,
      // consistent order — the authoritative column.moved event converges us.
      const rest = this.columns.filter((c) => c.id !== columnId)
      const at = Math.max(0, Math.min(index, rest.length))
      const next = [...rest.slice(0, at), column, ...rest.slice(at)]
      this.columns = next.map((c, i) => ({ ...c, position: i }))
    })
  }

  /** Move one slot up or down within its current column (-1 = up, +1 = down). */
  nudgeItem(item: Item, direction: -1 | 1): Promise<boolean> {
    const list = this.itemsForColumn(item.column_id)
    const index = list.findIndex((i) => i.id === item.id)
    if (index < 0) return Promise.resolve(false)
    // The API index is relative to the column WITHOUT the moved item.
    return this.moveItem(item.id, item.column_id, index + direction)
  }

  transferItem(itemId: string, toAgent: string, note?: string): Promise<boolean> {
    return this.mutate(async () => {
      const item = await api.transferItem(itemId, {
        to_agent: toAgent,
        note: note && note.length > 0 ? note : undefined,
        agent: api.DEFAULT_AGENT,
      })
      this.upsertItem(item)
    })
  }

  /** Full item detail (item + comments) for the item-detail modal. */
  loadItem(itemId: string): Promise<ItemDetail> {
    return api.getItem(itemId)
  }

  /** Keep a comment visible in an open detail modal: buffer by id, newest last. */
  private rememberComment(comment: Comment): void {
    this.recentComments = [...this.recentComments.filter((c) => c.id !== comment.id), comment]
  }

  /**
   * Raise the local item's activity_at to `at` when it is newer (PB-122).
   * Shared by the SSE comment.added branch and the optimistic addComment path
   * so both live-write and event-driven updates converge identically.
   */
  private bumpItemActivity(itemId: string, at: string): void {
    const idx = this.items.findIndex((i) => i.id === itemId)
    if (idx === -1) return
    const item = this.items[idx]
    if (item.activity_at >= at) return
    this.items[idx] = { ...item, activity_at: at }
  }

  async addComment(itemId: string, body: string): Promise<Comment> {
    const comment = await api.createComment(itemId, { body, agent: api.DEFAULT_AGENT })
    this.rememberComment(comment)
    this.bumpItemActivity(itemId, comment.created_at)
    return comment
  }

  /** Create a relation FROM `itemId`; applies the resulting edge locally. */
  addRelation(itemId: string, toItemId: string, relation: RelationKind): Promise<boolean> {
    return this.mutate(async () => {
      const edge = await api.createRelation(itemId, {
        to_item_id: toItemId,
        relation,
        agent: api.DEFAULT_AGENT,
      })
      this.applyRelationCreated(edge)
      this.relationsVersion += 1
    })
  }

  /** Remove a relation by id; drops it from the local badge index. */
  removeRelation(relationId: string): Promise<boolean> {
    return this.mutate(async () => {
      await api.deleteRelation(relationId)
      this.applyRelationDeleted(relationId)
      this.relationsVersion += 1
    })
  }

  // --- live events --------------------------------------------------------

  /**
   * Apply one SSE event to the open board.
   *
   * The contract pins the event type and envelope but leaves the affected
   * entity loosely specified, so this applies the entity when it is present
   * and falls back to a snapshot re-fetch when it is not — correct either way.
   */
  applyEvent(event: KanbanEvent): void {
    this.lastEventAt = event.at ?? new Date().toISOString()

    // Ignore board-scoped events for a different board than the one on screen.
    if (event.board_id && event.board_id !== this.boardId) return

    const entityId =
      event.id ??
      event.item?.id ??
      event.column?.id ??
      event.label?.id ??
      event.relation?.id ??
      event.board?.id ??
      event.workspace?.id

    switch (event.type) {
      case 'item.created':
      case 'item.updated':
      case 'item.moved':
      case 'item.transferred':
      case 'item.archived':
      case 'item.unarchived': {
        // A malformed entity must never enter the store: rendering reads
        // item.labels.some(...) directly, so a non-array `labels` would crash
        // the board. Fall back to a snapshot re-fetch, which converges on
        // server truth instead.
        if (!event.item || !this.itemIsWellFormed(event.item)) {
          void this.refresh()
          return
        }
        const item = event.item
        // Any same-board item mutation may have touched this board's children
        // (created, moved, closed, archived); detail modals refetch on bumps.
        this.itemsVersion += 1
        // A searched view is filtered server-side by the query, and we never
        // parse the query client-side, so a live event cannot be judged to
        // match. Re-issue the searched fetch instead of applying the item
        // incrementally — refresh() coalesces bursts into one trailing load,
        // so the queried result set stays exact and live.
        if (this.view.hasSearchQuery()) {
          void this.refresh()
          return
        }
        if (this.items.some((i) => i.id === item.id)) {
          this.upsertItem(item)
          return
        }
        // An item we do not hold sits beyond the loaded page. An archived
        // item that was being counted as a live, held-out row is dropped from
        // that pending list (it is hidden now either way); an unarchived item
        // or a created one is held out and counted while the column still has
        // unloaded pages (keyset pages only move forward, so it cannot be
        // merged into the loaded window). Updates/moves for an unloaded item --
        // and archived items we never held -- are ignored: the item arrives
        // with its page, or a resync picks it up.
        if (event.type === 'item.archived') {
          this.removePendingItem(item.id)
          return
        }
        if (event.type === 'item.created' || event.type === 'item.unarchived') {
          if (this.columnHasMore(item.column_id)) {
            this.addPendingItem(item)
            return
          }
          this.upsertItem(item)
        }
        return
      }

      case 'column.created':
      case 'column.updated':
        if (event.column) this.upsertColumn(event.column)
        else void this.refresh()
        return

      case 'column.moved':
        // The event carries the FULL reordered sibling list (every column's
        // position may change); apply it wholesale, else resync.
        if (event.columns && Array.isArray(event.columns) && event.columns.length > 0) {
          this.columns = [...event.columns].sort(byPosition)
        } else {
          void this.refresh()
        }
        return

      case 'column.deleted':
        if (entityId) this.columns = this.columns.filter((c) => c.id !== entityId)
        else void this.refresh()
        return

      case 'label.created':
      case 'label.updated':
        if (event.label) this.upsertLabel(event.label)
        else void this.refresh()
        return

      case 'label.deleted':
        if (entityId) this.labels = this.labels.filter((l) => l.id !== entityId)
        else void this.refresh()
        return

      case 'board.created': {
        const workspaceId = event.board?.workspace_id ?? this.workspaceId
        if (workspaceId) void this.loadBoards(workspaceId)
        return
      }

      case 'workspace.created':
        void this.loadWorkspaces()
        return

      case 'comment.added':
        // Comments are not part of the board view; an open detail modal merges
        // this in. Reject malformed payloads rather than storing them.
        if (
          event.comment &&
          typeof event.comment.id === 'string' &&
          typeof event.comment.item_id === 'string'
        ) {
          this.rememberComment(event.comment)
          // PB-122: comment events carry no item payload, so bump the item's
          // local activity_at here — live 'Recently updated' sorting converges
          // without a refetch (contract: comments do not emit item events and
          // do not bump updated_at server-side).
          this.bumpItemActivity(event.comment.item_id, event.comment.created_at)
        } else {
          void this.refresh()
        }
        return

      case 'relation.created':
        // The event carries the outgoing edge (item = target). Apply it to the
        // badge index when well-formed; otherwise resync from the server.
        if (event.relation && this.relationIsWellFormed(event.relation)) {
          this.applyRelationCreated(event.relation)
        } else {
          void this.refresh()
        }
        this.relationsVersion += 1
        return

      case 'relation.deleted':
        if (event.relation && typeof event.relation.id === 'string') {
          this.applyRelationDeleted(event.relation.id)
        } else if (typeof entityId === 'string') {
          this.applyRelationDeleted(entityId)
        } else {
          void this.refresh()
        }
        this.relationsVersion += 1
        return

      default:
        void this.refresh()
    }
  }

  /**
   * The live-stream contract leaves entity payloads loosely specified, so an
   * entity that is present but malformed (missing `labels` array, non-boolean
   * `closed`, non-string `position`/`created_at`, non-numeric `priority`) is
   * rejected here rather than applied — applyEvent then resyncs. Rendering
   * and ordering would otherwise crash or misbehave: Card reads
   * item.labels.some(...) and item.priority, and byItemPosition calls
   * created_at.localeCompare(...). All five fields are non-optional on Item,
   * so a conforming ItemView always passes this check.
   */
  private itemIsWellFormed(item: Item): boolean {
    return (
      Array.isArray(item.labels) &&
      typeof item.closed === 'boolean' &&
      typeof item.position === 'string' &&
      typeof item.created_at === 'string' &&
      typeof item.priority === 'number'
    )
  }

  /**
   * SSE relation events carry the OUTGOING edge (item = the target), so a
   * `blocks` event marks the target item as the blocked one. Symmetric
   * relations never mark a card.
   */
  private applyRelationCreated(edge: Relation): void {
    if (edge.relation !== 'blocks') return
    const itemId = edge.item?.id
    if (typeof itemId !== 'string') return
    const existing = this.blockedBy[itemId] ?? []
    if (existing.includes(edge.id)) return
    this.blockedBy = { ...this.blockedBy, [itemId]: [...existing, edge.id] }
  }

  private applyRelationDeleted(relationId: string): void {
    let changed = false
    const next: Record<string, string[]> = {}
    for (const [itemId, ids] of Object.entries(this.blockedBy)) {
      const remaining = ids.filter((id) => id !== relationId)
      if (remaining.length !== ids.length) changed = true
      if (remaining.length > 0) next[itemId] = remaining
    }
    if (changed) this.blockedBy = next
  }

  /**
   * The board snapshot carries no relations, so a card cannot know it is
   * blocked without a per-item fetch. Fetch details (and thus edges) only for
   * the cards currently rendered — never for every loaded item — and seed the
   * badge index; failures are non-fatal (a missing badge, no crash). Runs on
   * every board (re)load, healing anything missed while disconnected.
   */
  private async hydrateRelations(boardId: string, seq: number): Promise<void> {
    const visible = this.visibleWindowItems()
    const results = await Promise.all(
      visible.map(async (item) => {
        try {
          const detail = await api.getItem(item.id)
          return { id: item.id, incoming: detail.relations?.incoming }
        } catch {
          return { id: item.id, incoming: undefined }
        }
      }),
    )
    if (this.boardId !== boardId || seq !== this.loadSeq) return
    const next: Record<string, string[]> = {}
    for (const result of results) {
      if (!Array.isArray(result.incoming)) continue
      const blocks = result.incoming.filter((edge) => edge.relation === 'blocks')
      if (blocks.length > 0) next[result.id] = blocks.map((edge) => edge.id)
    }
    this.blockedBy = next
    this.relationsVersion += 1
  }

  /** A relation edge only needs its id, other-item id and a known kind. */
  private relationIsWellFormed(edge: Relation): boolean {
    return (
      typeof edge.id === 'string' &&
      typeof edge.item?.id === 'string' &&
      (edge.relation === 'blocks' ||
        edge.relation === 'relates_to' ||
        edge.relation === 'duplicates')
    )
  }

  private upsertItem(item: Item): void {
    this.mergeItems([item])
    // Local item writes converge the open detail modal immediately, without
    // waiting for the SSE echo of the same mutation.
    this.itemsVersion += 1
  }

  /**
   * Merge items into the store by id: an existing row is replaced in place,
   * a new row is appended. Never duplicates, so the snapshot/refresh path and
   * paginated appends share it safely.
   */
  private mergeItems(incoming: Item[]): void {
    if (incoming.length === 0) return
    const byId = new Map(this.items.map((item) => [item.id, item]))
    for (const item of incoming) byId.set(item.id, item)
    this.items = [...byId.values()]
  }

  /** Drop duplicate ids, keeping the last value (id is the item's identity). */
  private dedupeById(items: Item[]): Item[] {
    const byId = new Map<string, Item>()
    for (const item of items) byId.set(item.id, item)
    return [...byId.values()]
  }

  /**
   * Replace one column's loaded items with a freshly fetched page, dropping
   * that column's previous items; other columns are untouched. Dedupe by id so
   * an item already held (a local mutation or live event) is never duplicated.
   */
  private replaceColumnItems(columnId: string, items: Item[]): void {
    const others = this.items.filter((item) => item.column_id !== columnId)
    this.items = this.dedupeById([...others, ...items])
  }

  /** Hold a live-created/unarchived item out of a not-fully-loaded column. */
  private addPendingItem(item: Item): void {
    const list = this.columnPendingItems[item.column_id] ?? []
    if (list.some((i) => i.id === item.id)) return
    this.columnPendingItems = {
      ...this.columnPendingItems,
      [item.column_id]: [...list, item],
    }
  }

  /** Drop a held-out item from the pending count (e.g. it was archived). */
  private removePendingItem(itemId: string): void {
    let changed = false
    const next: Record<string, Item[]> = {}
    for (const [columnId, list] of Object.entries(this.columnPendingItems)) {
      const remaining = list.filter((i) => i.id !== itemId)
      if (remaining.length !== list.length) changed = true
      if (remaining.length > 0) next[columnId] = remaining
    }
    if (changed) this.columnPendingItems = next
  }

  private upsertColumn(column: Column): void {
    const next = this.columns.filter((c) => c.id !== column.id)
    next.push(column)
    this.columns = next.sort(byPosition)
  }

  private upsertLabel(label: Label): void {
    const next = this.labels.filter((l) => l.id !== label.id)
    next.push(label)
    // Keep the store's label list name-sorted like the server's list endpoint,
    // so a locally upserted label lands in its sorted place instead of last.
    this.labels = next.sort((a, b) => a.name.localeCompare(b.name))
  }

  /**
   * PB-72 picker choices: usage-ranked top-N by default, literal-substring
   * search with ?q= (filter-then-rank, PB-73 contract). `top` omitted → the
   * query alone (PB-73: q without top returns ALL matches, ranked) — used by
   * the picker's unwindowed exact-name guard. Failures resolve to an empty
   * list — the picker degrades to create-only rather than breaking the modal.
   */
  async searchLabelChoices(q: string, top?: number): Promise<Label[]> {
    if (!this.workspaceId) return []
    try {
      const params = q
        ? (top != null ? { q, top } : { q })
        : (top != null ? { top } : undefined)
      const { labels } = await api.listLabels(this.workspaceId, params)
      return labels
    } catch {
      return []
    }
  }
}

export const kanban = new KanbanStore()
