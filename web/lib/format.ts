/** Formats a duration given in milliseconds with a unit suited to its size. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return "–";
  if (ms === 0) return "0 ms";
  if (ms < 1) return `${trim(ms * 1000, ms * 1000 < 10 ? 2 : 0)} µs`;
  if (ms < 1000) return `${trim(ms, ms < 10 ? 2 : ms < 100 ? 1 : 0)} ms`;
  if (ms < 60_000) return `${trim(ms / 1000, 2)} s`;
  const totalSeconds = Math.round(ms / 1000);
  const m = Math.floor(totalSeconds / 60);
  const s = totalSeconds % 60;
  return `${m}m ${s.toString().padStart(2, "0")}s`;
}

function trim(n: number, digits: number): string {
  return Number(n.toFixed(digits)).toString();
}

/** Formats a 0..1 ratio as a percentage. */
export function formatPercent(ratio: number): string {
  if (!Number.isFinite(ratio)) return "–";
  if (ratio === 0) return "0%";
  const pct = ratio * 100;
  if (pct < 0.1) return "<0.1%";
  return `${trim(pct, pct < 10 ? 1 : 0)}%`;
}

/** Formats an integer count with thousands separators. */
export function formatCount(n: number): string {
  return new Intl.NumberFormat("en-US").format(n);
}

/** Short form of a hex identifier for tables. */
export function shortId(id: string, n = 8): string {
  return id.slice(0, n);
}

/** "12s ago", "5m ago", "3h ago", or a date for anything older than a day. */
export function formatRelative(iso: string, now: Date = new Date()): string {
  const t = new Date(iso).getTime();
  const diff = Math.max(0, now.getTime() - t) / 1000;
  if (diff < 60) return `${Math.floor(diff)}s ago`;
  if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
  if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
  return new Date(iso).toISOString().slice(0, 10);
}

/** Wall-clock time with milliseconds, in UTC so server and client agree. */
export function formatClock(iso: string): string {
  return new Date(iso).toISOString().slice(11, 23) + " UTC";
}

/** Renders an attribute value for display. */
export function formatAttribute(v: unknown): string {
  if (v === null || v === undefined) return "null";
  if (typeof v === "string") return v;
  return JSON.stringify(v);
}
