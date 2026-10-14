<script lang="ts">
  import { kindMeta, kindOf, priorityMeta, timeAgo } from './format'
  import LabelChip from './LabelChip.svelte'
  import type { KanbanStore } from './kanban.svelte'
  import { renderMarkdownCard } from './markdown'
  import type { Item } from './types'

  let {
    item,
    store,
    onTransfer,
    onOpen,
  }: {
    item: Item
    store: KanbanStore
    onTransfer: (item: Item) => void
    onOpen: (item: Item) => void
  } = $props()

  let editing = $state(false)
  let draftTitle = $state('')
  let draftDescription = $state('')
  let titleInput = $state<HTMLInputElement | null>(null)

  $effect(() => {
    if (editing) titleInput?.focus()
  })

  const columnItems = $derived(store.itemsForColumn(item.column_id))
  const index = $derived(columnItems.findIndex((i) => i.id === item.id))
  const priority = $derived(priorityMeta(item.priority))
  const kind = $derived(kindMeta(item.kind))
  const parent = $derived(store.parentOf(item))
  const parentKind = $derived(parent ? kindMeta(parent.kind) : null)
  // Server-computed progress counts (PB-84 fields); 0 when absent so older
  // payloads degrade to "no indicator" rather than a wrong bar.
  const totalChildren = $derived(item.total_children ?? 0)
  const closedChildren = $derived(item.closed_children ?? 0)
  const progressPct = $derived(
    totalChildren > 0 ? Math.round((closedChildren / totalChildren) * 100) : 0,
  )
  const progressFillClass = $derived(
    kindOf(item) === 'milestone' ? 'bg-indigo-500' : 'bg-violet-500',
  )

  // Archived cards render dimmed; a closed-but-active card keeps its dim.
  // (One class, not two stacked opacity utilities that would fight.)
  const dimClass = $derived(
    item.archived_at != null ? 'opacity-40' : item.closed ? 'opacity-60' : '',
  )

  function startEdit() {
    draftTitle = item.title
    draftDescription = item.description ?? ''
    editing = true
  }

  async function saveEdit() {
    if (draftTitle.trim().length === 0) return
    const ok = await store.editItem(item.id, {
      title: draftTitle.trim(),
      description: draftDescription.trim(),
    })
    if (ok) editing = false
  }

  // Native HTML5 drag source: the card carries its id, columns are drop
  // targets. Fine-grained in-column reordering stays on the ↑/↓ buttons.
  function onDragStart(event: DragEvent) {
    if (editing) {
      event.preventDefault()
      return
    }
    if (event.dataTransfer) {
      event.dataTransfer.setData('text/plain', item.id)
      event.dataTransfer.effectAllowed = 'move'
    }
  }
</script>

<article
  data-item={item.id}
  data-kind={kindOf(item)}
  draggable={!editing}
  ondragstart={onDragStart}
  class="rounded-md border p-3 shadow-xs transition hover:border-slate-300 {dimClass}"
  class:cursor-grab={!editing}
  class:border-slate-200={kindOf(item) === 'task'}
  class:bg-white={kindOf(item) === 'task'}
  class:border-indigo-300={kindOf(item) === 'milestone'}
  class:bg-indigo-50={kindOf(item) === 'milestone'}
  class:border-violet-300={kindOf(item) === 'epic'}
  class:bg-violet-50={kindOf(item) === 'epic'}
  class:border-l-4={kindOf(item) !== 'task'}
