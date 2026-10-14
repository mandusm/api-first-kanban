<script lang="ts">
  import Column from './Column.svelte'
  import ItemDetailModal from './ItemDetailModal.svelte'
  import TransferDialog from './TransferDialog.svelte'
  import { ITEM_KINDS, labelStyle, PRIORITIES } from './format'
  import { DISPLAY_LIMIT_OPTIONS, SORT_OPTIONS, parseDisplayLimit, parseSortMode } from './boardViewState.svelte'
  import type { KanbanStore } from './kanban.svelte'
  import type { Item } from './types'

  let { store }: { store: KanbanStore } = $props()

  let transferTarget = $state<Item | null>(null)
  let openItem = $state<Item | null>(null)

  // PB-112/PB-114: clicking a parent reference or a child row in the modal
  // loads THAT item into the modal (replacing the current one) without closing
  // it. The modal re-derives itemId/liveItem/detail from the new `item` prop.
  const navigateTo = (item: Item) => (openItem = item)

  const openItemCount = $derived(store.visibleItems.filter((item) => !item.closed).length)

  // PB-121: the column strip is the only horizontal overflow container on the
  // board; make it keyboard-scrollable and translate vertical wheel deltas into
  // horizontal scroll (only when it actually overflows — no hijack otherwise).
  let boardStrip = $state<HTMLElement | null>(null)

  const stripOverflow = () => {
    const el = boardStrip
    return !!el && el.scrollWidth > el.clientWidth
  }

  const stripKeydown = (event: KeyboardEvent) => {
    const el = boardStrip
    if (!el || !stripOverflow()) return
    let delta: number | null = null
    if (event.key === 'ArrowLeft') delta = -1
    else if (event.key === 'ArrowRight') delta = 1
    else if (event.key === 'Home') {
      el.scrollLeft = 0
      event.preventDefault()
      return
    } else if (event.key === 'End') {
      el.scrollLeft = el.scrollWidth
      event.preventDefault()
      return
    }
    if (delta === null) return
    // Scroll by ~one column width (first column + gap; fallback 320px).
    const first = el.querySelector<HTMLElement>('[data-column]')
    const gap = parseFloat(getComputedStyle(el).columnGap || '0') || 0
    const step = (first ? first.offsetWidth + gap : 0) || 320
    el.scrollLeft += delta * step
    event.preventDefault()
  }

  // Svelte attaches `onwheel` passively, and we need preventDefault here, so
  // bind the wheel handler manually with { passive: false }.
  const stripWheelAction = (node: HTMLElement) => {
    const onWheel = (event: WheelEvent) => {
      const el = boardStrip
      if (!el || !stripOverflow()) return
      // PB-123 focused scrolling: when the pointer is over a column (anywhere
      // inside a column root), do NOT intercept — let native vertical page
      // scrolling happen. Only hijack deltaY over the strip background/edges.
      if (
        event.target instanceof Element &&
        event.target.closest('[data-column], [role="group"]')
      )
        return
      // Native horizontal wheel (deltaX) already works; only translate deltaY.
      if (event.deltaX !== 0 || event.deltaY === 0) return
      const delta = event.deltaMode === 1 ? event.deltaY * 40 : event.deltaY
      const max = el.scrollWidth - el.clientWidth
      const before = el.scrollLeft
      // Contain overscroll: at an edge in the scroll direction, let the page scroll.
      if ((delta < 0 && before <= 0) || (delta > 0 && before >= max)) return
      event.preventDefault()
      el.scrollLeft = before + delta
    }
    node.addEventListener('wheel', onWheel, { passive: false })
    return { destroy: () => node.removeEventListener('wheel', onWheel) }
  }
</script>

