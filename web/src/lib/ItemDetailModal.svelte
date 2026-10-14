<script lang="ts">
  import { untrack } from 'svelte'
  import Combobox from './Combobox.svelte'
  import LabelSelect from './LabelSelect.svelte'
  import { ITEM_KINDS, kindMeta, kindOf, PRIORITIES, relationLabel, RELATION_KINDS, resolveParentId, statusPillClass, timeAgo } from './format'
  import LabelChip from './LabelChip.svelte'
  import { useLabelPicker } from './useLabelPicker'
  import { renderMarkdown } from './markdown'
  import type { KanbanStore } from './kanban.svelte'
  import type { Comment, Item, ItemDetail, ItemKind, Label, RelationKind } from './types'

  let {
    item,
    store,
    onNavigate,
    onClose,
  }: { item: Item; store: KanbanStore; onNavigate: (item: Item) => void; onClose: () => void } =
    $props()

  let dialog = $state<HTMLDialogElement | null>(null)
  let detail = $state<ItemDetail | null>(null)
  let loading = $state(true)
  let body = $state('')
  let error = $state<string | null>(null)
  let relationKind = $state<RelationKind>('blocks')
  let relationTargetId = $state('')
  // Hierarchy edit draft. The kind/parent pair is coupled (an epic's parent,
  // when set, must be a milestone), so it is saved together rather than patched
  // on change. Seeded once from the click-time item; edits must not be clobbered
  // by SSE.
  let draftKind = $state<ItemKind>(untrack(() => kindOf(item)))
  let draftParentId = $state(untrack(() => item.parent_item_id ?? ''))
  let labelsError = $state<string | null>(null)
  // PB-72: searchable label picker replaces the chips-cloud + "+" create form.
  // `pickerValue` is the picker's own selection state (label ids); the chip row
  // below still renders the server truth (liveItem.labels). Seeded once at
  // mount from the click-time snapshot, same contract as the hierarchy draft.
  let pickerValue = $state<string[]>(untrack(() => item.labels.map((l) => l.id)))
  let hierarchyError = $state<string | null>(null)
  // Label edit error, surfaced inline like hierarchyError above.
  // Title/description edit draft. Seeded once at edit-start from the live copy
  // so an SSE item.updated cannot touch the fields being edited — same
  // one-shot-seed contract the hierarchy draft above documents.
  let editing = $state(false)
  let draftTitle = $state('')
  let draftDescription = $state('')
  let titleInput = $state<HTMLInputElement | null>(null)

  const itemId = $derived(item.id)

  // The board store is kept current by SSE, so prefer its copy over the
  // click-time snapshot; the snapshot is only the initial fallback.
  const liveItem = $derived(store.items.find((i) => i.id === itemId) ?? item)
  const column = $derived(store.columns.find((c) => c.id === liveItem.column_id) ?? null)

  // PB-112: the clickable parent reference. Resolved through the store so it is
  // the live copy; a dangling/other-board parent id is not navigable (no Item
  // to pass onNavigate), so the row is hidden rather than shown dead.
  const parent = $derived(store.parentOf(liveItem))

  // A draft that differs from the live copy is dirty; explicit Save/Cancel own
  // the change, so the backdrop, ✕, and Escape must not discard it silently.
  const dirty = $derived(
    editing &&
      (draftTitle !== liveItem.title || draftDescription !== (liveItem.description ?? ''))
  )

  // The fetched thread plus any comments that arrived via SSE or were posted
  // from this modal — deduped by id so the same comment is never listed twice.
  const comments = $derived.by<Comment[]>(() => {
    const base = detail?.comments ?? []
    const extra = store.recentComments.filter(
      (c) => c.item_id === itemId && !base.some((b) => b.id === c.id)
    )
    return extra.length > 0 ? [...base, ...extra] : base
  })

  // Every relation on the open item, outgoing then incoming. Refreshed from
  // the API whenever the store's relation revision changes (see the effect).
  const relations = $derived([
    ...(detail?.relations?.outgoing ?? []),
    ...(detail?.relations?.incoming ?? []),
  ])

  // Children of the open item, from the detail fetch (server truth — includes
  // children the lazily-paginated board has not loaded). Archived children are
  // included and tagged; a child on another board of the workspace resolves no
  // column name from the active board's columns.
  const children = $derived(detail?.children ?? [])
  // Tasks can never have children (the kind/parent rule caps the chain at
  // milestone → epic → task), so the section is hidden for them — but shown
  // anyway when the data disagrees (defensive against legacy rows).
  const showChildren = $derived(kindOf(liveItem) !== 'task' || children.length > 0)

  // PB-88 progress display — SERVER FIELDS SINGLE SOURCE (critic condition):
  // read closed_children/total_children straight off the item payload and
  // never recompute from `children`; the archived-exclusion rule lives in the
  // server only. Prefer the refetched detail.item (this effect re-runs on
  // every itemsVersion bump, so counts converge live over SSE even without a
  // parent re-emit); the store's SSE-converged copy is the fallback.
  const progressItem = $derived(detail?.item ?? liveItem)
  const progress = $derived({
    closed: progressItem.closed_children,
    total: progressItem.total_children,
    percent:
      progressItem.total_children > 0
        ? Math.round((progressItem.closed_children / progressItem.total_children) * 100)
        : 0,
  })
  const showProgress = $derived(progressItem.total_children > 0)

  // Candidate targets for a new relation: other items in the active board.
  // They all belong to the active workspace, which the API requires.
  const relationTargets = $derived(store.items.filter((i) => i.id !== itemId))
  const relationTargetOptions = $derived(
    relationTargets.map((target) => ({ value: target.id, label: `${target.key} · ${target.title}` }))
  )

  // Parent options follow the kind/parent rule and exclude the item itself;
  // an empty choice means a top-level item.
  const parentOptions = $derived(
    store.parentCandidates(draftKind, itemId).map((parent) => ({
      value: parent.id,
      label: `${parent.key} · ${kindMeta(parent.kind).label}: ${parent.title}`,
    }))
  )

  $effect(() => {
    if (dialog && !dialog.open) dialog.showModal()
  })

  $effect(() => {
    if (editing) titleInput?.focus()
  })

  // PB-112/PB-114: navigating to a parent/child swaps the open item in place
  // (same component instance), so the one-shot drafts seeded above must re-seed
  // for the NEW item. Guard on a changed id so an SSE update to the SAME item
  // never clobbers an in-progress edit — the seed contract those drafts state.
  let navigationSeedId = $state(untrack(() => item.id))
  $effect(() => {
    if (itemId === navigationSeedId) return
    navigationSeedId = itemId
    draftKind = kindOf(liveItem)
    draftParentId = liveItem.parent_item_id ?? ''
    pickerValue = liveItem.labels.map((l) => l.id)
    editing = false
  })

  $effect(() => {
    // Re-run when the item changes, any relation mutates anywhere, or any
    // item on this board mutates (SSE or a local write — a child may have
    // been created, moved, closed or archived), so the modal's edge list,
    // children and comments converge without polling.
    void store.relationsVersion
    void store.itemsVersion
    void (async () => {
      try {
        detail = await store.loadItem(itemId)
        error = null
      } catch (err) {
        error = err instanceof Error ? err.message : String(err)
      } finally {
        loading = false
      }
    })()
  })

  async function submit(event: SubmitEvent) {
    event.preventDefault()
    const text = body.trim()
    if (text.length === 0) return
    try {
      await store.addComment(itemId, text)
      body = ''
      error = null
    } catch (err) {
      error = err instanceof Error ? err.message : String(err)
    }
  }

  // Enter edit mode: snapshot title/description into drafts so live updates
  // cannot clobber an in-progress edit.
  function startEdit() {
    draftTitle = liveItem.title
    draftDescription = liveItem.description ?? ''
    editing = true
  }

  function cancelEdit() {
    editing = false
  }

  // Save both fields through the existing store seam. The empty-title guard
  // mirrors Card.svelte; on success the modal reverts to read-only.
  async function saveEdit() {
    if (draftTitle.trim().length === 0) return
    const ok = await store.editItem(itemId, {
      title: draftTitle.trim(),
      description: draftDescription.trim(),
    })
    if (ok) editing = false
  }

  // PATCH the priority when the header picker changes. A failure surfaces in
  // the global action-error banner (the store swallows and records it).
  function onPriorityChange(event: Event) {
    const value = Number((event.currentTarget as HTMLSelectElement).value)
    if (Number.isNaN(value)) return
    void store.setItemPriority(itemId, value)
  }

  function onKindChange(event: Event) {
    draftKind = (event.currentTarget as HTMLSelectElement).value as ItemKind
    // The old parent may be illegal for the new kind; force a re-pick.
    draftParentId = ''
  }

  // PATCH kind and parent together: kind/parent only validates as a single
  // write (e.g. changing a milestone to an epic needs a parent cleared).
  async function saveHierarchy() {
    const parentItemId = resolveParentId(draftKind, draftParentId)
    const ok = await store.editItem(itemId, { kind: draftKind, parentItemId })
    hierarchyError = ok ? null : store.actionError
  }

  // Toggle one workspace label on the item and PATCH the full new set (the
  // update API's label_ids REPLACE semantics). Direct-apply, matching the
  // priority picker and relations add/remove in this modal — no draft/Save.
  // On success SSE converges the board; the modal reads liveItem from the
  // store, so chips update without local bookkeeping.
  async function toggleLabel(labelId: string) {
    const on = liveItem.labels.some((l) => l.id === labelId)
    const next = on
      ? liveItem.labels.filter((l) => l.id !== labelId).map((l) => l.id)
      : [...liveItem.labels.map((l) => l.id), labelId]
    const ok = await store.setItemLabels(itemId, next)
    labelsError = ok ? null : store.actionError
  }

  // PB-72 picker wiring, now via the shared useLabelPicker helper (PB-83):
  // windowed usage-ranked search (?top=8), unwindowed exact guard, and label
  // creation — one implementation with the item modal's create-form picker.
  // `onpick` persists the toggled set via the same REPLACE flow as the chips.
  // The assigned chip row is the visible truth; the picker only drives
  // additions/removals.
  const pickerLabels = useLabelPicker(() => store, {
    // Surface the server's real create rejection (duplicate/invalid name) in
    // the section error instead of the picker's generic load-failure message.
    onCreateError: (message) => (labelsError = message),
  })

  async function onPickerPick(label: Label, selected: boolean) {
    const base = liveItem.labels.map((l) => l.id)
    const next = selected ? [...base, label.id] : base.filter((id) => id !== label.id)
    const ok = await store.setItemLabels(itemId, next)
    labelsError = ok ? null : store.actionError
  }

  function onRelationKindChange(event: Event) {
    relationKind = (event.currentTarget as HTMLSelectElement).value as RelationKind
  }

  // POST a new relation FROM the open item. The store applies the returned
  // edge and bumps its revision, which re-fetches this modal's list.
  async function submitRelation(event: SubmitEvent) {
    event.preventDefault()
    if (relationTargetId.length === 0) return
    const ok = await store.addRelation(itemId, relationTargetId, relationKind)
    if (ok) relationTargetId = ''
  }

  function removeRelation(relationId: string) {
    void store.removeRelation(relationId)
  }

  // Block implicit closes (backdrop, ✕) while a draft is dirty; Save/Cancel are
  // the explicit exits.
  function requestClose() {
    if (dirty) return
    onClose()
  }

  // Native <dialog> renders its backdrop as part of the dialog element, so a
  // click whose target is the dialog itself is a click on the backdrop.
  function onDialogClick(event: MouseEvent) {
    if (event.target === dialog) requestClose()
  }

  // Escape closes the native dialog via its cancel event; guard it too so a
  // dirty draft is never discarded silently.
  function onCancel(event: Event) {
    if (dirty) event.preventDefault()
  }
