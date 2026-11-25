// Mirrors the JSON returned by Aperture's query API (internal/api).

export interface LatencyStats {
  count: number;
  errorCount: number;
  errorRate: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
}

export interface Service extends LatencyStats {
  name: string;
  spanCount: number;
}

export interface Operation extends LatencyStats {
  name: string;
  kind: SpanKind;
}

export interface Bucket extends LatencyStats {
  time: string;
}

export interface TimeSeries {
  step: string;
  buckets: Bucket[];
}

export interface TraceSummary {
  traceId: string;
  startTime: string;
  startTimeUnixNano: string;
  durationMs: number;
  spanCount: number;
  errorCount: number;
  services: string[];
  rootService: string | null;
  rootName: string | null;
}

export interface TraceList {
  traces: TraceSummary[];
  nextCursor?: string;
}

export type SpanKind = "unspecified" | "internal" | "server" | "client" | "producer" | "consumer";
export type StatusCode = "unset" | "ok" | "error";
export type AttributeValue = string | number | boolean | null | AttributeValue[] | { [k: string]: AttributeValue };
export type Attributes = Record<string, AttributeValue>;

export interface SpanEvent {
  name: string;
  timeUnixNano: string;
  startOffsetMs: number;
  attributes: Attributes;
}

export interface SpanLink {
  trace_id: string;
  span_id: string;
  attributes: Attributes;
}

export interface Span {
  spanId: string;
  parentSpanId: string | null;
  serviceName: string;
  name: string;
  kind: SpanKind;
  startTime: string;
  startTimeUnixNano: string;
  endTimeUnixNano: string;
  startOffsetMs: number;
  durationMs: number;
  statusCode: StatusCode;
  statusMessage?: string;
  traceState?: string;
  scopeName?: string;
  scopeVersion?: string;
  attributes: Attributes;
  resourceAttributes: Attributes;
  events: SpanEvent[];
  links: SpanLink[];
  droppedAttributesCount: number;
  droppedEventsCount: number;
  droppedLinksCount: number;
}

export interface Trace {
  traceId: string;
  startTime: string;
  startTimeUnixNano: string;
  durationMs: number;
  spanCount: number;
  errorCount: number;
  services: string[];
  spans: Span[];
}

export interface DependencyEdge extends LatencyStats {
  source: string;
  target: string;
}

export interface DependencyGraph {
  nodes: { id: string }[];
  edges: DependencyEdge[];
}
