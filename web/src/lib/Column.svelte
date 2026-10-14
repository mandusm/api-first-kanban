<script lang="ts">
  import Card from './Card.svelte'
  import Combobox from './Combobox.svelte'
  import LabelSelect from './LabelSelect.svelte'
  import { ITEM_KINDS, kindMeta, PRIORITIES, resolveParentId } from './format'
  import { useLabelPicker } from './useLabelPicker'
  import type { KanbanStore } from './kanban.svelte'
  import type { Column as ColumnModel, Item, ItemKind } from './types'

  let {
    column,
    store,
    onTransfer,
    onOpen,
  }: {
    column: ColumnModel
    store: KanbanStore
    onTransfer: (item: Item) => void
    onOpen: (item: Item) => void
  } = $props()

  let adding = $state(false)
  let newTitle = $state('')
  let newDescription = $state('')
  let newPriority = $state(2)
  let newKind = $state<ItemKind>('task')
  let newParentId = $state('')
  // PB-69: labels chosen before submission are sent on the create request.
  let newLabelIds = $state<string[]>([])
  let titleInput = $state<HTMLInputElement | null>(null)

  // Drop-target state. dragenter/leave fire for child elements too, so a depth
  // counter keeps the highlight on until the pointer truly leaves the column.
  let dragDepth = 0
  let dragOver = $state(false)

  // Column-reorder DnD. A column drag sets 'application/x-kanban-column' (plus
  // text/plain for dropEffect compat); card handlers ignore it via that type.
  let colDragSide = $state<'before' | 'after' | null>(null)

  const isColumnDrag = (event: DragEvent) =>
    !!event.dataTransfer?.types.includes('application/x-kanban-column')

  function onColumnDragStart(event: DragEvent) {
    if (!event.dataTransfer) return
    event.dataTransfer.setData('application/x-kanban-column', column.id)
    event.dataTransfer.setData('text/plain', column.id)
    event.dataTransfer.effectAllowed = 'move'
  }

  function colDropSide(event: DragEvent): 'before' | 'after' {
    const rect = (event.currentTarget as HTMLElement).getBoundingClientRect()
    return event.clientX < rect.left + rect.width / 2 ? 'before' : 'after'
  }

  function onColDragOver(event: DragEvent) {
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
    colDragSide = colDropSide(event)
  }

  function onColDrop(event: DragEvent) {
    event.preventDefault()
    // Reset BOTH drag modes (a column drag can arrive via the shared root
    // handlers with card-mode counters armed — mirrors the card path).
    dragDepth = 0
    dragOver = false
    const side = colDragSide
    colDragSide = null
    const dragged = event.dataTransfer?.getData('application/x-kanban-column')
    if (!dragged || !side) return
    // The API index is relative to the board WITHOUT the moved column.
    const cols = store.columns
    const from = cols.findIndex((c) => c.id === dragged)
    const self = cols.findIndex((c) => c.id === column.id)
    if (from < 0 || self < 0) return
    let index = self
    if (from < self) index -= 1
    if (side === 'after') index += 1
    // Dropping a column onto itself (same slot) is a no-op server-side.
    if (index !== from) void store.moveColumn(dragged, index)
  }

  /** Keyboard/a11y affordance mirroring the card nudge buttons. */
  function nudgeColumn(direction: -1 | 1) {
    const index = store.columns.findIndex((c) => c.id === column.id)
    const target = index + direction
    if (index < 0 || target < 0 || target >= store.columns.length) return
    void store.moveColumn(column.id, target)
  }

  function onDragEnter(event: DragEvent) {
    // A column-reorder drag takes precedence over a card drop (custom type).
    if (isColumnDrag(event)) {
      event.preventDefault()
      dragDepth += 1
      colDragSide = colDropSide(event)
      return
    }
    if (!event.dataTransfer?.types.includes('text/plain')) return
    event.preventDefault()
    dragDepth += 1
    dragOver = true
  }

  function onDragOver(event: DragEvent) {
    if (isColumnDrag(event)) {
      onColDragOver(event)
      return
    }
    if (!event.dataTransfer?.types.includes('text/plain')) return
    event.preventDefault()
    if (event.dataTransfer) event.dataTransfer.dropEffect = 'move'
    dragOver = true
  }

  function onDragLeave() {
    dragDepth = Math.max(0, dragDepth - 1)
    if (dragDepth === 0) {
      dragOver = false
      colDragSide = null
    }
  }

  function onDrop(event: DragEvent) {
    event.preventDefault()
    if (isColumnDrag(event)) {
      onColDrop(event)
      return
    }
    dragDepth = 0
    dragOver = false
    const itemId = event.dataTransfer?.getData('text/plain')
    if (!itemId) return
    // Dropping a card into the column it already occupies is a no-op: a silent
    // reorder to the end would surprise users nudging a card back.
    if (store.itemsForColumn(column.id).some((i) => i.id === itemId)) return
    // DnD moves to the END of the column: index = count of other items there
    // (the API clamps an index at/after the end to the end).
    const end = store.itemsForColumn(column.id).filter((i) => i.id !== itemId).length
    void store.moveItem(itemId, column.id, end)
  }

  const items = $derived(store.visibleItemsForColumn(column.id))
  // Render cap: only the first slice of matching loaded cards is rendered; the
  // progressive control reveals more or fetches the next page.
  const shownItems = $derived(store.shownItemsForColumn(column.id))
  // Parent options follow the kind/parent rule (task → milestone|epic,
  // epic → milestone, milestone → none); an empty choice means top-level.
  const parentOptions = $derived(
    store.parentCandidates(newKind).map((parent) => ({
      value: parent.id,
      label: `${parent.key} · ${kindMeta(parent.kind).label}: ${parent.title}`,
    }))
  )
  const canSubmit = $derived(newTitle.trim().length > 0)

  $effect(() => {
    if (adding) titleInput?.focus()
  })

  // A new kind can invalidate the chosen parent, so reset it on change.
  function onKindChange(event: Event) {
    newKind = (event.currentTarget as HTMLSelectElement).value as ItemKind
    newParentId = ''
  }

  // PB-83 label-picker wiring: the shared helper owns the windowed search,
  // the unwindowed exact guard, and label creation with the collision-aware
  // palette (same implementation as the item modal's picker). Create
  // failures surface inline next to the picker (the store's actionError
  // banner still renders globally).
  let pickerError = $state<string | null>(null)
  const picker = useLabelPicker(() => store, {
    onCreateError: (message) => (pickerError = message),
  })

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    if (!canSubmit) return
    const parentItemId = resolveParentId(newKind, newParentId)
    const ok = await store.addItem(column.id, newTitle.trim(), {
      description: newDescription.trim() || undefined,
      priority: newPriority,
      kind: newKind,
      parentItemId,
      labelIds: newLabelIds.length > 0 ? newLabelIds : undefined,
    })
    if (ok) {
      newTitle = ''
      newDescription = ''
      newPriority = 2
      newKind = 'task'
      newParentId = ''
      newLabelIds = []
      pickerError = null
      adding = false
    }
  }
