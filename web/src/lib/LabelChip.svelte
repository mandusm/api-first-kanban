<!--
  PB-107: shared label chip (extracted from the duplicated markup in the item
  modal and the picker's selected row, and from the Card label row).

  Owns the overflow clamp so every chip site is covered: `inline-block
  max-w-full truncate` keeps long label names inside their container (PB-101 —
  `inline-block` is load-bearing: max-width/overflow/ellipsis do nothing on a
  plain inline box). Truncation is VISUAL only, so screen readers always get
  the complete name from the chip's text content — no hidden duplicate needed
  (PB-107 round 2, F2: a sr-only copy of the name made SRs announce it twice).

  A11y by branch:
  - Static chip (cards, filter bar, dropdown options): accessible name is the
    label's text content (full name, truncation being visual-only). NOT
    focusable — the card chips are non-interactive; hover gets the `title`.
  - Removable chip (modal, picker selected row): a button whose `aria-label`
    ("Remove label “X”") supplies the complete accessible name and overrides
    the content, so no sr-only span is needed; ✕ is aria-hidden decoration.

  Both branches carry `data-label-chip={label.name}` — a stable e2e hook that
  survives internal chip markup changes.

  Props:
  - `label`: the label to render (name + color drive the chip).
  - `removable`: render as a button with a ✕ affordance (aria-pressed=true,
    remove onclick).
  - `onremove(label)`: remove callback for `removable` chips.
  - `disabled`: disables the remove button.
-->
<script lang="ts">
  import { labelStyle } from './format'
  import type { Label } from './types'

  let {
    label,
    removable = false,
    onremove = null,
    disabled = false,
  }: {
    label: Label
    removable?: boolean
    onremove?: ((label: Label) => void) | null
    disabled?: boolean
  } = $props()
</script>

{#if removable}
  <button
    type="button"
    class="inline-block max-w-full truncate rounded-full border px-2 py-0.5 text-[10px] font-medium disabled:cursor-not-allowed disabled:opacity-50"
    style={labelStyle(label.color)}
    aria-pressed="true"
    aria-label={`Remove label “${label.name}”`}
    title={`Remove label “${label.name}”`}
    data-label-chip={label.name}
    {disabled}
    onclick={() => onremove?.(label)}
  >
    {label.name}
    <span aria-hidden="true" class="text-[9px]">✕</span>
  </button>
{:else}
  <span
    class="inline-block max-w-full truncate rounded-full border px-2 py-0.5 text-[10px] font-medium"
    style={labelStyle(label.color)}
    title={label.name}
    data-label-chip={label.name}
  >
    {label.name}
  </span>
{/if}
