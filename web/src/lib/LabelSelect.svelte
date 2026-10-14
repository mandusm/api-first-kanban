<!--
  PB-72: searchable label picker (shared component).

  Opens on focus showing a short default list (usage-ranked ?top=N via the
  caller-supplied `search` callback); typing searches (?q=, debounced). When
  the query matches no existing label, a "Create '<text>'" row appears
  (search-before-create: an exact match is SELECTED, never duplicated).

  The combobox plumbing (open/active keynav, outside-click, blur, listbox
  ARIA state) lives in `comboboxBase.svelte.ts` (PB-83) — this component only
  renders the list and owns the label-search/create specifics.

  Component API — designed for reuse by PB-69's create form:
  - `value` (bindable): selected label IDS (multi-select). Picking toggles
    membership; the component never persists — parents own that.
  - `search(q) => Promise<Label[]>`: option loader (typically the store's
    `searchLabelChoices`, which maps to GET /labels?top=N&q=).
  - `create(name) => Promise<Label | null>` (optional): creates a label when
    absent; returning a label treats it as picked. On null the component shows
    nothing itself — the caller surfaces the real error (e.g. duplicate name).
  - `exactSearch(name) => Promise<Label[]>` (optional): full (untruncated)
    lookup used to guard the create row — the default search may be windowed
    (?top=8), which can hide an exact match among many substring hits; without
    this guard the picker would offer "Create" for an EXISTING label.
  - `onpick(label, selected)` (optional): pick/change notification for parents
    that persist per-change (like the item modal's REPLACE-set flow).
  - `showSelectedChips` (default true): render the selected set as chips above
    the input. Parents that already show their own chip row (the item modal
    renders server-truth chips) set this off to avoid a duplicate row.

  PB-102: the selected set is visible — chips render ABOVE the input (with
  remove buttons). The component keeps a best-effort cache of labels seen via
  search results and picks so chips resolve for stateless parents (the create
  form) that have no server-side chip row of their own; the cache is a plain
  Map, NOT reactive — ids set into `value` externally without passing through
  pick/create may not resolve a chip. Chip markup lives in the shared
  LabelChip (PB-107: clamp inside; truncation is visual-only, so the full
  name stays readable without hover); the min-w-0 on chip rows
  is defence-in-depth, not the load-bearing clamp.
-->
<script lang="ts">
  import type { Label } from './types'
  import LabelChip from './LabelChip.svelte'
  import { createComboboxBase } from './comboboxBase.svelte'

  let {
    value = $bindable<string[]>([]),
    search,
    create = null,
    exactSearch = null,
    onpick = null,
    disabled = false,
    showSelectedChips = true,
    placeholder = 'Search labels…',
    ariaLabel = 'Labels',
    debounceMs = 300,
  }: {
    value?: string[]
    search: (q: string) => Promise<Label[]>
    create?: ((name: string) => Promise<Label | null>) | null
    exactSearch?: ((q: string) => Promise<Label[]>) | null
    onpick?: ((label: Label, selected: boolean) => void) | null
    disabled?: boolean
    showSelectedChips?: boolean
    placeholder?: string
    ariaLabel?: string
    debounceMs?: number
  } = $props()

  let input = $state<HTMLInputElement | null>(null)
  let text = $state('')
  let options = $state<Label[]>([])
  let loading = $state(false)
  let failed = $state(false)
  let busy = $state(false)
  let debounceTimer: ReturnType<typeof setTimeout> | null = null

  // PB-102: cache of labels seen (search results + picks) so selected ids can
  // render as chips even when the current option list doesn't include them.
  let known = new Map<string, Label>()
  const remember = (labels: Label[]) => {
    for (const label of labels) known.set(label.id, label)
  }

  const listId = $props.id()

  // Chips for the selected ids, in selection order; ids never seen (shouldn't
  // happen — value only changes via pick/create) are skipped, not guessed.
  const selectedLabels = $derived(
    value.map((id) => known.get(id)).filter((l): l is Label => l !== undefined),
  )

  const query = $derived(text.trim())
  // Search-before-create: an exact (case-insensitive) match among the results
  // is a selection, so the create row only appears when nothing matched.
  const exactMatch = $derived(
    query.length > 0 ? (options.find((l) => l.name.toLowerCase() === query.toLowerCase()) ?? null) : null,
  )
  // The windowed search (?top=8) can hide an exact match among many substring
  // hits, so before offering "Create" the query is re-checked UNWINDOWED via
  // `exactSearch`. The create row stays hidden until that check passes
  // (PB-83 hazard fix: an API failure is never treated as "no matching
  // labels" — a failed load never offers Create, and an unverified exact
  // check also blocks it).
  let checkedFor = $state<string | null>(null)
  let exactHit = $state<Label | null>(null)
  const exactReady = $derived(exactSearch === null || checkedFor === query)
  const baseCanCreate = $derived(
    query.length > 0 && exactMatch === null && create !== null && !failed,
  )
  const canCreate = $derived(baseCanCreate && exactReady && exactHit === null)
  // The create row is a virtual row after the label options.
  const rowCount = $derived(options.length + (canCreate ? 1 : 0))

  const combobox = createComboboxBase({
    rowCount: () => rowCount,
    onCommit: (index) => void commit(index),
    // LabelSelect opens the dropdown unhovered (matches the pre-PB-83 list).
    arrowOpenActive: () => -1,
    onOpen: () => void refresh(query),
  })

  let refreshSeq = 0

  async function refresh(q: string) {
    // Token guard: out-of-order responses must not clobber newer results.
    const seq = ++refreshSeq
    loading = true
    failed = false
    exactHit = null
    checkedFor = null
    try {
      const labels = await search(q)
      if (seq !== refreshSeq) return
      remember(labels)
      options = labels
      const windowedHit =
        q.length > 0 ? (labels.find((l) => l.name.toLowerCase() === q.toLowerCase()) ?? null) : null
      if (baseCanCreate && exactSearch && windowedHit === null) {
        // PB-83 hazard fix: the unwindowed exact check has been folded into
        // the search cycle, so it runs once per debounced search instead of
        // once per keystroke.
        try {
          const hit = (await exactSearch(q)).find((l) => l.name.toLowerCase() === q.toLowerCase()) ?? null
          if (seq !== refreshSeq) return
          checkedFor = q
          // Surface the exact match as a selectable row even though the
          // windowed search missed it.
          if (hit) {
            exactHit = hit
            if (!options.some((o) => o.id === hit.id)) options = [...options, hit]
          }
        } catch {
          if (seq === refreshSeq) {
            // The exact check itself failed (API outage): the results are
            // still shown, but Create is never offered while the guard is
            // unverified.
            checkedFor = null
          }
        }
      } else if (seq === refreshSeq) {
        checkedFor = q
      }
    } catch {
      if (seq === refreshSeq) {
        options = []
        failed = true
        checkedFor = null
      }
    } finally {
      if (seq === refreshSeq) loading = false
    }
  }

  function queueRefresh() {
    if (debounceTimer) clearTimeout(debounceTimer)
    debounceTimer = setTimeout(() => void refresh(query), debounceMs)
  }

  function onFocus() {
    combobox.openDropdown()
    input?.select()
  }

  // Click also opens (idempotent): a picker should open on pointer-down even
  // when the input is already focused, and it keeps the dropdown testable in
  // unfocused headless browsers where focus events never fire.
  function onClick() {
    if (combobox.open) return
    combobox.openDropdown()
  }

  function onInput(event: Event) {
    text = (event.currentTarget as HTMLInputElement).value
    // The debounced search below owns the refresh; opening must not trigger
    // an immediate fetch on top of it.
    combobox.open = true
    combobox.active = -1
    queueRefresh()
  }

  function pick(label: Label) {
    remember([label])
    const on = value.includes(label.id)
    value = on ? value.filter((id) => id !== label.id) : [...value, label.id]
    onpick?.(label, !on)
    // Reset the query so the next pick starts from the default top list.
    text = ''
    combobox.active = -1
    input?.focus()
    void refresh('')
  }

  async function doCreate() {
    const name = query
    if (name.length === 0 || !create || busy) return
    busy = true
    try {
      const label = await create(name)
      if (label) {
        remember([label])
        if (!value.includes(label.id)) {
          value = [...value, label.id]
          onpick?.(label, true)
        }
        text = ''
        combobox.active = -1
        input?.focus()
        void refresh('')
      }
      // On null the caller surfaces the real error (duplicate/invalid name);
      // the generic message here is reserved for load failures.
    } finally {
      busy = false
    }
  }

  async function commit(index: number) {
    if (index < options.length) pick(options[index])
    else if (canCreate) await doCreate()
  }
</script>

<svelte:window onpointerdown={combobox.onWindowPointerDown} />

<div bind:this={combobox.root} class="relative min-w-0" data-label-picker>
  {#if showSelectedChips && selectedLabels.length > 0}
    <!-- PB-102: visible selected set (remove by clicking the chip's ×). -->
    <div class="mb-1 flex min-w-0 flex-wrap gap-1" data-label-picker-selected>
      {#each selectedLabels as label (label.id)}
        <!-- PB-107: shared chip (clamp + a11y full name inside). -->
        <LabelChip {label} removable {disabled} onremove={() => pick(label)} />
      {/each}
    </div>
  {/if}
  <input
    bind:this={input}
    type="text"
    role="combobox"
    aria-label={ariaLabel}
    aria-autocomplete="list"
    aria-expanded={combobox.open}
    aria-controls={listId}
    aria-activedescendant={combobox.activeDescendant(listId)}
    autocomplete="off"
    class="w-52 rounded border border-slate-300 px-2 py-0.5 text-xs text-slate-700 disabled:opacity-50"
    {placeholder}
    {disabled}
    data-label-picker-input
    value={text}
    onfocus={onFocus}
    onclick={onClick}
    oninput={onInput}
    onblur={combobox.onBlur}
    onkeydown={combobox.onKeydown}
  />

  {#if combobox.open}
    <ul
      id={listId}
      role="listbox"
      aria-label={ariaLabel}
      onmousedown={(event) => event.preventDefault()}
      class="absolute z-10 mt-1 max-h-56 w-64 overflow-y-auto rounded-md border border-slate-300 bg-white py-1 text-xs shadow-lg"
    >
      {#if loading && options.length === 0}
        <li class="px-2 py-1 text-slate-400">Loading…</li>
      {:else if failed}
        <li class="px-2 py-1 text-rose-700">Could not load labels.</li>
      {:else}
        {#each options as label, i (label.id)}
          <!-- Keyboard navigation lives on the combobox input (aria-activedescendant);
               option rows are pointer-only, matching the Combobox pattern. -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id={`${listId}-${i}`}
            role="option"
            aria-selected={value.includes(label.id)}
            class="flex cursor-pointer items-center justify-between gap-2 px-2 py-1"
            class:bg-slate-100={i === combobox.active}
            onmouseenter={() => (combobox.active = i)}
            onclick={() => pick(label)}
          >
            <!-- PB-107: shared chip (clamp inside). -->
            <LabelChip {label} />
            {#if label.usage_count != null}
              <span class="shrink-0 text-[10px] text-slate-400" title="Items using this label">
                {label.usage_count}
              </span>
            {/if}
          </li>
        {/each}
        {#if options.length === 0 && !canCreate}
          <li class="px-2 py-1 text-slate-400">No matching labels</li>
        {/if}
        {#if canCreate}
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id={`${listId}-${options.length}`}
            role="option"
            aria-selected={false}
            data-label-create
            class="cursor-pointer px-2 py-1 font-medium text-slate-700"
            class:bg-slate-100={combobox.active === options.length}
            onmouseenter={() => (combobox.active = options.length)}
            onclick={() => void doCreate()}
          >
            Create “{query}”
          </li>
        {/if}
      {/if}
    </ul>
  {/if}
</div>