{#if store.board}
  <section>
    <header class="mb-3 flex flex-wrap items-baseline gap-x-3 gap-y-1">
      <h2 class="text-lg font-semibold text-slate-900">{store.board.name}</h2>
      <span class="text-xs text-slate-500">
        {store.columns.length}
        {store.columns.length === 1 ? 'column' : 'columns'} ·
        {openItemCount}
        open · {store.visibleItems.length}
        total
      </span>
      <label class="ml-auto self-center flex items-center gap-1.5 text-xs text-slate-500">
        <span>Show per column</span>
        <select
          class="rounded border border-slate-300 bg-white px-1.5 py-0.5 text-xs text-slate-700"
          data-display-limit
          aria-label="Cards shown per column"
          title="Cards shown per column"
          value={String(store.displayLimit)}
          onchange={(event) => store.setDisplayLimit(parseDisplayLimit(event.currentTarget.value))}
        >
          {#each DISPLAY_LIMIT_OPTIONS as option (String(option))}
            <option value={String(option)}>{option === 'all' ? 'All' : option}</option>
          {/each}
        </select>
      </label>
    </header>

    <div class="mb-3 flex flex-wrap items-center gap-1.5">
      <span class="text-[11px] font-semibold uppercase tracking-wide text-slate-400">Filter</span>
      <button
        class="rounded-full border border-slate-300 px-2 py-0.5 text-[11px] font-medium text-slate-600 hover:bg-slate-100"
        class:bg-slate-900={!store.hasActiveFilters()}
        class:text-white={!store.hasActiveFilters()}
        class:border-slate-900={!store.hasActiveFilters()}
        onclick={() => store.clearFilters()}
      >
        All
      </button>
      {#each PRIORITIES as priority (priority.value)}
        <button
          class="rounded-full border px-2 py-0.5 text-[11px] font-medium {priority.badgeClass}"
          class:ring-2={store.priorityFilter === priority.value}
          class:ring-slate-400={store.priorityFilter === priority.value}
          title={`Filter by ${priority.label} ${priority.name}`}
          onclick={() => store.togglePriorityFilter(priority.value)}
        >
          {priority.label} {priority.name}
        </button>
      {/each}
      <span class="mx-1 h-4 w-px bg-slate-200" aria-hidden="true"></span>
      {#each ITEM_KINDS as kind (kind.value)}
        <button
          class="rounded-full border border-slate-300 px-2 py-0.5 text-[11px] font-medium text-slate-600 hover:bg-slate-100"
          class:ring-2={store.kindFilter === kind.value}
          class:ring-slate-400={store.kindFilter === kind.value}
          title={`Filter by ${kind.label}`}
          onclick={() => store.toggleKindFilter(kind.value)}
        >
          {kind.label}
        </button>
      {/each}
      {#if store.labels.length > 0}
        <span class="mx-1 h-4 w-px bg-slate-200" aria-hidden="true"></span>
        {#each store.labels as label (label.id)}
          <button
            class="min-w-0 max-w-full truncate rounded-full border px-2 py-0.5 text-[11px] font-medium"
            title={label.name}
            class:ring-2={store.labelFilter === label.id}
            class:ring-slate-400={store.labelFilter === label.id}
            style={labelStyle(label.color)}
            onclick={() => store.toggleLabelFilter(label.id)}
          >
            {label.name}
          </button>
        {/each}
      {/if}
      <span class="mx-1 h-4 w-px bg-slate-200" aria-hidden="true"></span>
      <label
        class="flex cursor-pointer items-center rounded-full border border-slate-300 px-2 py-0.5 text-[11px] font-medium text-slate-600 hover:bg-slate-100"
        class:border-slate-900={store.includeArchived}
        class:bg-slate-900={store.includeArchived}
        class:text-white={store.includeArchived}
        title="Include archived items in the board (instant, persisted)"
      >
        <input
          type="checkbox"
          data-show-archived
          class="sr-only"
          checked={store.includeArchived}
          onchange={(event) => store.setIncludeArchived(event.currentTarget.checked)}
        />
        Archived
      </label>
      <span class="mx-1 h-4 w-px bg-slate-200" aria-hidden="true"></span>
      <label class="flex items-center gap-1.5 text-[11px] font-medium text-slate-500">
        <span>Sort</span>
        <select
          class="rounded-full border border-slate-300 bg-white px-2 py-0.5 text-[11px] font-medium text-slate-600 hover:bg-slate-100"
          data-sort-mode
          aria-label="Sort items"
          title="Sort each column's visible items"
          value={store.sortMode}
          onchange={(event) => store.setSortMode(parseSortMode(event.currentTarget.value))}
        >
          {#each SORT_OPTIONS as option (option.value)}
            <option value={option.value}>{option.label}</option>
          {/each}
        </select>
      </label>
    </div>

    {#if store.columns.length === 0}
      <p class="rounded-lg border border-dashed border-slate-300 p-6 text-sm text-slate-500">
        This board has no columns yet.
      </p>
    {:else}
      <!-- Scrollable-region keyboard pattern (PB-121): role=region + tabindex=0
           is the standard accessible scrollable-area idiom. -->
      <!-- svelte-ignore a11y_no_noninteractive_tabindex, a11y_no_noninteractive_element_interactions -->
      <div
        bind:this={boardStrip}
        use:stripWheelAction
        class="board-column-strip flex items-start gap-4 overflow-x-auto pb-4"
        tabindex="0"
        role="region"
        aria-label="Board columns, horizontally scrollable"
        onkeydown={stripKeydown}
      >
        {#each store.columns as column (column.id)}
          <Column
            {column}
            {store}
            onTransfer={(item) => (transferTarget = item)}
            onOpen={(item) => (openItem = item)}
          />
        {/each}
      </div>
    {/if}
  </section>
{/if}

{#if transferTarget}
  <TransferDialog item={transferTarget} {store} onClose={() => (transferTarget = null)} />
{/if}

{#if openItem}
  <ItemDetailModal item={openItem} {store} onNavigate={navigateTo} onClose={() => (openItem = null)} />
{/if}