</script>

<div
  data-column={column.name}
  data-drop-target={dragOver ? 'active' : undefined}
  data-reconciling={store.reconciling ? 'true' : undefined}
  role="group"
  aria-label={`${column.name} column`}
  ondragenter={onDragEnter}
  ondragover={onDragOver}
  ondragleave={onDragLeave}
  ondrop={onDrop}
  class="relative flex w-72 shrink-0 flex-col rounded-lg border bg-slate-50 transition-colors"
  class:animate-pulse={store.reconciling}
  class:border-slate-200={!dragOver && !colDragSide}
  class:border-indigo-400={dragOver}
  class:bg-indigo-50={dragOver}
  class:ring-2={dragOver}
  class:ring-indigo-300={dragOver}
>
  {#if colDragSide}
    <div
      class="pointer-events-none absolute inset-y-0 z-10 w-1 rounded bg-indigo-500"
      style={colDragSide === 'before' ? 'left:-6px' : 'right:-6px'}
      aria-hidden="true"
    ></div>
  {/if}

  <header
    class="flex cursor-grab items-center justify-between border-b border-slate-200 px-3 py-2 active:cursor-grabbing"
    draggable="true"
    role="button"
    tabindex="0"
    aria-label={`Reorder ${column.name} column: drag this header, or use the arrow buttons`}
    ondragstart={onColumnDragStart}
    title="Drag to reorder columns"
  >
    <h3 class="flex items-center gap-1.5 text-sm font-semibold text-slate-700">
      <span class="text-slate-300" aria-hidden="true">⠿</span>
      {column.name}
    </h3>
    <span class="flex items-center gap-1">
      <span class="rounded bg-slate-200 px-1.5 text-xs tabular-nums text-slate-600">
        {items.length}
      </span>
      <button
        type="button"
        class="rounded px-1 text-xs text-slate-400 hover:bg-slate-200 hover:text-slate-700 disabled:opacity-30"
        aria-label={`Move ${column.name} column left`}
        title="Move column left"
        disabled={store.saving || store.columns.findIndex((c) => c.id === column.id) <= 0}
        onclick={() => nudgeColumn(-1)}
      >◀</button>
      <button
        type="button"
        class="rounded px-1 text-xs text-slate-400 hover:bg-slate-200 hover:text-slate-700 disabled:opacity-30"
        aria-label={`Move ${column.name} column right`}
        title="Move column right"
        disabled={store.saving || store.columns.findIndex((c) => c.id === column.id) >= store.columns.length - 1}
        onclick={() => nudgeColumn(1)}
      >▶</button>
    </span>
  </header>

  <div class="flex flex-col gap-2 p-2">
    {#if adding}
      <form onsubmit={submit} class="flex flex-col gap-2 rounded-md border border-slate-300 bg-white p-2">
        <input
          class="rounded border border-slate-300 px-2 py-1 text-sm text-slate-900"
          bind:this={titleInput}
          bind:value={newTitle}
          placeholder="Item title"
        />
        <textarea
          class="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700"
          bind:value={newDescription}
          rows="2"
          placeholder="Description (optional)"
        ></textarea>
        <select
          class="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700"
          bind:value={newPriority}
          aria-label="Priority"
          title="Priority"
        >
          {#each PRIORITIES as priority (priority.value)}
            <option value={priority.value}>{priority.label} · {priority.name}</option>
          {/each}
        </select>
        <div class="flex gap-2">
          <select
            class="min-w-0 flex-1 rounded border border-slate-300 px-2 py-1 text-xs text-slate-700"
            value={newKind}
            onchange={onKindChange}
            aria-label="Kind"
            title="Kind"
          >
            {#each ITEM_KINDS as kind (kind.value)}
              <option value={kind.value}>{kind.label}</option>
            {/each}
          </select>
          {#if newKind !== 'milestone'}
            <Combobox
              options={parentOptions}
              bind:value={newParentId}
              emptyOption="Top-level (no parent)"
              placeholder="Top-level (no parent)"
              ariaLabel="Parent"
            />
          {:else}
            <span class="flex flex-1 items-center text-[10px] text-slate-400">
              Milestones are top-level.
            </span>
          {/if}
        </div>
        <LabelSelect
          bind:value={newLabelIds}
          search={picker.search}
          exactSearch={picker.exactSearch}
          create={picker.create}
          disabled={store.saving}
          placeholder="Search or create labels…"
          ariaLabel="Labels for new item"
        />
        {#if pickerError}
          <p class="mt-1 text-[11px] text-rose-700">{pickerError}</p>
        {/if}
        <div class="flex justify-end gap-2 text-xs">
          <button
            type="button"
            class="rounded border border-slate-300 px-2 py-0.5 text-slate-600 hover:bg-slate-100"
            onclick={() => (adding = false)}
          >
            Cancel
          </button>
          <button
            type="submit"
            class="rounded bg-slate-900 px-2 py-0.5 font-medium text-white hover:bg-slate-700 disabled:opacity-50"
            disabled={store.saving || !canSubmit}
          >
            Add
          </button>
        </div>
      </form>
    {:else}
      <button
        class="rounded-md border border-dashed border-slate-300 px-2 py-1 text-xs text-slate-500 hover:border-slate-400 hover:text-slate-700"
        onclick={() => {
          adding = true
          pickerError = null
        }}
      >
        + Add item
      </button>
    {/if}

    {#each shownItems as item (item.id)}
      <Card {item} {store} {onTransfer} {onOpen} />
    {/each}

    {#if items.length === 0}
      <p class="px-2 py-4 text-center text-xs text-slate-400">
        {store.hasActiveFilters() ? 'No matching items' : 'No items'}
      </p>
    {/if}

    {#if store.columnHasControl(column.id)}
      <button
        type="button"
        data-load-more={column.id}
        class="rounded-md border border-slate-300 px-2 py-1 text-xs text-slate-600 hover:bg-slate-100 disabled:opacity-50"
        disabled={store.loadingMoreColumn === column.id}
        onclick={() => store.showMoreOrLoad(column.id)}
      >
        {#if store.loadingMoreColumn === column.id}
          Loading…
        {:else if store.hiddenLoadedCount(column.id) > 0}
          Show {store.columnRevealCount(column.id)} more (of {items.length} loaded)
        {:else}
          Load more ({store.columnLoadMoreCount(column.id)})
        {/if}
      </button>
    {/if}

  </div>
</div>
