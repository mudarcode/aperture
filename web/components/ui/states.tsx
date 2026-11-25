import type { ReactNode } from "react";
import { CircleAlert, Inbox } from "lucide-react";

export function EmptyState({ title, children }: { title: string; children?: ReactNode }) {
  return (
    <div className="flex flex-col items-center justify-center gap-2 px-6 py-12 text-center">
      <Inbox aria-hidden className="size-6 text-muted" />
      <p className="text-sm font-medium">{title}</p>
      {children ? <div className="max-w-md text-sm text-muted">{children}</div> : null}
    </div>
  );
}

export function ErrorState({ title, message }: { title: string; message: string }) {
  return (
    <div role="alert" className="flex items-start gap-3 rounded-lg border border-border bg-critical-soft p-4 text-sm">
      <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-critical-text" />
      <div>
        <p className="font-medium text-critical-text">{title}</p>
        <p className="mt-1 text-text">{message}</p>
      </div>
    </div>
  );
}
