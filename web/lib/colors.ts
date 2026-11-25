/**
 * Service identity colors. Each service maps to one of seven categorical
 * slots (CSS variables --series-1..7, validated for color-vision deficiency in
 * light and dark mode). The mapping is a stable hash of the service name so a
 * service keeps its color on every page; service names are always shown next
 * to the color, so a hash collision never makes identity ambiguous.
 */
// Slot 8 (red) is left out: red is reserved for errors in every view.
export const SLOT_COUNT = 7;

export function serviceSlot(name: string): number {
  // FNV-1a, 32-bit.
  let h = 0x811c9dc5;
  for (let i = 0; i < name.length; i++) {
    h ^= name.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return ((h >>> 0) % SLOT_COUNT) + 1;
}

export function serviceColor(name: string): string {
  return `var(--series-${serviceSlot(name)})`;
}

/**
 * Assigns distinct slots to a set of services: each takes its hashed slot,
 * or the next free one on collision (names processed alphabetically so the
 * result does not depend on input order). With more than seven services the
 * extra ones share slots, and the always-visible name labels disambiguate.
 */
export function assignServiceColors(services: Iterable<string>): Map<string, string> {
  const names = [...new Set(services)].sort();
  const taken = new Set<number>();
  const out = new Map<string, string>();
  for (const name of names) {
    let slot = serviceSlot(name);
    if (taken.size < SLOT_COUNT) {
      while (taken.has(slot)) slot = (slot % SLOT_COUNT) + 1;
    }
    taken.add(slot);
    out.set(name, `var(--series-${slot})`);
  }
  return out;
}
