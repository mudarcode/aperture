import { CircleAlert, CircleCheck } from "lucide-react";
import { cn } from "@/lib/cn";
import { formatPercent } from "@/lib/format";

/** Error rate shown with icon + text, never color alone. */
export function ErrorRate({ rate, count }: { rate: number; count?: number }) {
  if (rate === 0) {
    return <span className="text-muted tabular">0%</span>;
  }
  const severe = rate >= 0.05;
  return (
    <span className={cn("inline-flex items-center gap-1 tabular", severe ? "text-critical-text font-medium" : "text-text")}>
      {severe ? <CircleAlert aria-hidden className="size-3.5" /> : null}
      {formatPercent(rate)}
      {count !== undefined ? <span className="text-muted font-normal">({count})</span> : null}
    </span>
  );
}

export function StatusBadge({ status }: { status: "unset" | "ok" | "error" }) {
  if (status === "error") {
    return (
      <span className="inline-flex items-center gap-1 rounded bg-critical-soft px-1.5 py-0.5 text-xs font-medium text-critical-text">
        <CircleAlert aria-hidden className="size-3" /> Error
      </span>
    );
  }
  return (
    <span className="inline-flex items-center gap-1 rounded bg-surface-2 px-1.5 py-0.5 text-xs text-muted">
      <CircleCheck aria-hidden className="size-3" /> {status === "ok" ? "OK" : "Unset"}
    </span>
  );
}

export function ServiceChip({ name, color }: { name: string; color: string }) {
  return (
    <span className="inline-flex items-center gap-1.5 text-xs whitespace-nowrap">
      <span aria-hidden className="size-2.5 shrink-0 rounded-sm" style={{ background: color }} />
      {name}
    </span>
  );
}
