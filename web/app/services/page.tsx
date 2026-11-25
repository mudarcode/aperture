import Link from "next/link";
import type { Metadata } from "next";
import { apiGet } from "@/lib/api";
import type { Service } from "@/lib/types";
import { parseWindow, WINDOWS } from "@/lib/windows";
import { assignServiceColors } from "@/lib/colors";
import { formatCount, formatDuration } from "@/lib/format";
import { PageHeader } from "@/components/page-header";
import { WindowSelect } from "@/components/window-select";
import { Card, CardHeader } from "@/components/ui/card";
import { Table, Td, Th } from "@/components/ui/table";
import { ErrorRate, ServiceChip } from "@/components/ui/status";
import { EmptyState } from "@/components/ui/states";

export const metadata: Metadata = { title: "Services" };

export default async function ServicesPage({ searchParams }: { searchParams: Promise<Record<string, string | string[]>> }) {
  const window = parseWindow((await searchParams).window);
  const { services } = await apiGet<{ services: Service[] }>("/api/v1/services", { window });
  const colors = assignServiceColors(services.map((s) => s.name));
  const windowLabel = WINDOWS.find((w) => w.value === window)!.label.toLowerCase();

  const totalReq = services.reduce((n, s) => n + s.count, 0);
  const totalErr = services.reduce((n, s) => n + s.errorCount, 0);

  return (
    <>
      <PageHeader
        title="Services"
        description={`Requests handled by each service (server, consumer, and root spans), ${windowLabel}.`}
        actions={<WindowSelect />}
      />
      <dl className="mb-6 grid grid-cols-1 gap-3 sm:grid-cols-3">
        <Stat label="Services" value={formatCount(services.length)} />
        <Stat label="Requests" value={formatCount(totalReq)} />
        <Stat label="Failed requests" value={formatCount(totalErr)} />
      </dl>
      <Card>
        <CardHeader title="Service health" description="Rate, errors, and duration percentiles per service." />
        {services.length === 0 ? (
          <EmptyState title="No spans in this time range">
            Start the demo stack with <code className="font-mono">docker compose up</code>, or send OTLP traces to{" "}
            <code className="font-mono">/v1/traces</code>.
          </EmptyState>
        ) : (
          <Table label="Service health">
            <thead>
              <tr>
                <Th>Service</Th>
                <Th numeric>Requests</Th>
                <Th numeric>Error rate</Th>
                <Th numeric>p50</Th>
                <Th numeric>p95</Th>
                <Th numeric>p99</Th>
                <Th numeric>Spans</Th>
              </tr>
            </thead>
            <tbody>
              {services.map((s) => (
                <tr key={s.name} className="hover:bg-surface-2">
                  <Td>
                    <Link href={`/services/${encodeURIComponent(s.name)}?window=${window}`} className="hover:underline">
                      <ServiceChip name={s.name} color={colors.get(s.name)!} />
                    </Link>
                  </Td>
                  <Td numeric>{formatCount(s.count)}</Td>
                  <Td numeric>
                    <ErrorRate rate={s.errorRate} count={s.errorCount} />
                  </Td>
                  <Td numeric>{s.count ? formatDuration(s.p50Ms) : "–"}</Td>
                  <Td numeric>{s.count ? formatDuration(s.p95Ms) : "–"}</Td>
                  <Td numeric>{s.count ? formatDuration(s.p99Ms) : "–"}</Td>
                  <Td numeric className="text-muted">
                    {formatCount(s.spanCount)}
                  </Td>
                </tr>
              ))}
            </tbody>
          </Table>
        )}
      </Card>
    </>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="rounded-lg border border-border bg-surface px-4 py-3">
      <dt className="text-xs text-muted">{label}</dt>
      <dd className="mt-1 text-2xl font-semibold">{value}</dd>
    </div>
  );
}