>
  {#if editing}
    <form
      onsubmit={(event) => {
        event.preventDefault()
        void saveEdit()
      }}
      class="flex flex-col gap-2"
    >
      <input
        class="rounded-md border border-slate-300 px-2 py-1 text-sm text-slate-900"
        bind:this={titleInput}
        bind:value={draftTitle}
      />
      <textarea
        class="rounded-md border border-slate-300 px-2 py-1 text-xs text-slate-700"
        bind:value={draftDescription}
        rows="2"
        placeholder="Description"
      ></textarea>
      <div class="flex justify-end gap-2 text-xs">
        <button
          type="button"
          class="rounded border border-slate-300 px-2 py-0.5 text-slate-600 hover:bg-slate-100"
          onclick={() => (editing = false)}
        >
          Cancel
        </button>
        <button
          type="submit"
          class="rounded bg-slate-900 px-2 py-0.5 font-medium text-white hover:bg-slate-700 disabled:opacity-50"
          disabled={store.saving || draftTitle.trim().length === 0}
        >
          Save
        </button>
      </div>
    </form>
  {:else}
    {#if kindOf(item) !== 'task'}
      <div class="mb-1.5 flex items-center gap-2">
        <span
          class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide {kind.badgeClass}"
        >
          {kind.icon} {kind.label}
        </span>
        {#if totalChildren > 0}
          <div
            class="flex items-center gap-1.5"
            title={`${progressPct}% of direct children closed`}
          >
            <!-- Percent lives ONLY in the tooltip (title); the bar itself is
                 announced through its role + values. -->
            <div
              class="h-1 w-14 overflow-hidden rounded bg-slate-200"
              role="progressbar"
              aria-valuenow={closedChildren}
              aria-valuemin="0"
              aria-valuemax={totalChildren}
              aria-label={`${closedChildren} of ${totalChildren} children closed`}
            >
              <div
                class="h-full rounded {progressFillClass}"
                style={`width: ${progressPct}%`}
              ></div>
            </div>
            <span class="text-[10px] tabular-nums text-slate-400">
              {closedChildren}/{totalChildren}
            </span>
          </div>
        {/if}
      </div>
    {/if}

    {#if parent && parentKind}
      <button
        type="button"
        class="mb-1 flex max-w-full items-center gap-1 rounded bg-slate-100 px-1.5 py-0.5 text-[10px] text-slate-500 hover:bg-slate-200"
        title={`Parent ${parentKind.label}: ${parent.title}`}
        onclick={() => onOpen(parent)}
      >
        <span aria-hidden="true">{parentKind.icon}</span>
        <span class="truncate">
          {parent.title}{parent.archived_at != null ? ' (archived)' : ''}
        </span>
      </button>
    {/if}

    <div class="flex min-w-0 items-start justify-between gap-2">
      <h3 class="min-w-0 wrap-anywhere text-sm font-medium text-slate-900" class:line-through={item.closed}>
        <button
          type="button"
          class="text-left hover:underline"
          title="Open item details"
          onclick={() => onOpen(item)}
        >
          {item.title}
        </button>
      </h3>
      <div class="flex shrink-0 items-center gap-1">
        {#if item.closed}
          <span
            class="rounded bg-slate-200 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-slate-600"
          >
            closed
          </span>
        {/if}
        {#if item.archived_at != null}
          <span
            class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700"
            title={`Archived ${item.archived_at}`}
          >
            archived
          </span>
        {/if}
        <span
          class="rounded border px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide {priority.badgeClass}"
          title={`${priority.label} ${priority.name}`}
        >
          {priority.label}
        </span>
        {#if store.isBlocked(item.id)}
          <span
            class="rounded border border-rose-200 bg-rose-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-rose-700"
            title={`Blocked by ${store.blockedCount(item.id)} item(s)`}
          >
            blocked
          </span>
        {/if}
      </div>
    </div>

    {#if item.description}
      <p class="card-md mt-1 line-clamp-2 text-xs text-slate-500">{@html renderMarkdownCard(item.description)}</p>
    {/if}

    {#if item.labels.length > 0}
      <!-- PB-101/PB-107: chips carry their own clamp (shared LabelChip); the
           min-w-0 here is defence-in-depth for the flex row. -->
      <ul class="mt-2 flex min-w-0 flex-wrap gap-1">
        {#each item.labels as label (label.id)}
          <li class="max-w-full">
            <LabelChip {label} />
          </li>
        {/each}
      </ul>
    {/if}

    <footer class="mt-2 flex items-center justify-between text-[11px] text-slate-400">
      <span class="flex items-center gap-1.5">
        <span class="font-mono" title="Item key">{item.key}</span>
        <span class:font-medium={item.owner_agent !== null}>
          {item.owner_agent ? `@${item.owner_agent}` : 'unassigned'}
        </span>
      </span>
      <span title={item.updated_at}>{timeAgo(item.updated_at)}</span>
    </footer>

    <div class="mt-2 flex flex-wrap items-center gap-1 border-t border-slate-100 pt-2 text-[11px]">
      <button
        class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100 disabled:opacity-30"
        title="Move up"
        aria-label="Move up"
        disabled={store.saving || index <= 0}
        onclick={() => void store.nudgeItem(item, -1)}
      >
        ↑
      </button>
      <button
        class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100 disabled:opacity-30"
        title="Move down"
        aria-label="Move down"
        disabled={store.saving || index < 0 || index >= columnItems.length - 1}
        onclick={() => void store.nudgeItem(item, 1)}
      >
        ↓
      </button>

      <span
        class="px-1 text-slate-300"
        title="Drag the card onto a column to move it there"
        aria-hidden="true">⠿</span
      >

      <span class="flex-1"></span>

      <button
        class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100"
        onclick={startEdit}
      >
        edit
      </button>
      <button
        class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100 disabled:opacity-50"
        disabled={store.saving}
        onclick={() => void store.toggleClosed(item)}
      >
        {item.closed ? 'reopen' : 'close'}
      </button>
      <button class="rounded px-1.5 py-0.5 text-slate-600 hover:bg-slate-100" onclick={() => onTransfer(item)}>
        transfer
      </button>
    </div>
  {/if}
</article>

<!--
  Card summaries render the Markdown subset in compact phrasing-only form
  (headings → bold, bullets → "•") via {@html}, so those generated tags are
  not scope-hashed by Svelte. Style them through :global() descendants of the
  scoped `.card-md` paragraph, mirroring the detail modal's `a`/`code` rules
  for the tags Tailwind preflight leaves unstyled.
-->
<style>
  .card-md :global(a) {
    color: #2563eb;
    text-decoration: underline;
  }
  .card-md :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    background: #f1f5f9;
    border-radius: 0.25rem;
    padding: 0.05rem 0.3rem;
  }
  .card-md :global(strong) {
    font-weight: 600;
  }
</style>
