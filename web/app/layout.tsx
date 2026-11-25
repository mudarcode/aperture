import type { Metadata } from "next";
import { Suspense, type ReactNode } from "react";
import { Nav } from "@/components/nav";
import "./globals.css";

export const metadata: Metadata = {
  title: { default: "Aperture", template: "%s · Aperture" },
  description: "Distributed tracing for diagnosing latency and failures across services.",
};

export default function RootLayout({ children }: { children: ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen font-sans antialiased">
        <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:p-2">
          Skip to content
        </a>
        <Suspense fallback={<div className="h-12 border-b border-border bg-surface" />}>
          <Nav />
        </Suspense>
        <main id="main" className="mx-auto max-w-7xl px-4 py-6">
          {children}
        </main>
      </body>
    </html>
  );
}
