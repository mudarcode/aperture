import { Graph, layout } from "@dagrejs/dagre";

export const NODE_WIDTH = 220;
export const NODE_HEIGHT = 76;

export interface Positioned {
  id: string;
  x: number; // top-left, as React Flow expects
  y: number;
}

/**
 * Lays out a service graph left-to-right (callers on the left, callees on
 * the right) with dagre. Cycles are allowed; dagre breaks them internally.
 */
export function layoutGraph(nodes: string[], edges: { source: string; target: string }[]): Positioned[] {
  const g = new Graph();
  g.setGraph({ rankdir: "LR", nodesep: 56, ranksep: 220, marginx: 20, marginy: 20 });
  g.setDefaultEdgeLabel(() => ({}));
  for (const id of nodes) g.setNode(id, { width: NODE_WIDTH, height: NODE_HEIGHT });
  for (const e of edges) g.setEdge(e.source, e.target);
  layout(g);
  return nodes.map((id) => {
    const n = g.node(id) as { x: number; y: number };
    return { id, x: n.x - NODE_WIDTH / 2, y: n.y - NODE_HEIGHT / 2 };
  });
}
