// Sentinel value of the "All networks" entry in the client editor's ISP
// selector. It is never persisted: "no restriction" is stored as an empty list.
export const ISP_ALL_VALUE = 'all';

/**
 * Maps what the multi-select reports back onto the stored selection.
 *
 * The stored value never contains {@link ISP_ALL_VALUE} — no restriction is the
 * empty array — while the select *displays* `['all']` in that state. The
 * decision therefore has to be made against what was on screen:
 *
 * - displayed `['all']`, user picks `mci`  → next `['all','mci']` → `['mci']`
 * - displayed `['mci']`, user picks `all`  → next `['mci','all']` → `[]`
 *
 * Comparing against the stored array instead (which never holds `all`) made
 * every pick look like "the user chose All networks", so the selection snapped
 * back and no operator could be chosen.
 */
export function resolveIspSelection(current: string[], next: string[]): string[] {
  const displayedAll = current.length === 0;
  const pickedAll = next.includes(ISP_ALL_VALUE);
  if (pickedAll && !displayedAll) return [];
  return next.filter((id) => id !== ISP_ALL_VALUE);
}

/** What the select should show for a stored selection. */
export function ispSelectValue(current: string[] | undefined | null): string[] {
  return current && current.length > 0 ? current : [ISP_ALL_VALUE];
}
