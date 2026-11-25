import Link from "next/link";
import type { Metadata } from "next";
import { apiGet } from "@/lib/api";
import type { DependencyGraph, Service } from "@/lib/types";
import { parseWindow, WINDOWS } from "@/lib/windows";
import { formatCount, formatDuration } from "@/lib/format";
import { PageHeader } from "@/components/page-header";
import { WindowSelect } from "@/components/window-select";
import { DependencyGraphView } from "@/components/dependency-graph";
import { Card, CardHeader } from "@/components/ui/card";
import { Table, Td, Th } from "@/components/ui/table";
import { ErrorRate } from "@/components/ui/status";
import { EmptyState } from "@/components/ui/states";

export const metadata: Metadata = { title: "Dependencies" };

export default async function DependenciesPage({ searchParams }: { searchParams: Promise<Record<string, string | string[]>> }) {
  const window = parseWindow((await searchParams).window);
  const [graph, { services }] = await Promise.all([
    apiGet<DependencyGraph>("/api/v1/dependencies", { window }),
    apiGet<{ services: Service[] }>("/api/v1/services", { window }),
  ]);
  const label = WINDOWS.find((w) => w.value === window)!.label.toLowerCase();

  return (
    <>
      <PageHeader
        title="Dependencies"
        description={`Service calls derived from parent/child spans that cross a service boundary, ${label}.`}
        actions={<WindowSelect />}
      />
      {services.length === 0 ? (
        <Card>
          <EmptyState title="No spans in this time range" />
        </Card>
      ) : (
        <>
          <Card className="mb-6 overflow-hidden">
            <CardHeader title="Service map" description="Edges show call volume, p95 latency of the callee, and error rate. Red edges fail 5% or more. Hover a service to highlight its calls." />
            <DependencyGraphView graph={graph} services={services} window={window} />
          </Card>
          <Card>
            <CardHeader title="Calls" description="The same data as a table." />
            {graph.edges.length === 0 ? (
              <EmptyState title="No cross-service calls in this time range" />
            ) : (
              <Table label="Service calls">
                <thead>
                  <tr>
                    <Th>Caller</Th>
                    <Th>Callee</Th>
                    <Th numeric>Calls</Th>
                    <Th numeric>Error rate</Th>
                    <Th numeric>p50</Th>
                    <Th numeric>p95</Th>
                    <Th numeric>p99</Th>
                  </tr>
                </thead>
                <tbody>
                  {graph.edges.map((e) => (
                    <tr key={`${e.source}->${e.target}`} className="hover:bg-surface-2">
                      <Td>
                        <Link className="hover:underline" href={`/services/${encodeURIComponent(e.source)}?window=${window}`}>
                          {e.source}
                        </Link>
                      </Td>
                      <Td>
                        <Link className="hover:underline" href={`/services/${encodeURIComponent(e.target)}?window=${window}`}>
                          {e.target}
                        </Link>
                      </Td>
                      <Td numeric>{formatCount(e.count)}</Td>
                      <Td numeric>
                        <ErrorRate rate={e.errorRate} count={e.errorCount} />
                      </Td>
                      <Td numeric>{formatDuration(e.p50Ms)}</Td>
                      <Td numeric>{formatDuration(e.p95Ms)}</Td>
                      <Td numeric>{formatDuration(e.p99Ms)}</Td>
                    </tr>
                  ))}
                </tbody>
              </Table>
            )}
          </Card>
        </>
      )}
    </>
  );
}
