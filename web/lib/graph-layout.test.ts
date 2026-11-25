import { test } from "node:test";
import assert from "node:assert/strict";
import { layoutGraph, NODE_WIDTH } from "./graph-layout.ts";

test("callers are placed left of callees", () => {
  const pos = new Map(
    layoutGraph(["gateway", "checkout", "payments"], [
      { source: "gateway", target: "checkout" },
      { source: "checkout", target: "payments" },
    ]).map((p) => [p.id, p]),
  );
  assert.ok(pos.get("gateway")!.x + NODE_WIDTH <= pos.get("checkout")!.x);
  assert.ok(pos.get("checkout")!.x + NODE_WIDTH <= pos.get("payments")!.x);
});

test("cycles and isolated nodes are laid out", () => {
  const pos = layoutGraph(["a", "b", "lonely"], [
    { source: "a", target: "b" },
    { source: "b", target: "a" },
  ]);
  assert.equal(pos.length, 3);
  for (const p of pos) assert.ok(Number.isFinite(p.x) && Number.isFinite(p.y));
});
