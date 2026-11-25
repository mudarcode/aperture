"use client";

import { Bar, BarChart, CartesianGrid, Legend, Line, LineChart, ResponsiveContainer, Tooltip, XAxis, YAxis } from "recharts";
import type { Bucket } from "@/lib/types";
import { formatCount, formatDuration } from "@/lib/format";

const axisTick = { fill: "var(--text-muted)", fontSize: 11 };

function timeLabel(iso: string, long: boolean) {
  const d = new Date(iso);
  return long
    ? d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" })
    : d.toLocaleTimeString(undefined, { hour: "2-digit", minute: "2-digit" });
}

function ChartTooltip({
  active,
  payload,
  label,
  format,
}: {
  active?: boolean;
  payload?: { name: string; value: number; color: string }[];
  label?: string;
  format: (v: number) => string;
}) {
  if (!active || !payload?.length || !label) return null;
  return (
    <div className="rounded-md border border-border bg-surface px-3 py-2 text-xs shadow-sm">
      <p className="mb-1 text-muted">{new Date(label).toLocaleString()}</p>
      {payload.map((p) => (
        <p key={p.name} className="flex items-center gap-2">
          <span aria-hidden className="size-2 rounded-sm" style={{ background: p.color }} />
          <span>{p.name}</span>
          <span className="tabular ml-auto pl-3 font-medium">{format(p.value)}</span>
        </p>
      ))}
    </div>
  );
}

export function ServiceCharts({ buckets, step }: { buckets: Bucket[]; step: string }) {
  const long = buckets.length > 1 && new Date(buckets.at(-1)!.time).getTime() - new Date(buckets[0]!.time).getTime() > 86_400_000;
  const data = buckets.map((b) => ({ ...b, ok: b.count - b.errorCount }));
  const tick = (v: string) => timeLabel(v, long);

  return (
    <div className="grid gap-4 lg:grid-cols-3">
      <Panel title="Requests" subtitle={`per ${step}`}>
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -12 }}>
          <CartesianGrid vertical={false} stroke="var(--grid)" />
          <XAxis dataKey="time" tickFormatter={tick} tick={axisTick} stroke="var(--axis)" minTickGap={40} />
          <YAxis allowDecimals={false} tick={axisTick} stroke="var(--axis)" width={48} />
          <Tooltip cursor={{ fill: "var(--surface-2)" }} content={<ChartTooltip format={formatCount} />} />
          <Bar dataKey="count" name="Requests" fill="var(--series-1)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
        </BarChart>
      </Panel>
      <Panel title="Errors" subtitle={`failed requests per ${step}`}>
        <BarChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -12 }}>
          <CartesianGrid vertical={false} stroke="var(--grid)" />
          <XAxis dataKey="time" tickFormatter={tick} tick={axisTick} stroke="var(--axis)" minTickGap={40} />
          <YAxis allowDecimals={false} tick={axisTick} stroke="var(--axis)" width={48} />
          <Tooltip cursor={{ fill: "var(--surface-2)" }} content={<ChartTooltip format={formatCount} />} />
          <Bar dataKey="errorCount" name="Errors" fill="var(--critical)" radius={[4, 4, 0, 0]} isAnimationActive={false} />
        </BarChart>
      </Panel>
      <Panel title="Latency" subtitle="request duration percentiles">
        <LineChart data={data} margin={{ top: 8, right: 8, bottom: 0, left: -8 }}>
          <CartesianGrid vertical={false} stroke="var(--grid)" />
          <XAxis dataKey="time" tickFormatter={tick} tick={axisTick} stroke="var(--axis)" minTickGap={40} />
          <YAxis tickFormatter={(v: number) => formatDuration(v)} tick={axisTick} stroke="var(--axis)" width={68} />
          <Tooltip content={<ChartTooltip format={formatDuration} />} />
          <Legend iconType="plainline" wrapperStyle={{ fontSize: 11, color: "var(--text-muted)" }} />
          <Line dataKey="p50Ms" name="p50" stroke="var(--series-1)" strokeWidth={2} dot={false} isAnimationActive={false} />
          <Line dataKey="p95Ms" name="p95" stroke="var(--series-2)" strokeWidth={2} dot={false} isAnimationActive={false} />
          <Line dataKey="p99Ms" name="p99" stroke="var(--series-3)" strokeWidth={2} dot={false} isAnimationActive={false} />
        </LineChart>
      </Panel>
    </div>
  );
}

function Panel({ title, subtitle, children }: { title: string; subtitle: string; children: React.ReactElement }) {
  return (
    <figure className="rounded-lg border border-border bg-surface p-4">
      <figcaption className="mb-2">
        <span className="text-sm font-semibold">{title}</span>
        <span className="ml-2 text-xs text-muted">{subtitle}</span>
      </figcaption>
      <div className="h-48">
        <ResponsiveContainer width="100%" height="100%">
          {children}
        </ResponsiveContainer>
      </div>
    </figure>
  );
}
