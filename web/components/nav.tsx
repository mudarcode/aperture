"use client";

import Link from "next/link";
import { usePathname, useSearchParams } from "next/navigation";
import { Aperture } from "lucide-react";
import { cn } from "@/lib/cn";

const LINKS = [
  { href: "/services", label: "Services" },
  { href: "/traces", label: "Traces" },
  { href: "/dependencies", label: "Dependencies" },
];

export function Nav() {
  const pathname = usePathname();
  const params = useSearchParams();
  const window = params.get("window");
  const suffix = window ? `?window=${encodeURIComponent(window)}` : "";
  return (
    <header className="border-b border-border bg-surface">
      <div className="mx-auto flex h-12 max-w-7xl items-center gap-4 px-4 sm:gap-6">
        <Link href="/services" className="flex shrink-0 items-center gap-2 text-sm font-semibold">
          <Aperture aria-hidden className="size-5 text-accent" />
          Aperture
        </Link>
        <nav aria-label="Main" className="flex min-w-0 gap-1 overflow-x-auto">
          {LINKS.map((l) => {
            const active = pathname === l.href || pathname.startsWith(l.href + "/");
            return (
              <Link
                key={l.href}
                href={l.href + suffix}
                aria-current={active ? "page" : undefined}
                className={cn(
                  "rounded-md px-3 py-1.5 text-sm whitespace-nowrap text-muted hover:bg-surface-2 hover:text-text",
                  active && "bg-surface-2 font-medium text-text",
                )}
              >
                {l.label}
              </Link>
            );
          })}
        </nav>
      </div>
    </header>
  );
}
