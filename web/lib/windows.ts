export const WINDOWS = [
  { value: "15m", label: "Last 15 minutes" },
  { value: "1h", label: "Last hour" },
  { value: "6h", label: "Last 6 hours" },
  { value: "24h", label: "Last 24 hours" },
  { value: "7d", label: "Last 7 days" },
] as const;

export type WindowValue = (typeof WINDOWS)[number]["value"];

export const DEFAULT_WINDOW: WindowValue = "1h";

export function parseWindow(v: string | string[] | undefined): WindowValue {
  const s = Array.isArray(v) ? v[0] : v;
  return WINDOWS.some((w) => w.value === s) ? (s as WindowValue) : DEFAULT_WINDOW;
}

/** Time-series bucket size giving ~60 points per window. */
export function stepFor(w: WindowValue): string {
  return { "15m": "15s", "1h": "1m", "6h": "5m", "24h": "20m", "7d": "3h" }[w];
}

export function first(v: string | string[] | undefined): string {
  return (Array.isArray(v) ? v[0] : v) ?? "";
}
