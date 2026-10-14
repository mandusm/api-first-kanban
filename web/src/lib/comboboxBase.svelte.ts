/**
 * PB-83: headless combobox plumbing shared by every dropdown-style picker
 * (currently Combobox and LabelSelect). This owns the open/active state
 * machine that used to be duplicated ~verbatim in both components:
 *
 *   focus/click/arrow opening, ArrowUp/ArrowDown movement, Enter commit
 *   (prevented from reaching enclosing forms), first-Escape-swallowing close
 *   (so a modal's dialog only closes on the second press), Tab close, blur
 *   close, and outside-pointerdown close.
 *
 * Rendering stays in the components — only behavior is unified here. Callers
 * wire it into the template with `bind:this` on the wrapper element and
 * forward `onkeydown`/`onblur` from the combobox input, plus
 * `onpointerdown` from `<svelte:window>`.
 */
export interface ComboboxBaseOptions {
  /** Total selectable rows (options plus any virtual row, e.g. a create row). */
  rowCount: () => number
  /** Enter commit for row `index` (0-based; `active`, or the first row). */
  onCommit: (index: number) => void
  /**
   * Active index when the dropdown opens via an arrow key. Combobox lands on
   * the first (ArrowDown) or last (ArrowUp) row; LabelSelect opens unhovered.
   */
  arrowOpenActive: (count: number, down: boolean) => number
  /** Runs whenever the dropdown opens (e.g. refresh the default option list). */
  onOpen?: () => void
  /** Runs whenever it closes (e.g. Combobox reverts the visible text). */
  onClose?: () => void
}

export function createComboboxBase(opts: ComboboxBaseOptions) {
  let open = $state(false)
  let active = $state(-1)
  // Wrapper element, assigned via `bind:this={base.root}` by the component.
  let root = $state<HTMLElement | null>(null)

  function openDropdown() {
    open = true
    active = -1
    opts.onOpen?.()
  }

  function openViaArrow(down: boolean) {
    open = true
    active = opts.arrowOpenActive(opts.rowCount(), down)
    opts.onOpen?.()
  }

  function closeDropdown() {
    open = false
    active = -1
    opts.onClose?.()
  }

  function onBlur() {
    // Like the option rows' mousedown-preventDefault: a click commits before
    // the blur fires, so a real blur simply means "close".
    closeDropdown()
  }

  function onKeydown(event: KeyboardEvent) {
    if (event.key === 'ArrowDown') {
      event.preventDefault()
      if (!open) {
        openViaArrow(true)
        return
      }
      active = Math.min(active + 1, opts.rowCount() - 1)
    } else if (event.key === 'ArrowUp') {
      event.preventDefault()
      if (!open) {
        openViaArrow(false)
        return
      }
      active = Math.max(active - 1, 0)
    } else if (event.key === 'Enter') {
      // Selection, never form submit (enclosing forms own the mutations).
      event.preventDefault()
      const count = opts.rowCount()
      if (open && count > 0) opts.onCommit(active >= 0 ? active : 0)
    } else if (event.key === 'Escape') {
      if (open) {
        // The first Escape closes only the dropdown; the next reaches the
        // native <dialog> so it can close the enclosing modal.
        event.preventDefault()
        event.stopPropagation()
        closeDropdown()
      }
    } else if (event.key === 'Tab') {
      closeDropdown()
    }
  }

  function onWindowPointerDown(event: PointerEvent) {
    if (!open) return
    if (root && !root.contains(event.target as Node)) closeDropdown()
  }

  return {
    get open() {
      return open
    },
    set open(v: boolean) {
      open = v
    },
    get active() {
      return active
    },
    set active(v: number) {
      active = v
    },
    get root() {
      return root
    },
    set root(v: HTMLElement | null) {
      root = v
    },
    openDropdown,
    closeDropdown,
    onKeydown,
    onBlur,
    onWindowPointerDown,
    /** aria-activedescendant for the input while the dropdown is open. */
    activeDescendant: (listId: string) => (open && active >= 0 ? `${listId}-${active}` : undefined),
  }
}