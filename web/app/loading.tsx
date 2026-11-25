export default function Loading() {
  return (
    <div role="status" aria-live="polite" className="space-y-4">
      <span className="sr-only">Loading</span>
      <div className="h-7 w-48 animate-pulse rounded bg-surface-2" />
      <div className="h-64 animate-pulse rounded-lg bg-surface-2" />
    </div>
  );
}
