"use client";

import { useMemo, useState } from "react";
import Link from "next/link";
import {
  Background,
  Controls,
  Handle,
  MarkerType,
  Position,
  ReactFlow,
  type Edge,
  type EdgeProps,
  type Node,
  type NodeProps,
  BaseEdge,
  EdgeLabelRenderer,
  getBezierPath,
} from "@xyflow/react";
import "@xyflow/react/dist/style.css";
import { CircleAlert } from "lucide-react";
import type { DependencyGraph as Graph, Service } from "@/lib/types";
import { layoutGraph, NODE_HEIGHT, NODE_WIDTH } from "@/lib/graph-layout";
import { assignServiceColors } from "@/lib/colors";
import { formatCount, formatDuration, formatPercent } from "@/lib/format";

type ServiceNodeData = { name: string; color: string; stats?: Service; window: string };
type CallEdgeData = { calls: number; errorRate: number; p95Ms: number; highlighted: boolean };

function ServiceNode({ data }: NodeProps<Node<ServiceNodeData>>) {
  const s = data.stats;
  const failing = s && s.errorRate >= 0.05;
  return (
    <Link
      href={`/services/${encodeURIComponent(data.name)}?window=${data.window}`}
      className="flex h-full flex-col justify-center rounded-lg border border-border bg-surface px-3 shadow-sm hover:bg-surface-2"
      style={{ borderLeft: `4px solid ${data.color}` }}
    >
      <Handle type="target" position={Position.Left} className="!size-1.5 !border-0 !bg-[var(--axis)]" />
      <span className="truncate text-sm font-semibold">{data.name}</span>
      {s ? (
        <>
          <span className="tabular mt-0.5 text-[11px] whitespace-nowrap text-muted">
            {formatCount(s.count)} req · p95 {formatDuration(s.p95Ms)}
          </span>
          <span className={`tabular inline-flex items-center gap-1 text-[11px] whitespace-nowrap ${failing ? "font-medium text-critical-text" : "text-muted"}`}>
            {failing ? <CircleAlert aria-hidden className="size-3" /> : null}
            {formatPercent(s.errorRate)} errors
          </span>
        </>
      ) : null}
      <Handle type="source" position={Position.Right} className="!size-1.5 !border-0 !bg-[var(--axis)]" />
    </Link>
  );
}

function CallEdge({ id, sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition, data, markerEnd }: EdgeProps<Edge<CallEdgeData>>) {
  const [path, labelX, labelY] = getBezierPath({ sourceX, sourceY, targetX, targetY, sourcePosition, targetPosition });
  const failing = (data?.errorRate ?? 0) >= 0.05;
  const stroke = failing ? "var(--critical)" : data?.highlighted ? "var(--accent)" : "var(--axis)";
  return (
    <>
      <BaseEdge id={id} path={path} markerEnd={markerEnd} style={{ stroke, strokeWidth: data?.highlighted ? 2.5 : 1.5 }} />
      <EdgeLabelRenderer>
        <div
          className="tabular nodrag nopan pointer-events-none absolute rounded border border-border bg-surface px-1.5 py-0.5 text-[10px] whitespace-nowrap text-muted"
          style={{ transform: `translate(-50%, -50%) translate(${labelX}px, ${labelY}px)` }}
        >
          {formatCount(data?.calls ?? 0)} calls · p95 {formatDuration(data?.p95Ms ?? 0)}
          {data && data.errorRate > 0 ? <span className={failing ? "font-medium text-critical-text" : ""}> · {formatPercent(data.errorRate)} err</span> : null}
        </div>
      </EdgeLabelRenderer>
    </>
  );
}

const nodeTypes = { service: ServiceNode };
const edgeTypes = { call: CallEdge };

export function DependencyGraphView({ graph, services, window }: { graph: Graph; services: Service[]; window: string }) {
  const [hovered, setHovered] = useState<string | null>(null);
  const { nodes, edges } = useMemo(() => {
    // Include services that made no cross-service calls so nothing disappears.
    const ids = [...new Set([...graph.nodes.map((n) => n.id), ...services.map((s) => s.name)])];
    const colors = assignServiceColors(ids);
    const stats = new Map(services.map((s) => [s.name, s]));
    const positions = layoutGraph(ids, graph.edges);
    const nodes: Node<ServiceNodeData>[] = positions.map((p) => ({
      id: p.id,
      type: "service",
      position: { x: p.x, y: p.y },
      width: NODE_WIDTH,
      height: NODE_HEIGHT,
      data: { name: p.id, color: colors.get(p.id)!, stats: stats.get(p.id), window },
      draggable: true,
    }));
    const edges: Edge<CallEdgeData>[] = graph.edges.map((e) => ({
      id: `${e.source}->${e.target}`,
      source: e.source,
      target: e.target,
      type: "call",
      markerEnd: { type: MarkerType.ArrowClosed, width: 16, height: 16 },
      data: { calls: e.count, errorRate: e.errorRate, p95Ms: e.p95Ms, highlighted: false },
    }));
    return { nodes, edges };
  }, [graph, services, window]);

  const shownEdges = hovered
    ? edges.map((e) => ({ ...e, data: { ...e.data!, highlighted: e.source === hovered || e.target === hovered } }))
    : edges;

  return (
    <div className="h-[560px] w-full" aria-label="Service dependency graph">
      <ReactFlow
        nodes={nodes}
        edges={shownEdges}
        nodeTypes={nodeTypes}
        edgeTypes={edgeTypes}
        onNodeMouseEnter={(_, n) => setHovered(n.id)}
        onNodeMouseLeave={() => setHovered(null)}
        fitView
        fitViewOptions={{ padding: 0.15 }}
        minZoom={0.3}
        proOptions={{ hideAttribution: true }}
        nodesConnectable={false}
      >
        <Background gap={24} color="var(--grid)" />
        <Controls showInteractive={false} />
      </ReactFlow>
    </div>
  );
}
