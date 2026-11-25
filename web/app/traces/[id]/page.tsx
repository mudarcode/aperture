import Link from "next/link";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { ApiError, apiGet } from "@/lib/api";
import type { Trace } from "@/lib/types";
import { formatClock, formatDuration } from "@/lib/format";
import { PageHeader } from "@/components/page-header";
import { TraceView } from "@/components/trace-view";
import { CircleAlert } from "lucide-react";

type Props = { params: Promise<{ id: string }> };

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: `Trace ${(await params).id.slice(0, 8)}` };
}

export default async function TracePage({ params }: Props) {
  const { id } = await params;
  if (!/^[0-9a-f]{32}$/i.test(id)) notFound();

  let trace: Trace;
  try {
    trace = await apiGet<Trace>(`/api/v1/traces/${id.toLowerCase()}`);
  } catch (e) {
    if (e instanceof ApiError && e.status === 404) notFound();
    throw e;
  }
  const root = trace.spans.find((s) => s.parentSpanId === null) ?? trace.spans[0]!;

  return (
    <>
      <nav aria-label="Breadcrumb" className="mb-2 text-xs text-muted">
        <Link href="/traces" className="hover:underline">
          Traces
        </Link>{" "}
        / <span className="font-mono">{trace.traceId}</span>
      </nav>
      <PageHeader
        title={
          <span className="font-mono text-lg">
            {root.serviceName}: {root.name}
          </span>
        }
        description={
          <span className="tabular">
            {formatDuration(trace.durationMs)} · {trace.spanCount} spans · {trace.services.length} services · started {formatClock(trace.startTime)}
            {trace.errorCount > 0 ? (
              <span className="ml-1 inline-flex items-center gap-1 font-medium text-critical-text">
                · <CircleAlert aria-hidden className="size-3.5" /> {trace.errorCount} failed spans
              </span>
            ) : null}
          </span>
        }
      />
      <TraceView trace={trace} />
    </>
  );
}
