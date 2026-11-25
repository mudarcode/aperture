"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import { ChevronDown, ChevronRight, CircleAlert, Route, Unlink } from "lucide-react";
import type { Attributes, Span, Trace } from "@/lib/types";
import { buildTree, criticalPath, serviceBreakdown, visibleRows, type TreeRow } from "@/lib/trace-tree";
import { assignServiceColors } from "@/lib/colors";
import { formatAttribute, formatClock, formatDuration } from "@/lib/format";
import { cn } from "@/lib/cn";
import { StatusBadge } from "@/components/ui/status";

export function TraceView({ trace }: { trace: Trace }) {
  const rows = useMemo(() => buildTree(trace.spans), [trace.spans]);
  const critical = useMemo(() => criticalPath(trace.spans), [trace.spans]);
  const breakdown = useMemo(() => serviceBreakdown(rows), [rows]);
  const colors = useMemo(() => assignServiceColors(trace.services), [trace.services]);
  // The most likely root cause: the earliest failed span with no failed children.
  const firstError = useMemo(() => {
    const failedParents = new Set(rows.filter((r) => r.span.statusCode === "error" && r.span.parentSpanId).map((r) => r.span.parentSpanId));
    const leaves = rows.filter((r) => r.span.statusCode === "error" && !failedParents.has(r.span.spanId));
    return leaves.sort((a, b) => a.span.startOffsetMs - b.span.startOffsetMs)[0];
  }, [rows]);

  const [collapsed, setCollapsed] = useState<Set<string>>(new Set());
  const [selected, setSelected] = useState<string | null>(firstError?.span.spanId ?? rows[0]?.span.spanId ?? null);
  const [showCritical, setShowCritical] = useState(false);

  const shown = visibleRows(rows, collapsed);
  const selectedRow = rows.find((r) => r.span.spanId === selected) ?? null;
  const total = Math.max(trace.durationMs, 0.001);

  const toggle = (id: string) =>
    setCollapsed((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });

  return (
    <div className="space-y-6">
      <Breakdown breakdown={breakdown} colors={colors} total={breakdown.reduce((s, b) => s + b.selfTimeMs, 0)} />

      <div className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_380px]">
        <section aria-label="Span waterfall" className="min-w-0 rounded-lg border border-border bg-surface">
          <div className="flex flex-wrap items-center justify-between gap-2 border-b border-border px-4 py-2">
            <h2 className="text-sm font-semibold">Waterfall</h2>
            <div className="flex items-center gap-2">
              <button
                type="button"
                aria-pressed={showCritical}
                onClick={() => setShowCritical((v) => !v)}
                className={cn(
                  "inline-flex h-7 items-center gap-1.5 rounded-md border border-border px-2 text-xs hover:bg-surface-2",
                  showCritical && "bg-accent-soft font-medium",
                )}
              >
                <Route aria-hidden className="size-3.5" /> Critical path
              </button>
              <button type="button" onClick={() => setCollapsed(new Set())} className="h-7 rounded-md border border-border px-2 text-xs hover:bg-surface-2">
                Expand all
              </button>
              {firstError ? (
                <button
                  type="button"
                  onClick={() => setSelected(firstError.span.spanId)}
                  className="inline-flex h-7 items-center gap-1 rounded-md border border-border px-2 text-xs text-critical-text hover:bg-surface-2"
                >
                  <CircleAlert aria-hidden className="size-3.5" /> Root-cause error
                </button>
              ) : null}
            </div>
          </div>

          <div className="overflow-x-auto">
            <div className="min-w-[720px]">
              <TimeAxis total={total} />
              <ul role="tree" aria-label="Spans">
                {shown.map((row) => (
                  <WaterfallRow
                    key={row.span.spanId}
                    row={row}
                    total={total}
                    color={colors.get(row.span.serviceName) ?? "var(--series-1)"}
                    selected={row.span.spanId === selected}
                    collapsed={collapsed.has(row.span.spanId)}
                    dimmed={showCritical && !critical.has(row.span.spanId)}
                    critical={showCritical && critical.has(row.span.spanId)}
                    onSelect={() => setSelected(row.span.spanId)}
                    onToggle={() => toggle(row.span.spanId)}
                  />
                ))}
              </ul>
            </div>
          </div>
        </section>

        <aside aria-label="Span details" className="min-w-0">
          {selectedRow ? <SpanDetails row={selectedRow} color={colors.get(selectedRow.span.serviceName) ?? "var(--series-1)"} onCritical={critical.has(selectedRow.span.spanId)} /> : null}
        </aside>
      </div>
    </div>
  );
}

const LABEL_WIDTH = 300;

function TimeAxis({ total }: { total: number }) {
  const ticks = [0, 0.25, 0.5, 0.75, 1];
  return (
    <div className="flex border-b border-border text-[11px] text-muted" aria-hidden>
      <div style={{ width: LABEL_WIDTH }} className="shrink-0 px-4 py-1.5">
        Service · operation
      </div>
      <div className="flex-1 py-1.5 pr-4">
        <div className="relative">
          {ticks.map((t) => (
            <span
              key={t}
              className={cn("tabular absolute whitespace-nowrap", t === 0 ? "" : t === 1 ? "-translate-x-full" : "-translate-x-1/2")}
              style={{ left: `${t * 100}%` }}
            >
              {formatDuration(total * t)}
            </span>
          ))}
          &nbsp;
        </div>
      </div>
    </div>
  );
}

