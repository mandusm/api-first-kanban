// Small display helpers shared by the board components.

import type { ItemKind, Relation, RelationKind } from './types'

/** Compact relative time: "just now", "4m", "3h", "2d", else a date. */
export function timeAgo(iso: string | null | undefined): string {
  if (!iso) return ''
  const then = new Date(iso).getTime()
  if (Number.isNaN(then)) return ''
  const seconds = Math.max(0, Math.round((Date.now() - then) / 1000))
  if (seconds < 45) return 'just now'
  const minutes = Math.round(seconds / 60)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 7) return `${days}d ago`
  return new Date(iso).toLocaleDateString()
}

/** Fixed 8-hue palette used for on-the-fly label creation. */
export const LABEL_PALETTE = [
  '#dc2626', '#ea580c', '#ca8a04', '#16a34a',
  '#0891b2', '#2563eb', '#7c3aed', '#db2777',
] as const

function parseHex(hex: string): [number, number, number] | null {
  const m = /^#([0-9a-f]{6})$/i.exec(hex)
  if (!m) return null
  const n = parseInt(m[1], 16)
  return [(n >> 16) & 0xff, (n >> 8) & 0xff, n & 0xff]
}

function channelLum(c: number): number {
  const s = c / 255
  return s <= 0.04045 ? s / 12.92 : ((s + 0.055) / 1.055) ** 2.4
}

function relLuminance([r, g, b]: [number, number, number]): number {
  return 0.2126 * channelLum(r) + 0.7152 * channelLum(g) + 0.0722 * channelLum(b)
}

function contrast(a: [number, number, number], b: [number, number, number]): number {
  const l1 = relLuminance(a)
  const l2 = relLuminance(b)
  const [hi, lo] = l1 >= l2 ? [l1, l2] : [l2, l1]
  return (hi + 0.05) / (lo + 0.05)
}

const mix = (
  a: [number, number, number],
  b: [number, number, number],
  t: number,
): [number, number, number] => [
  Math.round(a[0] + (b[0] - a[0]) * t),
  Math.round(a[1] + (b[1] - a[1]) * t),
  Math.round(a[2] + (b[2] - a[2]) * t),
]

const WHITE: [number, number, number] = [255, 255, 255]
const BLACK: [number, number, number] = [0, 0, 0]

/**
 * Inline style for a label pill. Label colors come from the API, so they
 * cannot be Tailwind classes; a translucent background keeps the chip tinted.
 *
 * The text color is darkened from the label hue until it reaches WCAG AA
 * (4.5:1) against the tinted background — roughly half the palette hues fail
 * at full strength (worst #ca8a04 ≈2.5:1). The border keeps the original hue
 * so chips stay visually distinct.
 */
export function labelStyle(color: string): string {
  const rgb = parseHex(color)
  if (!rgb) return `border-color:${color};color:${color};background:transparent`
  // The chip background is the hue blended 0x1f/0xff (~12%) onto white.
  const tint = mix(WHITE, rgb, 0x1f / 0xff)
  let text = rgb
  for (let t = 0; t <= 1 && contrast(text, tint) < 4.5; t += 0.05) {
    text = mix(rgb, BLACK, t)
  }
  return `border-color:${color};color:#${text.map((c) => c.toString(16).padStart(2, '0')).join('')};background:${color}1f`
}

/**
 * Collision-aware palette pick for a new label: prefer the first palette hue
 * no existing label uses; fall back to round-robin once all 8 are taken
 * (e.g. past 8 labels, or after deletes freed earlier hues).
 */
export function pickLabelColor(existingColors: string[]): string {
  const used = new Set(existingColors.map((c) => c.toLowerCase()))
  return LABEL_PALETTE.find((c) => !used.has(c.toLowerCase())) ?? LABEL_PALETTE[existingColors.length % LABEL_PALETTE.length]
}

/**
 * Display metadata for the item priority enum (0..3). The Tailwind classes are
 * spelled out in full here because the JIT compiler only sees literal class
 * strings — building them from the numeric value would purge them.
 */
export interface PriorityMeta {
  value: number
  /** Short badge text, e.g. "P0". */
  label: string
  /** Readable name, e.g. "Blocker". */
  name: string
  /** Badge classes: P0 red through P3 gray. */
  badgeClass: string
}

