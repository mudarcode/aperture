import Link from "next/link";
import type { Metadata } from "next";
import { notFound } from "next/navigation";
import { apiGet } from "@/lib/api";
import type { Operation, Service, TimeSeries } from "@/lib/types";
import { parseWindow, stepFor } from "@/lib/windows";
import { assignServiceColors } from "@/lib/colors";
import { formatCount, formatDuration } from "@/lib/format";
import { PageHeader } from "@/components/page-header";
import { WindowSelect } from "@/components/window-select";
import { ServiceCharts } from "@/components/charts/service-charts";
import { Card, CardHeader } from "@/components/ui/card";
import { Table, Td, Th } from "@/components/ui/table";
import { ErrorRate } from "@/components/ui/status";
import { EmptyState } from "@/components/ui/states";

type Props = {
  params: Promise<{ name: string }>;
  searchParams: Promise<Record<string, string | string[]>>;
};

export async function generateMetadata({ params }: Props): Promise<Metadata> {
  return { title: decodeURIComponent((await params).name) };
}

export default async function ServicePage({ params, searchParams }: Props) {
  const name = decodeURIComponent((await params).name);
  const window = parseWindow((await searchParams).window);
  const path = `/api/v1/services/${encodeURIComponent(name)}`;

  const [{ services }, { operations }, series] = await Promise.all([
    apiGet<{ services: Service[] }>("/api/v1/services", { window }),
    apiGet<{ operations: Operation[] }>(`${path}/operations`, { window }),
    apiGet<TimeSeries>(`${path}/timeseries`, { window, step: stepFor(window) }),
  ]);
  const svc = services.find((s) => s.name === name);
  if (!svc && operations.length === 0) notFound();
  const color = assignServiceColors(services.map((s) => s.name)).get(name) ?? "var(--series-1)";
  const tracesHref = (extra: Record<string, string> = {}) =>
    `/traces?${new URLSearchParams({ window, service: name, ...extra }).toString()}`;

  return (
    <>
      <PageHeader
        title={
          <span className="inline-flex items-center gap-2">
            <span aria-hidden className="size-3 rounded-sm" style={{ background: color }} />
            {name}
          </span>
        }
        description={
          svc ? (
            <>
              {formatCount(svc.count)} requests · <ErrorRate rate={svc.errorRate} count={svc.errorCount} /> errors · p95{" "}
              {formatDuration(svc.p95Ms)}
            </>
          ) : undefined
        }
        actions={
          <div className="flex flex-wrap items-center gap-2">
            <Link href={tracesHref()} className="inline-flex h-8 items-center rounded-md border border-border bg-surface px-2.5 text-sm hover:bg-surface-2">
              View traces
            </Link>
            <Link
              href={tracesHref({ status: "error" })}
              className="inline-flex h-8 items-center rounded-md border border-border bg-surface px-2.5 text-sm hover:bg-surface-2"
            >
              Failed traces
            </Link>
            <WindowSelect />
          </div>
        }
      />

      {series.buckets.length > 0 ? (
        <ServiceCharts buckets={series.buckets} step={series.step} />
      ) : (
        <Card>
          <EmptyState title="No requests in this time range" />
        </Card>
      )}

      <Card className="mt-6">
        <CardHeader title="Operations" description="Every span name this service emitted, most frequent first." />
        {operations.length === 0 ? (
          <EmptyState title="No operations in this time range" />
        ) : (
          <Table label={`Operations of ${name}`}>
            <thead>
              <tr>
                <Th>Operation</Th>
                <Th>Kind</Th>
                <Th numeric>Spans</Th>
                <Th numeric>Error rate</Th>
                <Th numeric>p50</Th>
                <Th numeric>p95</Th>
                <Th numeric>p99</Th>
              </tr>
            </thead>
            <tbody>
              {operations.map((o) => (
                <tr key={o.name} className="hover:bg-surface-2">
                  <Td className="max-w-md">
                    <Link href={tracesHref({ operation: o.name })} className="font-mono text-xs break-all hover:underline">
                      {o.name || "(unnamed)"}
                    </Link>
                  </Td>
                  <Td className="text-xs text-muted">{o.kind}</Td>
                  <Td numeric>{formatCount(o.count)}</Td>
                  <Td numeric>
                    <ErrorRate rate={o.errorRate} count={o.errorCount} />
                  </Td>
                  <Td numeric>{formatDuration(o.p50Ms)}</Td>
                  <Td numeric>{formatDuration(o.p95Ms)}</Td>
                  <Td numeric>{formatDuration(o.p99Ms)}</Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </>
  );
}
