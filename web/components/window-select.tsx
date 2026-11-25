"use client";

import { usePathname, useRouter, useSearchParams } from "next/navigation";
import { useTransition } from "react";
import { RefreshCw } from "lucide-react";
import { WINDOWS, parseWindow } from "@/lib/windows";
import { cn } from "@/lib/cn";

/** Time-window picker and refresh button; state lives in the ?window= URL param. */
export function WindowSelect() {
  const router = useRouter();
  const pathname = usePathname();
  const params = useSearchParams();
  const [pending, startTransition] = useTransition();
  const value = parseWindow(params.get("window") ?? undefined);

  return (
    <div className="flex items-center gap-2">
      <label className="sr-only" htmlFor="window-select">
        Time range
      </label>
      <select
        id="window-select"
        value={value}
        onChange={(e) => {
          const next = new URLSearchParams(params);
          next.set("window", e.target.value);
          next.delete("cursor");
          startTransition(() => router.push(`${pathname}?${next.toString()}`));
        }}
        className="h-8 rounded-md border border-border bg-surface px-2 text-sm"
      >
        {WINDOWS.map((w) => (
          <option key={w.value} value={w.value}>
            {w.label}
          </option>
        ))}
      </select>
      <button
        type="button"
        onClick={() => startTransition(() => router.refresh())}
        className="inline-flex h-8 items-center gap-1.5 rounded-md border border-border bg-surface px-2.5 text-sm hover:bg-surface-2"
      >
        <RefreshCw aria-hidden className={cn("size-3.5", pending && "animate-spin")} />
        Refresh
      </button>
    </div>
  );
}