export const PRIORITIES: readonly PriorityMeta[] = [
  { value: 0, label: 'P0', name: 'Blocker', badgeClass: 'border-red-200 bg-red-100 text-red-700' },
  { value: 1, label: 'P1', name: 'High', badgeClass: 'border-amber-200 bg-amber-100 text-amber-700' },
  { value: 2, label: 'P2', name: 'Normal', badgeClass: 'border-slate-200 bg-slate-100 text-slate-600' },
  { value: 3, label: 'P3', name: 'Low', badgeClass: 'border-gray-200 bg-gray-100 text-gray-500' },
]

/** Metadata for a priority value; unknown values fall back to P2 (the default). */
export function priorityMeta(value: number): PriorityMeta {
  return PRIORITIES.find((p) => p.value === value) ?? PRIORITIES[2]
}

/**
 * Kind of an item, tolerant of a missing field on a non-conforming payload.
 * The API default is `task`, so an absent kind renders as a task.
 */
export function kindOf(item: { kind?: ItemKind }): ItemKind {
  return item.kind ?? 'task'
}

/** Display metadata for an item kind (label, badge classes, glyph). */
export interface KindMeta {
  value: ItemKind
  /** Readable name, e.g. "Milestone". */
  label: string
  /** Badge classes — spelled out for the Tailwind JIT. */
  badgeClass: string
  /** Small glyph used in compact tags. */
  icon: string
}

export const ITEM_KINDS: readonly KindMeta[] = [
  { value: 'milestone', label: 'Milestone', badgeClass: 'bg-indigo-100 text-indigo-700', icon: '⌂' },
  { value: 'epic', label: 'Epic', badgeClass: 'bg-violet-100 text-violet-700', icon: '◇' },
  { value: 'task', label: 'Task', badgeClass: 'bg-slate-100 text-slate-600', icon: '·' },
]

/** Metadata for a kind; unknown or missing values fall back to `task`. */
export function kindMeta(kind: ItemKind | undefined): KindMeta {
  return ITEM_KINDS.find((k) => k.value === kind) ?? ITEM_KINDS[2]
}

/**
 * Kinds allowed as the parent of an item of `kind`, per the API rule:
 * milestone (no parent) ← epic ← task. An empty result means no kind is an
 * eligible parent; an item with no parent (top-level) is valid for every kind.
 */
export function parentKindsFor(kind: ItemKind): readonly ItemKind[] {
  if (kind === 'task') return ['milestone', 'epic']
  if (kind === 'epic') return ['milestone']
  return []
}

/**
 * Badge classes for a child-status pill keyed by COLUMN NAME. Columns carry
 * no color from the API, so a conservative name heuristic supplies one:
 * done/complete/finished → emerald, backlog/todo → slate, anything else
 * (in progress, review, …) → sky. The classes are spelled out in full for
 * the Tailwind JIT (see PRIORITIES); this is cosmetic only — unknown names
 * fall back to sky.
 */
export function statusPillClass(columnName: string): string {
  const name = columnName.toLowerCase()
  if (/(done|complete|finish|ship)/.test(name))
    return 'border-emerald-200 bg-emerald-50 text-emerald-700'
  if (/(backlog|todo|to do|later|icebox)/.test(name))
    return 'border-slate-200 bg-slate-100 text-slate-600'
  return 'border-sky-200 bg-sky-50 text-sky-700'
}

/**
 * Resolve the `parent_item_id` to send for a hierarchy write: a milestone can
 * never have a parent, and an empty selection means top-level (null). Shared
 * by the create and edit call sites so the rule lives in one place.
 */
export function resolveParentId(kind: ItemKind, parentId: string): string | null {
  if (kind === 'milestone') return null
  return parentId.length > 0 ? parentId : null
}

/** Selectable relation types, in the order offered in the detail modal. */
export const RELATION_KINDS: readonly { value: RelationKind; label: string }[] = [
  { value: 'blocks', label: 'blocks' },
  { value: 'relates_to', label: 'relates to' },
  { value: 'duplicates', label: 'duplicates' },
]

/**
 * Human phrase for a relation edge, phrased from the owning item's point of
 * view. `blocks` is asymmetric ("Blocks" / "Blocked by"); the other two are
 * symmetric, though `duplicates` reads as passive when incoming.
 */
export function relationLabel(edge: Relation): string {
  if (edge.direction === 'incoming') {
    if (edge.relation === 'blocks') return 'Blocked by'
    if (edge.relation === 'duplicates') return 'Duplicated by'
    return 'Related to'
  }
  if (edge.relation === 'blocks') return 'Blocks'
  if (edge.relation === 'duplicates') return 'Duplicates'
  return 'Related to'
}
