# Pinned versions

Go module versions are locked in `go.mod`/`go.sum`. This file records the
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
| GitHub Actions: checkout / setup-go | v7.0.1 / v7.1.0 | `.github/workflows/ci.yml` |

Notes:

* The Collector release was chosen one minor behind the newest (released the
  same day) to avoid a brand-new release.
* goose v3.30.1 rather than v3.30.2 for the same reason.
