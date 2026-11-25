"use client";

import { useRouter } from "next/navigation";
import { CartesianGrid, Legend, ResponsiveContainer, Scatter, ScatterChart, Tooltip, XAxis, YAxis, ZAxis } from "recharts";
import type { TraceSummary } from "@/lib/types";
import { formatDuration } from "@/lib/format";

const axisTick = { fill: "var(--text-muted)", fontSize: 11 };

interface Point {
  t: number;
  d: number;
  id: string;
  label: string;
  errors: number;
  spans: number;
}

/** Trace duration over time; failed traces are a separate series. Click a point to open it. */
export function TraceScatter({ traces }: { traces: TraceSummary[] }) {
  const router = useRouter();
  const toPoint = (t: TraceSummary): Point => ({
    t: new Date(t.startTime).getTime(),
    d: t.durationMs,
    id: t.traceId,
    label: `${t.rootService ?? "?"} · ${t.rootName ?? "(root span missing)"}`,
    errors: t.errorCount,
    spans: t.spanCount,
  });
  const ok = traces.filter((t) => t.errorCount === 0).map(toPoint);
  const failed = traces.filter((t) => t.errorCount > 0).map(toPoint);
  const open = (p: unknown) => {
    const id = (p as { payload?: Point }).payload?.id;
    if (id) router.push(`/traces/${id}`);
  };

  return (
    <div className="h-56">
      <ResponsiveContainer width="100%" height="100%">
        <ScatterChart margin={{ top: 8, right: 16, bottom: 0, left: -4 }}>
          <CartesianGrid stroke="var(--grid)" />
          <XAxis
            type="number"
            dataKey="t"
            domain={["dataMin", "dataMax"]}
            scale="time"
            tickFormatter={(v: number) => new Date(v).toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit", second: "2-digit" })}
            tick={axisTick}
            stroke="var(--axis)"
            name="Start"
            minTickGap={50}
          />
          <YAxis type="number" dataKey="d" tickFormatter={(v: number) => formatDuration(v)} tick={axisTick} stroke="var(--axis)" width={68} name="Duration" />
          <ZAxis range={[48, 48]} />
          <Tooltip
            cursor={{ stroke: "var(--axis)" }}
            content={({ active, payload }) => {
              const p = active ? (payload?.[0]?.payload as Point | undefined) : undefined;
              if (!p) return null;
              return (
                <div className="rounded-md border border-border bg-surface px-3 py-2 text-xs shadow-sm">
                  <p className="font-medium">{p.label}</p>
                  <p className="text-muted">{new Date(p.t).toLocaleString()}</p>
                  <p className="tabular mt-1">
                    {formatDuration(p.d)} · {p.spans} spans{p.errors ? ` · ${p.errors} errors` : ""}
                  </p>
                  <p className="mt-1 text-muted">Click to open</p>
                </div>
              );
            }}
          />
          <Legend wrapperStyle={{ fontSize: 11, color: "var(--text-muted)" }} />
          <Scatter name="Succeeded" data={ok} fill="var(--series-1)" stroke="var(--surface)" strokeWidth={2} onClick={open} cursor="pointer" isAnimationActive={false} />
          <Scatter
            name="Contains errors"
            data={failed}
            fill="var(--critical)"
            stroke="var(--surface)"
            strokeWidth={2}
            shape="diamond"
            onClick={open}
            cursor="pointer"
            isAnimationActive={false}
          />
        </ScatterChart>
      </ResponsiveContainer>
    </div>
  );
}
