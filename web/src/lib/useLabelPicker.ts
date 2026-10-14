/**
 * PB-83: the single label-picker wiring shared by every LabelSelect call site
 * (the column create form and the item modal's label section). Previously
 * searchLabels / exactSearchLabels / createPickerLabel (+ the top=8 window
 * and the collision-aware palette pick) were duplicated verbatim in both
 * components with divergent create-error surfacing.
 *
 * The store is read through a getter at call time, so the wiring stays
 * reactive to the caller's props (and `state_referenced_locally`-clean).
 *
 * Search/exactSearch remain thin wrappers over the store so the "unwindowed
 * exact-match guard" contract stays grep-verifiable in one place. Create
 * failures are surfaced through a hook — the modal reports them inline next
 * to the picker; the column form does the same (plus the store's global
 * actionError banner).
 */
import { pickLabelColor } from './format'
import type { KanbanStore } from './kanban.svelte'
import type { Label } from './types'

/** Usage-ranked window for the default picker list (GET /labels?top=N). */
export const PICKER_TOP = 8

export interface LabelPickerWiring {
  /** Windowed, usage-ranked choices for the picker's default/searched list. */
  search: (q: string) => Promise<Label[]>
  /** Unwindowed exact-name lookup backing the create-row guard. */
  exactSearch: (q: string) => Promise<Label[]>
  /**
   * Creates a label with a collision-aware palette pick. Returns null on
   * failure — the server's real rejection (duplicate/invalid name) is
   * delivered through `onCreateError` instead, so both call sites surface
   * errors the same way.
   */
  create: (name: string) => Promise<Label | null>
}

export function useLabelPicker(
  getStore: () => KanbanStore,
  hooks: { onCreateError: (message: string) => void },
): LabelPickerWiring {
  return {
    search: (q) => getStore().searchLabelChoices(q, PICKER_TOP),
    exactSearch: (q) => getStore().searchLabelChoices(q),
    create: async (name) => {
      const store = getStore()
      const label = await store.createLabel(name, pickLabelColor(store.labels.map((l) => l.color)))
      // Surface the server's real rejection (duplicate/invalid) in the call
      // site's inline error instead of the picker's generic load-failure
      // message. `createLabel` also sets store.actionError, which App renders
      // globally — same surfacing path as every other store mutation.
      if (!label) hooks.onCreateError(store.actionError ?? `Could not create label “${name}”.`)
      return label
    },
  }
}