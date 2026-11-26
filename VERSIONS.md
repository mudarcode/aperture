# Pinned versions

Go module versions are locked in `go.mod`/`go.sum` and npm versions in
`web/package.json` (exact, no ranges) and `web/package-lock.json`. This file records the
rest, and why each was chosen. Versions were current, supported releases as
of November 2026.

| Component | Version | Where pinned |
|---|---|---|
| Go toolchain | 1.27.4 (language 1.27) | `go.mod` `toolchain`, `Dockerfile` |
| PostgreSQL | 18.7 | `compose.yaml`, CI service |
| OpenTelemetry Collector (core distribution) | 0.165.0 | `compose.yaml` |
| Alpine (runtime image) | 3.24 | `Dockerfile` |
| OTLP protobuf (`go.opentelemetry.io/proto/otlp`) | v1.11.1 | `go.mod` |
| OpenTelemetry Go SDK + OTLP/HTTP exporter | v1.47.0 | `go.mod` |
| pgx | v5.13.0 | `go.mod` |
| goose (migrations) | v3.30.1 | `go.mod` |
| Prometheus client_golang | v1.25.0 | `go.mod` |
| Node.js | 24.22.3 (LTS) | `web/Dockerfile`, CI |
| Next.js / React | 16.4.6 / 19.3.1 | `web/package.json` |
| TypeScript | 6.0.3 (strict) | `web/package.json` |
| Tailwind CSS | 4.3.5 | `web/package.json` |
| Recharts | 3.10.1 | `web/package.json` |
| React Flow (`@xyflow/react`) + dagre | 12.12.1 + 3.1.3 | `web/package.json` |
| Radix UI / lucide-react | 1.8.1 / 1.78.0 | `web/package.json` |
| ESLint | 9.39.5 | `web/package.json` |
| Playwright | 1.65.1 | `web/package.json` |
| GitHub Actions: checkout / setup-go / setup-node / upload-artifact | v7.0.1 / v7.1.0 / v7.1.0 / v7.0.2 | `.github/workflows/ci.yml` |

Notes:

* The Collector release was chosen one minor behind the newest (released the
  same day) to avoid a brand-new release.
* goose v3.30.1 rather than v3.30.2 for the same reason.
* ESLint 9 rather than 10: `eslint-config-next` 16.4 does not yet declare
  support for ESLint 10.
* Web unit tests use Node's built-in test runner with type stripping instead
  of a separate test framework, which keeps the dependency tree smaller.