</script>

<dialog
  bind:this={dialog}
  onclose={onClose}
  oncancel={onCancel}
  onclick={onDialogClick}
  class="m-auto w-[42rem] max-w-[94vw] rounded-lg border border-slate-200 p-0 shadow-xl backdrop:bg-slate-900/50"
>
  <div class="flex max-h-[86vh] flex-col">
    <header class="flex items-start justify-between gap-3 border-b border-slate-200 p-4">
      <div class="min-w-0">
        {#if editing}
          <input
            bind:this={titleInput}
            bind:value={draftTitle}
            class="w-full rounded-md border border-slate-300 px-2 py-1 text-base font-semibold text-slate-900"
            aria-label="Title"
            placeholder="Title"
          />
        {:else}
          <h2 class="text-base font-semibold text-slate-900" class:line-through={liveItem.closed}>
            {liveItem.title}
          </h2>
        {/if}
        <div class="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-slate-500">
          <span class="font-mono" title="Item key">{liveItem.key}</span>
          <span aria-hidden="true">·</span>
          <span>{column ? column.name : 'Unknown column'}</span>
          <span aria-hidden="true">·</span>
          <span class:font-medium={liveItem.owner_agent !== null}>
            {liveItem.owner_agent ? `@${liveItem.owner_agent}` : 'unassigned'}
          </span>
          <span aria-hidden="true">·</span>
          <span
            class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide"
            class:bg-slate-200={liveItem.closed}
            class:text-slate-600={liveItem.closed}
            class:bg-emerald-100={!liveItem.closed}
            class:text-emerald-700={!liveItem.closed}
          >
            {liveItem.closed ? 'closed' : 'open'}
          </span>
          {#if liveItem.archived_at != null}
            <span
              class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700"
            >
              archived
            </span>
          {/if}
          <label class="flex items-center gap-1">
            <span class="text-slate-400">priority</span>
            <select
              class="rounded border border-slate-300 px-1.5 py-0.5 text-xs text-slate-700 disabled:opacity-50"
              value={liveItem.priority}
              disabled={store.saving}
              aria-label="Priority"
              title="Change priority"
              onchange={onPriorityChange}
            >
              {#each PRIORITIES as priority (priority.value)}
                <option value={priority.value}>{priority.label} · {priority.name}</option>
              {/each}
            </select>
          </label>
        </div>
        {#if parent}
          <!-- PB-112: parent reference; loads the parent into this modal. -->
          <button
            type="button"
            class="mt-1.5 flex max-w-full items-center gap-1 rounded bg-slate-100 px-1.5 py-0.5 text-[11px] text-slate-600 hover:bg-slate-200"
            aria-label={`Open parent ${parent.key}`}
            title={`Open parent ${parent.key}: ${parent.title}`}
            onclick={() => onNavigate(parent)}
          >
            <span class="text-slate-400">Parent</span>
            <span aria-hidden="true">·</span>
            <span class="shrink-0 font-mono">{parent.key}</span>
            <span aria-hidden="true">·</span>
            <span class="shrink-0">{kindMeta(kindOf(parent)).label}:</span>
            <span class="truncate">{parent.title}</span>
            {#if parent.archived_at != null}
              <span class="shrink-0">(archived)</span>
            {/if}
          </button>
        {/if}
      </div>
      <div class="flex shrink-0 items-center gap-2">
        {#if !editing}
          <button
            type="button"
            class="rounded-md border border-slate-300 px-2 py-0.5 text-xs text-slate-600 hover:bg-slate-100"
            title="Edit title and description"
            onclick={startEdit}
          >
            edit
          </button>
        {/if}
        <button
          type="button"
          class="rounded-md border border-slate-300 px-2 py-0.5 text-xs text-slate-600 hover:bg-slate-100 disabled:opacity-50"
          disabled={store.saving}
          title={liveItem.archived_at != null ? 'Restore to the board' : 'Hide from the board'}
          onclick={() =>
            void (liveItem.archived_at != null
              ? store.unarchiveItem(itemId)
              : store.archiveItem(itemId))}
        >
          {liveItem.archived_at != null ? 'Unarchive' : 'Archive'}
        </button>
        <button
          type="button"
          class="shrink-0 rounded-md border border-slate-300 px-2 py-0.5 text-xs text-slate-600 hover:bg-slate-100"
          aria-label="Close"
          title="Close"
          onclick={requestClose}
        >
          ✕
        </button>
      </div>
    </header>

    <div class="flex-1 overflow-y-auto p-4">
      <section class="mb-3 rounded-md border border-slate-200 bg-slate-50 p-3">
        <h3 class="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Hierarchy</h3>
        <div class="flex flex-wrap items-center gap-2 text-xs">
          <label class="flex items-center gap-1">
            <span class="text-slate-400">kind</span>
            <select
              class="rounded border border-slate-300 px-1.5 py-0.5 text-xs text-slate-700 disabled:opacity-50"
              value={draftKind}
              disabled={store.saving}
              aria-label="Kind"
              title="Kind"
              onchange={onKindChange}
            >
              {#each ITEM_KINDS as kind (kind.value)}
                <option value={kind.value}>{kind.label}</option>
              {/each}
            </select>
          </label>
          {#if draftKind !== 'milestone'}
            <label class="flex min-w-0 items-center gap-1">
              <span class="text-slate-400">parent</span>
              <Combobox
                options={parentOptions}
                bind:value={draftParentId}
                emptyOption="Top-level (no parent)"
                placeholder="Top-level (no parent)"
                ariaLabel="Parent"
                disabled={store.saving}
              />
            </label>
          {:else}
            <span class="text-[11px] text-slate-400">Milestones are top-level.</span>
          {/if}
          <button
            type="button"
            class="rounded-md bg-slate-900 px-2 py-0.5 text-xs font-medium text-white hover:bg-slate-700 disabled:opacity-50"
            disabled={store.saving}
            onclick={() => void saveHierarchy()}
          >
            Save
          </button>
        </div>
        {#if hierarchyError}
          <p class="mt-1 text-[11px] text-rose-700">{hierarchyError}</p>
        {/if}
      </section>

      <section class="mb-3 rounded-md border border-slate-200 bg-slate-50 p-3" data-labels>
        <h3 class="mb-2 text-xs font-semibold uppercase tracking-wide text-slate-400">Labels</h3>
        {#if store.labels.length === 0}
          <p class="text-[11px] text-slate-400">This workspace has no labels yet — type below to create one.</p>
        {/if}
        {#if liveItem.labels.length > 0}
          <div class="flex min-w-0 flex-wrap gap-1.5">
            {#each liveItem.labels as label (label.id)}
              <!-- PB-107: shared chip (clamp + a11y full name inside). -->
              <LabelChip {label} removable disabled={store.saving} onremove={() => void toggleLabel(label.id)} />
            {/each}
          </div>
        {/if}
        <div class="mt-2">
          <LabelSelect
            bind:value={pickerValue}
            search={pickerLabels.search}
            exactSearch={pickerLabels.exactSearch}
            create={pickerLabels.create}
            onpick={onPickerPick}
            showSelectedChips={false}
            disabled={store.saving}
            placeholder="Search or create labels…"
            ariaLabel="Add labels"
          />
        </div>
        {#if labelsError}
          <p class="mt-1 text-[11px] text-rose-700">{labelsError}</p>
        {/if}
      </section>

      {#if editing}
        <div class="mb-3">
          <label
            for="modal-description"
            class="mb-1 block text-xs font-semibold uppercase tracking-wide text-slate-400"
          >
            Description (markdown)
          </label>
          <textarea
            id="modal-description"
            class="w-full rounded-md border border-slate-300 px-2 py-1 text-sm text-slate-800"
            bind:value={draftDescription}
            rows="6"
            placeholder="Description (markdown)"
          ></textarea>
          <div class="mt-2 flex justify-end gap-2 text-xs">
            <button
              type="button"
              class="rounded-md border border-slate-300 px-2 py-0.5 text-slate-600 hover:bg-slate-100"
              onclick={cancelEdit}
            >
              Cancel
            </button>
            <button
              type="button"
              class="rounded-md bg-slate-900 px-3 py-0.5 font-medium text-white hover:bg-slate-700 disabled:opacity-50"
              disabled={store.saving || draftTitle.trim().length === 0}
              onclick={() => void saveEdit()}
            >
              Save
            </button>
          </div>
        </div>
      {:else if liveItem.description}
        <div class="markdown-body">{@html renderMarkdown(liveItem.description)}</div>
      {:else}
        <p class="text-sm italic text-slate-400">No description.</p>
      {/if}

      {#if showChildren}
        <section class="mt-5 border-t border-slate-200 pt-4" data-children>
          <div class="mb-3 flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-xs font-semibold uppercase tracking-wide text-slate-400">
              {children.length > 0 ? `Children (${children.length})` : 'Children'}
            </h3>
            {#if showProgress}
              <!--
                PB-88: header progress bar + "N of M closed (P%)". Values come
                exclusively from the item payload's closed_children/
                total_children (server truth, SSE-converged); archived children
                are excluded server-side and never re-computed here.
              -->
              <div
                class="flex items-center gap-2"
                data-progress
                title="% of direct non-archived children closed"
              >
                <div
                  role="progressbar"
                  aria-label="Child progress"
                  aria-valuemin={0}
                  aria-valuemax={100}
                  aria-valuenow={progress.percent}
                  class="h-1.5 w-32 overflow-hidden rounded-full bg-slate-200"
                >
                  <div
                    class="h-full rounded-full bg-emerald-500 transition-all"
                    style={`width: ${progress.percent}%`}
                  ></div>
                </div>
                <span class="text-[11px] font-medium text-slate-500">
                  {progress.closed} of {progress.total} closed ({progress.percent}%)
                </span>
              </div>
            {/if}
          </div>

          {#if children.length > 0}
            <ul class="flex flex-col gap-1.5">
              {#each children as child (child.id)}
                {@const col = store.columns.find((c) => c.id === child.column_id) ?? null}
                <li
                  data-child={child.id}
                  class="flex items-center justify-between gap-2 rounded-md border border-slate-200 bg-slate-50 px-2.5 py-1.5 text-sm"
                >
                  <button
                    type="button"
                    class="flex min-w-0 items-baseline gap-2 text-left hover:underline"
                    aria-label={`Open child ${child.key}`}
                    title={child.title}
                    onclick={() => onNavigate(child)}
                  >
                    <span class="shrink-0 font-mono text-xs text-slate-500" title="Item key">{child.key}</span>
                    <span class="truncate text-slate-700" class:line-through={child.closed}>{child.title}</span>
                  </button>
                  <span class="flex shrink-0 items-center gap-1.5">
                    {#if child.total_children > 0}
                      <!--
                        PB-88: per-child mini-bar for children that are
                        themselves parents (e.g. epics under a milestone).
                        Server fields again — child.closed_children/
                        total_children from the detail payload.
                      -->
                      <span
                        role="progressbar"
                        aria-label={`${child.key} child progress`}
                        aria-valuemin={0}
                        aria-valuemax={100}
                        aria-valuenow={Math.round((child.closed_children / child.total_children) * 100)}
                        title={`${child.closed_children} of ${child.total_children} children closed`}
                        class="h-1 w-12 overflow-hidden rounded-full bg-slate-200"
                      >
                        <span
                          class="block h-full rounded-full bg-emerald-500"
                          style={`width: ${Math.round((child.closed_children / child.total_children) * 100)}%`}
                        ></span>
                      </span>
                    {/if}
                    <span
                      class="rounded px-1.5 py-0.5 text-[10px] font-semibold uppercase {kindMeta(kindOf(child)).badgeClass}"
                    >{kindMeta(kindOf(child)).label}</span>
                    <span
                      class="rounded border px-1.5 py-0.5 text-[10px] font-medium {col ? statusPillClass(col.name) : 'border-slate-200 bg-white text-slate-400'}"
                      title={col ? `Status: column “${col.name}”` : 'Child on another board of this workspace'}
                    >{col ? col.name : 'other board'}</span>
                    {#if child.closed}
                      <span class="text-[10px] font-bold text-emerald-600" title="Closed">✓</span>
                    {/if}
                    {#if child.archived_at != null}
                      <span
                        class="rounded bg-amber-100 px-1.5 py-0.5 text-[10px] font-semibold uppercase tracking-wide text-amber-700"
                      >archived</span>
                    {/if}
                  </span>
                </li>
              {/each}
            </ul>
          {:else}
            <p class="text-sm text-slate-500">No child items yet.</p>
          {/if}
        </section>
      {/if}

      <section class="mt-5 border-t border-slate-200 pt-4">
        <h3 class="mb-3 text-xs font-semibold uppercase tracking-wide text-slate-400">Relations</h3>

        {#if relations.length > 0}
          <ul class="flex flex-col gap-1.5">
            {#each relations as relation (relation.id)}
              <li
                class="flex items-center justify-between gap-2 rounded-md border border-slate-200 bg-slate-50 px-2.5 py-1.5 text-sm"
              >
                <span class="min-w-0 truncate text-slate-700">
                  <span class="font-medium text-slate-500">{relationLabel(relation)}</span>
                  {relation.item.title}
                </span>
                <button
                  type="button"
                  class="shrink-0 rounded px-1.5 py-0.5 text-[11px] text-slate-500 hover:bg-slate-200 hover:text-rose-700"
                  title="Remove relation"
                  aria-label="Remove relation"
                  disabled={store.saving}
                  onclick={() => removeRelation(relation.id)}
                >
                  remove
                </button>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="text-sm text-slate-500">No relations yet.</p>
        {/if}

        <form onsubmit={submitRelation} class="mt-3 flex flex-wrap items-center gap-2">
          <select
            class="rounded border border-slate-300 px-2 py-1 text-xs text-slate-700"
            value={relationKind}
            aria-label="Relation type"
            title="Relation type"
            onchange={onRelationKindChange}
          >
            {#each RELATION_KINDS as kind (kind.value)}
              <option value={kind.value}>{kind.label}</option>
            {/each}
          </select>
          <Combobox
            options={relationTargetOptions}
            bind:value={relationTargetId}
            emptyOption="Select item…"
            placeholder="Select item…"
            ariaLabel="Related item"
          />
          <button
            type="submit"
            class="rounded-md bg-slate-900 px-3 py-1 text-xs font-medium text-white hover:bg-slate-700 disabled:opacity-50"
            disabled={store.saving || relationTargetId.length === 0}
          >
            Add
          </button>
        </form>
      </section>

      <section class="mt-5 border-t border-slate-200 pt-4">
        <h3 class="mb-3 text-xs font-semibold uppercase tracking-wide text-slate-400">Comments</h3>

        {#if loading}
          <p class="text-sm text-slate-500">Loading…</p>
        {:else if comments.length > 0}
          <ul class="flex flex-col gap-3">
            {#each comments as comment (comment.id)}
              <li class="rounded-md border border-slate-200 bg-slate-50 p-2.5">
                <div class="mb-1 flex items-center justify-between gap-2 text-[11px] text-slate-500">
                  <span class="font-medium text-slate-700">
                    {comment.author_name}
                    <span
                      class="ml-1 rounded px-1 py-0.5"
                      class:bg-violet-100={comment.author_type === 'agent'}
                      class:text-violet-700={comment.author_type === 'agent'}
                      class:bg-sky-100={comment.author_type !== 'agent'}
                      class:text-sky-700={comment.author_type !== 'agent'}
                    >
                      {comment.author_type}
                    </span>
                  </span>
                  <span title={comment.created_at}>{timeAgo(comment.created_at)}</span>
                </div>
                <div class="markdown-body">{@html renderMarkdown(comment.body)}</div>
              </li>
            {/each}
          </ul>
        {:else}
          <p class="text-sm text-slate-500">No comments yet.</p>
        {/if}

        <form onsubmit={submit} class="mt-4">
          {#if error}
            <p class="mb-2 text-xs text-rose-700">{error}</p>
          {/if}
          <textarea
            class="w-full rounded-md border border-slate-300 px-2 py-1 text-sm text-slate-800"
            bind:value={body}
            rows="3"
            placeholder="Add a comment as human…"
          ></textarea>
          <div class="mt-2 flex justify-end">
            <button
              type="submit"
              class="rounded-md bg-slate-900 px-3 py-1 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
              disabled={body.trim().length === 0}
            >
              Comment
            </button>
          </div>
        </form>
      </section>
    </div>
  </div>
</dialog>

<!--
  Markdown output is injected via {@html}, so its elements are not scoped by
  Svelte. The `.markdown-body` wrapper is scoped, and `:global()` descendants
  style the generated tags; Tailwind preflight zeroes list/heading styles, so
  they are restored here.
-->
<style>
  .markdown-body {
    font-size: 0.875rem;
    line-height: 1.5;
    color: #334155;
    overflow-wrap: anywhere;
  }
  .markdown-body :global(p) {
    margin: 0 0 0.5rem;
  }
  .markdown-body :global(h1),
  .markdown-body :global(h2),
  .markdown-body :global(h3) {
    font-weight: 600;
    color: #0f172a;
    margin: 0.75rem 0 0.4rem;
  }
  .markdown-body :global(h1) {
    font-size: 1.125rem;
  }
  .markdown-body :global(h2) {
    font-size: 1rem;
  }
  .markdown-body :global(h3) {
    font-size: 0.875rem;
  }
  .markdown-body :global(p:last-child),
  .markdown-body :global(ul:last-child),
  .markdown-body :global(ol:last-child),
  .markdown-body :global(pre:last-child) {
    margin-bottom: 0;
  }
  .markdown-body :global(ul),
  .markdown-body :global(ol) {
    margin: 0 0 0.5rem;
    padding-left: 1.25rem;
  }
  .markdown-body :global(ul) {
    list-style: disc;
  }
  .markdown-body :global(ol) {
    list-style: decimal;
  }
  .markdown-body :global(li) {
    margin: 0.15rem 0;
  }
  .markdown-body :global(code) {
    font-family: ui-monospace, SFMono-Regular, Menlo, monospace;
    font-size: 0.85em;
    background: #f1f5f9;
    border-radius: 0.25rem;
    padding: 0.05rem 0.3rem;
  }
  .markdown-body :global(pre) {
    background: #f1f5f9;
    border: 1px solid #e2e8f0;
    border-radius: 0.375rem;
    padding: 0.5rem 0.75rem;
    overflow-x: auto;
    margin: 0 0 0.5rem;
  }
  .markdown-body :global(pre code) {
    background: transparent;
    padding: 0;
    font-size: 0.8125rem;
  }
  .markdown-body :global(a) {
    color: #2563eb;
    text-decoration: underline;
  }
  .markdown-body :global(strong) {
    font-weight: 600;
  }
</style>
