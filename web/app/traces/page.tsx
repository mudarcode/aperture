import Link from "next/link";
import type { Metadata } from "next";
import { ApiError, apiGet } from "@/lib/api";
import type { Service, TraceList } from "@/lib/types";
import { first, parseWindow, WINDOWS } from "@/lib/windows";
import { assignServiceColors } from "@/lib/colors";
import { formatDuration, formatRelative, shortId } from "@/lib/format";
import { PageHeader } from "@/components/page-header";
import { TraceScatter } from "@/components/charts/trace-scatter";
import { Card, CardBody, CardHeader } from "@/components/ui/card";
import { Table, Td, Th } from "@/components/ui/table";
import { ServiceChip } from "@/components/ui/status";
import { EmptyState, ErrorState } from "@/components/ui/states";
import { CircleAlert } from "lucide-react";

export const metadata: Metadata = { title: "Traces" };

const inputClass = "h-8 w-full rounded-md border border-border bg-surface px-2 text-sm";

export default async function TracesPage({ searchParams }: { searchParams: Promise<Record<string, string | string[]>> }) {
  const sp = await searchParams;
  const window = parseWindow(sp.window);
  const filters = {
    service: first(sp.service),
    operation: first(sp.operation),
    status: first(sp.status) === "error" ? "error" : "",
    minDurationMs: first(sp.minDurationMs),
    maxDurationMs: first(sp.maxDurationMs),
  };
  const cursor = first(sp.cursor);

  const servicesP = apiGet<{ services: Service[] }>("/api/v1/services", { window });
  let list: TraceList | null = null;
  let error: string | null = null;
  try {
    list = await apiGet<TraceList>("/api/v1/traces", {
      window,
      ...filters,
      operation: filters.service ? filters.operation : "",
      limit: 50,
      cursor,
    });
  } catch (e) {
    // Bad filter input (400) is shown inline; anything else goes to the error boundary.
    if (e instanceof ApiError && e.status === 400) error = e.message;
    else throw e;
  }
  const { services } = await servicesP;
  const colors = assignServiceColors(services.map((s) => s.name));

  const olderHref = list?.nextCursor ? `/traces?${new URLSearchParams({ ...strip({ window, ...filters }), cursor: list.nextCursor })}` : null;

  return (
    <>
      <PageHeader title="Traces" description="Search requests end to end. Newest first." />

      <Card className="mb-6">
        <CardBody>
          <form method="get" action="/traces" className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-7 lg:items-end">
            <Field label="Time range" htmlFor="f-window">
              <select id="f-window" name="window" defaultValue={window} className={inputClass}>
                {WINDOWS.map((w) => (
                  <option key={w.value} value={w.value}>
                    {w.label}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Service" htmlFor="f-service">
              <select id="f-service" name="service" defaultValue={filters.service} className={inputClass}>
                <option value="">Any service</option>
                {services.map((s) => (
                  <option key={s.name} value={s.name}>
                    {s.name}
                  </option>
                ))}
              </select>
            </Field>
            <Field label="Operation" htmlFor="f-operation" className="lg:col-span-2">
              <input
                id="f-operation"
                name="operation"
                defaultValue={filters.operation}
                placeholder="Exact span name, e.g. POST /charge"
                className={inputClass}
              />
            </Field>
            <Field label="Status" htmlFor="f-status">
              <select id="f-status" name="status" defaultValue={filters.status} className={inputClass}>
                <option value="">Any</option>
                <option value="error">Contains errors</option>
              </select>
            </Field>
            <Field label="Duration (ms)" htmlFor="f-min">
              <div className="flex items-center gap-1">
                <input id="f-min" name="minDurationMs" inputMode="decimal" defaultValue={filters.minDurationMs} placeholder="min" className={inputClass} aria-label="Minimum duration in milliseconds" />
                <span className="text-muted">–</span>
                <input name="maxDurationMs" inputMode="decimal" defaultValue={filters.maxDurationMs} placeholder="max" className={inputClass} aria-label="Maximum duration in milliseconds" />
              </div>
            </Field>
            <button type="submit" className="h-8 rounded-md bg-accent px-3 text-sm font-medium text-white hover:opacity-90">
              Search
            </button>
          </form>
          {filters.operation && !filters.service ? (
            <p className="mt-2 text-xs text-muted">Operation filtering needs a service; the operation was ignored.</p>
          ) : null}
        </CardBody>
      </Card>

      {error ? <ErrorState title="Invalid search" message={error} /> : null}

      {list && list.traces.length === 0 ? (
        <Card>
          <EmptyState title="No traces match">Widen the time range or remove a filter.</EmptyState>
        </Card>
      ) : null}

      {list && list.traces.length > 0 ? (
        <>
          <Card className="mb-6">
            <CardHeader title="Duration over time" description={`${list.traces.length} traces on this page. Select a point to open the trace.`} />
            <CardBody>
              <TraceScatter traces={list.traces} />
            </CardBody>
          </Card>
          <Card>
            <Table label="Traces">
              <thead>
                <tr>
                  <Th>Root operation</Th>
                  <Th>Services</Th>
                  <Th numeric>Spans</Th>
                  <Th numeric>Errors</Th>
                  <Th numeric>Duration</Th>
                  <Th numeric>Started</Th>
                  <Th>Trace ID</Th>
                </tr>
              </thead>
              <tbody>
                {list.traces.map((t) => (
                  <tr key={t.traceId} className="hover:bg-surface-2">
                    <Td className="max-w-xs">
                      <Link href={`/traces/${t.traceId}`} className="block hover:underline">
                        <span className="text-xs text-muted">
                          {t.rootService ?? "root span not received (yet)"}
                        </span>
                        <span className="block truncate font-mono text-xs">{t.rootName ?? "–"}</span>
                      </Link>
                    </Td>
                    <Td>
                      <div className="flex max-w-sm flex-wrap gap-x-3 gap-y-1">
                        {t.services.map((s) => (
                          <ServiceChip key={s} name={s} color={colors.get(s) ?? "var(--series-1)"} />
                        ))}
                      </div>
                    </Td>
                    <Td numeric>{t.spanCount}</Td>
                    <Td numeric>
                      {t.errorCount > 0 ? (
                        <span className="inline-flex items-center gap-1 font-medium text-critical-text">
                          <CircleAlert aria-hidden className="size-3.5" />
                          {t.errorCount}
                        </span>
                      ) : (
                        <span className="text-muted">0</span>
                      )}
                    </Td>
                    <Td numeric>{formatDuration(t.durationMs)}</Td>
                    <Td numeric className="text-muted" title={t.startTime}>
                      {formatRelative(t.startTime)}
                    </Td>
                    <Td>
                      <Link href={`/traces/${t.traceId}`} className="font-mono text-xs text-accent hover:underline">
                        {shortId(t.traceId)}
                      </Link>
                    </Td>
                  </tr>
                ))}
              </tbody>
            </Table>
            <div className="flex justify-end gap-2 px-4 py-3">
              {cursor ? (
                <Link href={`/traces?${new URLSearchParams(strip({ window, ...filters }))}`} className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-surface-2">
                  Newest
                </Link>
              ) : null}
              {olderHref ? (
                <Link href={olderHref} className="rounded-md border border-border px-3 py-1.5 text-sm hover:bg-surface-2">
                  Older traces
                </Link>
              ) : null}
            </div>
          </Card>
        </>
      ) : null}
    </>
  );
}

function strip(o: Record<string, string>): Record<string, string> {
  return Object.fromEntries(Object.entries(o).filter(([, v]) => v !== ""));
}

function Field({ label, htmlFor, className, children }: { label: string; htmlFor: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={className}>
      <label htmlFor={htmlFor} className="mb-1 block text-xs text-muted">
        {label}
      </label>
      {children}
    </div>
  );
}
