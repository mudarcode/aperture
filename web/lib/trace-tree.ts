import type { Span } from "./types.ts";

export interface TreeRow {
  span: Span;
  depth: number;
  childCount: number;
  /** Parent ID is set but that span was never received. */
  orphan: boolean;
  /** Duration not covered by any child span. */
  selfTimeMs: number;
}

/**
 * Orders spans for a waterfall: depth-first, children sorted by start time.
 * Spans whose parent is missing (not yet received, or dropped upstream) are
 * shown at the top level and flagged, so no span is ever hidden.
 */
export function buildTree(spans: Span[]): TreeRow[] {
  const byId = new Map(spans.map((s) => [s.spanId, s]));
  const children = new Map<string, Span[]>();
  const roots: Span[] = [];

  for (const s of spans) {
    if (s.parentSpanId && byId.has(s.parentSpanId) && s.parentSpanId !== s.spanId) {
      const list = children.get(s.parentSpanId) ?? [];
      list.push(s);
      children.set(s.parentSpanId, list);
    } else {
      roots.push(s);
    }
  }
  const byStart = (a: Span, b: Span) => a.startOffsetMs - b.startOffsetMs || a.spanId.localeCompare(b.spanId);
  roots.sort(byStart);
  for (const list of children.values()) list.sort(byStart);

  const rows: TreeRow[] = [];
  const visited = new Set<string>();
  // Iterative DFS: traces can be deep, and recursion depth is not bounded by the API.
  const stack: { span: Span; depth: number }[] = roots.map((span) => ({ span, depth: 0 })).reverse();
  while (stack.length > 0) {
    const { span, depth } = stack.pop()!;
    if (visited.has(span.spanId)) continue; // defends against parent cycles in bad data
    visited.add(span.spanId);
    const kids = children.get(span.spanId) ?? [];
    rows.push({
      span,
      depth,
      childCount: kids.length,
      orphan: span.parentSpanId !== null && !byId.has(span.parentSpanId),
      selfTimeMs: selfTime(span, kids),
    });
    for (let i = kids.length - 1; i >= 0; i--) stack.push({ span: kids[i]!, depth: depth + 1 });
  }
  // Spans only reachable through a cycle are appended so they still render.
  for (const s of spans) {
    if (!visited.has(s.spanId)) rows.push({ span: s, depth: 0, childCount: 0, orphan: true, selfTimeMs: s.durationMs });
  }
  return rows;
}

/** Span duration minus the union of its children's intervals (clipped to the span). */
export function selfTime(span: Span, kids: Span[]): number {
  const start = span.startOffsetMs;
  const end = start + span.durationMs;
  const intervals = kids
    .map((k) => [Math.max(start, k.startOffsetMs), Math.min(end, k.startOffsetMs + k.durationMs)] as const)
    .filter(([a, b]) => b > a)
    .sort((x, y) => x[0] - y[0]);
  let covered = 0;
  let curA = -Infinity;
  let curB = -Infinity;
  for (const [a, b] of intervals) {
    if (a > curB) {
      if (curB > curA) covered += curB - curA;
      curA = a;
      curB = b;
    } else {
      curB = Math.max(curB, b);
    }
  }
  if (curB > curA) covered += curB - curA;
  return Math.max(0, span.durationMs - covered);
}

/**
 * Returns the IDs of spans on the critical path: the chain of work that
 * determined the trace's end-to-end latency. Starting from each root, walk
 * backwards from the span's end and repeatedly follow the child that finished
 * last before the current point in time.
 */
export function criticalPath(spans: Span[]): Set<string> {
  const byId = new Map(spans.map((s) => [s.spanId, s]));
  const children = new Map<string, Span[]>();
  for (const s of spans) {
    if (s.parentSpanId && byId.has(s.parentSpanId)) {
      const list = children.get(s.parentSpanId) ?? [];
      list.push(s);
      children.set(s.parentSpanId, list);
    }
  }
  const end = (s: Span) => s.startOffsetMs + s.durationMs;
  const onPath = new Set<string>();

  const walk = (span: Span) => {
    if (onPath.has(span.spanId)) return;
    onPath.add(span.spanId);
    const kids = [...(children.get(span.spanId) ?? [])].sort((a, b) => end(b) - end(a));
    let cursor = end(span);
    for (const k of kids) {
      // A child is critical if it was still running at the cursor (allowing for
      // small clock skew between hosts) - i.e. the parent was waiting on it.
      if (k.startOffsetMs < cursor && end(k) <= cursor + 0.5) {
        walk(k);
        cursor = k.startOffsetMs;
      }
    }
  };

  const roots = spans.filter((s) => !s.parentSpanId || !byId.has(s.parentSpanId));
  if (roots.length === 0) return onPath;
  // Only the root that ends last bounds the trace.
  walk(roots.reduce((a, b) => (end(b) > end(a) ? b : a)));
  return onPath;
}

export interface ServiceBreakdown {
  service: string;
  spans: number;
  errors: number;
  selfTimeMs: number;
}

/** Aggregates self time per service: where the trace actually spent its time. */
export function serviceBreakdown(rows: TreeRow[]): ServiceBreakdown[] {
  const m = new Map<string, ServiceBreakdown>();
  for (const r of rows) {
    const b = m.get(r.span.serviceName) ?? { service: r.span.serviceName, spans: 0, errors: 0, selfTimeMs: 0 };
    b.spans++;
    if (r.span.statusCode === "error") b.errors++;
    b.selfTimeMs += r.selfTimeMs;
    m.set(r.span.serviceName, b);
  }
  return [...m.values()].sort((a, b) => b.selfTimeMs - a.selfTimeMs);
}

/** Visible rows after collapsing: hides every descendant of a collapsed span. */
export function visibleRows(rows: TreeRow[], collapsed: Set<string>): TreeRow[] {
  const out: TreeRow[] = [];
  let hideBelow = Infinity;
  for (const r of rows) {
    if (r.depth > hideBelow) continue;
    hideBelow = Infinity;
    out.push(r);
    if (collapsed.has(r.span.spanId)) hideBelow = r.depth;
  }
  return out;
}
