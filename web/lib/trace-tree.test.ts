import { test } from "node:test";
import assert from "node:assert/strict";
import { buildTree, criticalPath, selfTime, serviceBreakdown, visibleRows } from "./trace-tree.ts";
import { assignServiceColors } from "./colors.ts";
import { formatDuration, formatPercent } from "./format.ts";
import type { Span } from "./types.ts";

function span(id: string, parent: string | null, start: number, dur: number, extra: Partial<Span> = {}): Span {
  return {
    spanId: id, parentSpanId: parent, serviceName: "svc", name: id, kind: "internal",
    startTime: "", startTimeUnixNano: "0", endTimeUnixNano: "0",
    startOffsetMs: start, durationMs: dur, statusCode: "unset",
    attributes: {}, resourceAttributes: {}, events: [], links: [],
    droppedAttributesCount: 0, droppedEventsCount: 0, droppedLinksCount: 0,
    ...extra,
  };
}

// root 0..100
//   a 10..40
//     a1 15..20
//   b 30..90   (overlaps a)
//   c 50..60   (inside b's window, parallel)
const spans = [
  span("b", "root", 30, 60, { serviceName: "db" }),
  span("root", null, 0, 100),
  span("a1", "a", 15, 5),
  span("c", "root", 50, 10),
  span("a", "root", 10, 30, { statusCode: "error" }),
];

test("buildTree orders depth-first by start time", () => {
  const rows = buildTree(spans);
  assert.deepEqual(rows.map((r) => `${r.span.spanId}@${r.depth}`), ["root@0", "a@1", "a1@2", "b@1", "c@1"]);
  assert.equal(rows[0]!.childCount, 3);
});

test("orphans are shown at top level and flagged", () => {
  const rows = buildTree([span("x", null, 0, 10), span("y", "missing", 5, 1)]);
  assert.deepEqual(rows.map((r) => [r.span.spanId, r.depth, r.orphan]), [["x", 0, false], ["y", 0, true]]);
});

test("parent cycles do not loop forever and keep every span", () => {
  const rows = buildTree([span("p", "q", 0, 1), span("q", "p", 0, 1)]);
  assert.equal(rows.length, 2);
});

test("selfTime subtracts the union of child intervals", () => {
  // root children cover 10..90 (a: 10-40, b: 30-90, c: 50-60) => self = 100 - 80
  assert.equal(selfTime(spans[1]!, [spans[0]!, spans[3]!, spans[4]!]), 20);
  // children extending past the parent are clipped
  assert.equal(selfTime(span("p", null, 0, 10), [span("k", "p", 5, 100)]), 5);
});

test("criticalPath follows the last-finishing child chain", () => {
  const cp = criticalPath(spans);
  // b ends last (90) and started at 30; a ended at 40 > 30 so it overlaps b and is not critical;
  // c is inside b's window.
  assert.deepEqual([...cp].sort(), ["b", "root"]);

  const sequential = [span("r", null, 0, 100), span("s1", "r", 0, 40), span("s2", "r", 45, 50)];
  assert.deepEqual([...criticalPath(sequential)].sort(), ["r", "s1", "s2"]);
});

test("visibleRows hides descendants of collapsed spans", () => {
  const rows = buildTree(spans);
  assert.deepEqual(visibleRows(rows, new Set(["a"])).map((r) => r.span.spanId), ["root", "a", "b", "c"]);
  assert.deepEqual(visibleRows(rows, new Set(["root"])).map((r) => r.span.spanId), ["root"]);
});

test("serviceBreakdown sums self time per service", () => {
  const b = new Map(serviceBreakdown(buildTree(spans)).map((x) => [x.service, x]));
  // root 20 + a 25 + a1 5 + c 10 (parallel work is counted per span)
  assert.deepEqual(b.get("svc"), { service: "svc", spans: 4, errors: 1, selfTimeMs: 60 });
  assert.deepEqual(b.get("db"), { service: "db", spans: 1, errors: 0, selfTimeMs: 60 });
});

test("assignServiceColors gives distinct non-red slots to up to seven services", () => {
  const names = ["gateway", "catalog", "checkout", "inventory", "payments", "a", "b"];
  const colors = assignServiceColors(names);
  assert.equal(new Set(colors.values()).size, 7);
  assert.ok(![...colors.values()].includes("var(--series-8)"));
  assert.deepEqual(assignServiceColors([...names].reverse()), colors);
});

test("formatting", () => {
  assert.equal(formatDuration(0.25), "250 µs");
  assert.equal(formatDuration(12.345), "12.3 ms");
  assert.equal(formatDuration(1534), "1.53 s");
  assert.equal(formatDuration(125_000), "2m 05s");
  assert.equal(formatPercent(0.0512), "5.1%");
  assert.equal(formatPercent(0.0004), "<0.1%");
});
