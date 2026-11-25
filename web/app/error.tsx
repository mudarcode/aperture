"use client";

import { ErrorState } from "@/components/ui/states";

export default function Error({ error, reset }: { error: Error; reset: () => void }) {
  return (
    <div className="space-y-4">
      <ErrorState title="Something went wrong loading this page" message={error.message} />
      <button type="button" onClick={reset} className="rounded-md border border-border bg-surface px-3 py-1.5 text-sm hover:bg-surface-2">
        Try again
      </button>
    </div>
  );
}