function WaterfallRow({
  row,
  total,
  color,
  selected,
  collapsed,
  dimmed,
  critical,
  onSelect,
  onToggle,
}: {
  row: TreeRow;
  total: number;
  color: string;
  selected: boolean;
  collapsed: boolean;
  dimmed: boolean;
  critical: boolean;
  onSelect: () => void;
  onToggle: () => void;
}) {
  const { span, depth, childCount, orphan } = row;
  const left = (span.startOffsetMs / total) * 100;
  const width = Math.max((span.durationMs / total) * 100, 0.3);
  const isError = span.statusCode === "error";
  const labelRight = left + width < 70;

  return (
    <li
      role="treeitem"
      aria-selected={selected}
      aria-expanded={childCount > 0 ? !collapsed : undefined}
      aria-level={depth + 1}
      className={cn("group flex border-b border-border last:border-b-0", selected ? "bg-accent-soft" : "hover:bg-surface-2", dimmed && "opacity-40")}
    >
      <div style={{ width: LABEL_WIDTH }} className="flex shrink-0 items-center gap-1 py-1 pr-2 pl-2">
        <span style={{ width: depth * 12 }} className="shrink-0" aria-hidden />
        {childCount > 0 ? (
          <button
            type="button"
            onClick={onToggle}
            aria-label={`${collapsed ? "Expand" : "Collapse"} ${span.name}`}
            className="grid size-5 shrink-0 place-items-center rounded text-muted hover:bg-surface-2 hover:text-text"
          >
            {collapsed ? <ChevronRight className="size-3.5" /> : <ChevronDown className="size-3.5" />}
          </button>
        ) : (
          <span className="size-5 shrink-0" aria-hidden />
        )}
        <button type="button" onClick={onSelect} className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
          <span aria-hidden className="h-4 w-1 shrink-0 rounded-sm" style={{ background: color }} />
          <span className="min-w-0">
            <span className="block truncate text-[11px] leading-tight text-muted">
              {span.serviceName}
              {collapsed ? ` · ${childCount} hidden` : ""}
            </span>
            <span className="flex items-center gap-1 truncate text-xs leading-tight">
              {isError ? <CircleAlert aria-label="Error" className="size-3 shrink-0 text-critical-text" /> : null}
              {orphan ? <Unlink aria-label="Parent span not received" className="size-3 shrink-0 text-muted" /> : null}
              <span className="truncate">{span.name || "(unnamed)"}</span>
            </span>
          </span>
        </button>
      </div>
      <button type="button" onClick={onSelect} tabIndex={-1} aria-hidden className="flex-1 py-1 pr-4">
        <span className="relative block h-full">
          <span
            className={cn("absolute top-1/2 h-3 -translate-y-1/2 rounded-sm", critical && "ring-2 ring-text ring-offset-1 ring-offset-surface")}
            style={{ left: `${left}%`, width: `max(2px, ${width}%)`, background: isError ? "var(--critical)" : color }}
          />
          <span
            className="tabular absolute top-1/2 -translate-y-1/2 text-[11px] whitespace-nowrap text-muted"
            style={labelRight ? { left: `calc(${left + width}% + 6px)` } : { right: `calc(${100 - left}% + 6px)` }}
          >
            {formatDuration(span.durationMs)}
          </span>
        </span>
      </button>
    </li>
  );
}

