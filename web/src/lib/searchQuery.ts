/**
 * Single owner for "blank query" normalization shared by the board store and
 * the board view state (PB-65 / PB-63 P1-2 + P2-2).
 *
 * Blank follows the backend spec (docs/search-query.md §1.1): only ASCII
 * whitespace (space, tab, CR, LF) is trimmed, so a whitespace-only query is
 * indistinguishable from no search. NBSP or other Unicode whitespace is NOT
 * blank — the server deliberately treats it as a parse error ("unexpected
 * input"), and this check preserves that edge instead of JS `trim()`'s
 * Unicode semantics.
 */
export function isBlankQuery(q: string): boolean {
  return !/[^ \t\n\r]/.test(q)
}

/** Normalize a raw search-box value: blank (per §1.1) becomes '' (no search). */
export function normalizeSearchQuery(q: string): string {
  return isBlankQuery(q) ? '' : q
}
