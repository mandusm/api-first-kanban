<script lang="ts">
  import { untrack } from 'svelte'
  import { createComboboxBase } from './comboboxBase.svelte'

  /** One selectable row. `value` is always a stable id, never free-form text. */
  export interface ComboboxOption {
    value: string
    label: string
  }

  let {
    options,
    value = $bindable(''),
    placeholder = '',
    emptyOption = '',
    disabled = false,
    ariaLabel,
  }: {
    options: ComboboxOption[]
    value?: string
    placeholder?: string
    /** Non-empty label for an extra `value: ''` choice (e.g. "Top-level"). */
    emptyOption?: string
    disabled?: boolean
    ariaLabel: string
  } = $props()

  let input = $state<HTMLInputElement | null>(null)
  // The text the field shows. While the dropdown is open it doubles as the
  // filter query; it always snaps back to the committed option's label, so the
  // field never holds a free-form value.
  let text = $state('')

  const listId = $props.id()

  // `emptyOption` is modelled as just another option with `value: ''` so the
  // filter, keyboard navigation, and rendering have a single code path.
  const allOptions = $derived<ComboboxOption[]>(
    emptyOption.length > 0 ? [{ value: '', label: emptyOption }, ...options] : options
  )

  const selectedLabel = $derived(allOptions.find((o) => o.value === value)?.label ?? '')

  const query = $derived(text.trim().toLowerCase())

  // Case-insensitive substring filter on the label. An empty query lists every
  // option; a query equal to the committed label (e.g. on re-focus) also lists
  // everything, since it means "no filter" rather than a literal search.
  const filtered = $derived.by<ComboboxOption[]>(() => {
    if (query.length === 0 || query === selectedLabel.toLowerCase()) return allOptions
    return allOptions.filter((o) => o.label.toLowerCase().includes(query))
  })

  // Since PB-83 the open/active keynav state machine (focus/click/arrow
  // opening, ArrowUp/Down, Enter commit, first-Escape-swallowing close, Tab,
  // blur, and outside-pointerdown close) lives in `comboboxBase.svelte.ts`;
  // this component renders the options and reverts the visible text on close.
  const combobox = createComboboxBase({
    rowCount: () => filtered.length,
    onCommit: (index) => commit(filtered[index]),
    arrowOpenActive: (count, down) => (down ? (count > 0 ? 0 : -1) : count - 1),
    onClose: () => {
      text = selectedLabel
    },
  })

  // Keep the visible text aligned with the committed value. Only `value` is a
  // dependency (the option lookup is untracked), so a live `options` recompute
  // can never clobber what the user is typing.
  $effect(() => {
    value
    text = untrack(() => allOptions.find((o) => o.value === value)?.label ?? '')
  })

  function commit(option: ComboboxOption) {
    value = option.value
    text = option.label
    combobox.closeDropdown()
  }

  function onFocus() {
    combobox.openDropdown()
    // Select the text so typing immediately replaces the current label.
    input?.select()
  }

  function onInput(event: Event) {
    text = (event.currentTarget as HTMLInputElement).value
    combobox.open = true
    combobox.active = -1
  }
</script>

<svelte:window onpointerdown={combobox.onWindowPointerDown} />

<div bind:this={combobox.root} class="relative min-w-0 flex-1">
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
    class="w-full rounded border border-slate-300 px-2 py-1 text-xs text-slate-700 disabled:opacity-50"
    {placeholder}
    {disabled}
    value={text}
    onfocus={onFocus}
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
      class="absolute z-10 mt-1 max-h-56 w-full overflow-y-auto rounded-md border border-slate-300 bg-white py-1 text-xs shadow-lg"
    >
      {#if filtered.length === 0}
        <li class="px-2 py-1 text-slate-400">No matches</li>
      {:else}
        {#each filtered as option, i (option.value)}
          <!-- Keyboard navigation lives on the combobox input (aria-activedescendant),
               which is the ARIA combobox pattern; the option rows are pointer-only. -->
          <!-- svelte-ignore a11y_click_events_have_key_events -->
          <li
            id={`${listId}-${i}`}
            role="option"
            aria-selected={option.value === value}
            class="cursor-pointer px-2 py-1 text-slate-700"
            class:bg-slate-100={i === combobox.active}
            onmouseenter={() => (combobox.active = i)}
            onclick={() => commit(option)}
          >
            {option.label}
          </li>
        {/each}
      {/if}
    </ul>
  {/if}
</div>