function Breakdown({
  breakdown,
  colors,
  total,
}: {
  breakdown: ReturnType<typeof serviceBreakdown>;
  colors: Map<string, string>;
  total: number;
}) {
  return (
    <section aria-label="Time by service" className="rounded-lg border border-border bg-surface p-4">
      <h2 className="text-sm font-semibold">Where the time went</h2>
      <p className="mt-0.5 text-xs text-muted">Self time per service: span time not spent waiting on child spans.</p>
      <div className="mt-3 flex h-3 w-full gap-0.5 overflow-hidden rounded-sm" aria-hidden>
        {breakdown.map((b) =>
          b.selfTimeMs > 0 ? (
            <span key={b.service} style={{ width: `${(b.selfTimeMs / total) * 100}%`, background: colors.get(b.service) }} className="h-full first:rounded-l-sm last:rounded-r-sm" />
          ) : null,
        )}
      </div>
      <ul className="mt-3 grid grid-cols-1 gap-x-6 gap-y-1 text-xs sm:grid-cols-2 lg:grid-cols-3">
        {breakdown.map((b) => (
          <li key={b.service} className="flex items-center gap-2">
            <span aria-hidden className="size-2.5 shrink-0 rounded-sm" style={{ background: colors.get(b.service) }} />
            <Link href={`/services/${encodeURIComponent(b.service)}`} className="truncate hover:underline">
              {b.service}
            </Link>
            <span className="tabular ml-auto text-muted">
              {formatDuration(b.selfTimeMs)} · {total > 0 ? Math.round((b.selfTimeMs / total) * 100) : 0}%
              {b.errors > 0 ? <span className="text-critical-text"> · {b.errors} err</span> : null}
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function SpanDetails({ row, color, onCritical }: { row: TreeRow; color: string; onCritical: boolean }) {
  const s: Span = row.span;
  return (
    <div className="rounded-lg border border-border bg-surface xl:sticky xl:top-4">
      <div className="border-b border-border px-4 py-3">
        <p className="flex items-center gap-1.5 text-xs text-muted">
          <span aria-hidden className="size-2.5 rounded-sm" style={{ background: color }} />
          {s.serviceName} · {s.kind}
        </p>
        <h2 className="mt-1 font-mono text-sm font-semibold break-all">{s.name || "(unnamed)"}</h2>
        <div className="mt-2 flex flex-wrap items-center gap-2">
          <StatusBadge status={s.statusCode} />
          {onCritical ? <span className="rounded bg-accent-soft px-1.5 py-0.5 text-xs">On critical path</span> : null}
          {row.orphan ? <span className="rounded bg-surface-2 px-1.5 py-0.5 text-xs text-muted">Parent not received</span> : null}
        </div>
        {s.statusMessage ? <p className="mt-2 text-xs break-words text-critical-text">{s.statusMessage}</p> : null}
      </div>
      <dl className="grid grid-cols-3 gap-x-3 gap-y-2 border-b border-border px-4 py-3 text-xs">
        <Fact label="Duration" value={formatDuration(s.durationMs)} />
        <Fact label="Self time" value={formatDuration(row.selfTimeMs)} />
        <Fact label="Starts at" value={`+${formatDuration(s.startOffsetMs)}`} />
        <div className="col-span-3">
          <Fact label="Start" value={formatClock(s.startTime)} />
        </div>
        <div className="col-span-3">
          <Fact label="Span ID" value={s.spanId} mono />
        </div>
        {s.parentSpanId ? (
          <div className="col-span-3">
            <Fact label="Parent span ID" value={s.parentSpanId} mono />
          </div>
        ) : null}
      </dl>
      <AttrSection title="Attributes" attrs={s.attributes} dropped={s.droppedAttributesCount} />
      {s.events.length > 0 ? (
        <section className="border-b border-border px-4 py-3">
          <h3 className="text-xs font-semibold">Events ({s.events.length})</h3>
          <ol className="mt-2 space-y-2">
            {s.events.map((e, i) => (
              <li key={i} className="text-xs">
                <p>
                  <span className="tabular text-muted">+{formatDuration(e.startOffsetMs)}</span> <span className="font-medium">{e.name}</span>
                </p>
                <AttrList attrs={e.attributes} />
              </li>
            ))}
          </ol>
        </section>
      ) : null}
      {s.links.length > 0 ? (
        <section className="border-b border-border px-4 py-3">
          <h3 className="text-xs font-semibold">Links ({s.links.length})</h3>
          <ul className="mt-2 space-y-1 text-xs">
            {s.links.map((l, i) => (
              <li key={i}>
                <Link href={`/traces/${l.trace_id}`} className="font-mono text-accent hover:underline">
                  {l.trace_id.slice(0, 16)}… / {l.span_id}
                </Link>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
      <AttrSection title="Resource" attrs={s.resourceAttributes} dropped={0} />
      {s.scopeName ? (
        <p className="px-4 py-3 text-xs break-all text-muted">
          Instrumentation scope: <span className="font-mono">{s.scopeName}</span>
          {s.scopeVersion ? ` ${s.scopeVersion}` : ""}
        </p>
      ) : null}
    </div>
  );
}

function Fact({ label, value, mono }: { label: string; value: string; mono?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="text-muted">{label}</dt>
      <dd className={cn("tabular break-all", mono && "font-mono")}>{value}</dd>
    </div>
  );
}

function AttrSection({ title, attrs, dropped }: { title: string; attrs: Attributes; dropped: number }) {
  const n = Object.keys(attrs).length;
  return (
    <section className="border-b border-border px-4 py-3 last:border-b-0">
      <h3 className="text-xs font-semibold">
        {title} ({n})
      </h3>
      {n === 0 ? <p className="mt-1 text-xs text-muted">None</p> : <AttrList attrs={attrs} />}
      {dropped > 0 ? <p className="mt-1 text-xs text-muted">{dropped} attributes dropped by the SDK</p> : null}
    </section>
  );
}

function AttrList({ attrs }: { attrs: Attributes }) {
  const entries = Object.entries(attrs).sort(([a], [b]) => a.localeCompare(b));
  if (entries.length === 0) return null;
  return (
    <dl className="mt-1.5 grid grid-cols-[minmax(0,2fr)_minmax(0,3fr)] gap-x-3 gap-y-1 text-xs">
      {entries.map(([k, v]) => (
        <div key={k} className="contents">
          <dt className="truncate font-mono text-muted" title={k}>
            {k}
          </dt>
          <dd className="font-mono break-all">{formatAttribute(v)}</dd>
        </div>
      ))}
    </dl>
  );
}
