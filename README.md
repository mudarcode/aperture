# Aperture

Aperture is a distributed tracing backend. It ingests standard OpenTelemetry
traces over OTLP/HTTP, processes them through a bounded concurrent pipeline,
stores them in PostgreSQL, and (in progress) serves a web UI for diagnosing
latency and failures across microservices.

> Status: under active development. The ingestion slice (OTLP → pipeline →
> PostgreSQL, with Collector and Compose) is complete and tested. The query
> API, web UI, demo services, and benchmarks are being built next.

## Quick start

Requirements: Docker with Compose v2, and Go 1.27+ for the trace generator.

```sh
docker compose up -d --build --wait          # PostgreSQL, Aperture, OTel Collector
go run ./cmd/tracegen -traces 100            # send traces through the Collector
docker compose exec postgres psql -U aperture -c \
  "select service_name, count(*) from spans group by 1"
```

Endpoints on `localhost:8080`: `POST /v1/traces` (OTLP/HTTP protobuf),
`GET /healthz`, `GET /readyz`, `GET /metrics`.

## Design highlights

* **Standards-based ingestion.** Official OTLP protobuf types, gzip, and the
  OTLP/HTTP status-code and partial-success semantics. Malformed spans are
  rejected individually and reported; valid spans in the same request are
  kept.
* **Explicit durability.** A `200` means the spans are in a committed
  PostgreSQL transaction. Retries are idempotent through a
  `(trace_id, span_id)` primary key. See [docs/ingestion.md](docs/ingestion.md).
* **Bounded, batched pipeline.** A fixed-size queue sheds load with `503` +
  `Retry-After` instead of growing memory; workers coalesce requests into
  single-statement `unnest` inserts.
* **Operable.** Liveness/readiness probes, Prometheus metrics for every stage,
  structured JSON logs, graceful shutdown that drains in-flight work.

## Development

```sh
make up      # start the stack
make test    # full test suite incl. PostgreSQL integration and end-to-end tests
make down
```

Integration tests create and drop their own databases on the server named by
`APERTURE_TEST_DATABASE_URL`; without it they are skipped.

## Documentation

* [Ingestion, durability, and status codes](docs/ingestion.md)
* [Configuration](docs/configuration.md)
* [Pinned versions](VERSIONS.md)

## Local-demo limitations

The Compose stack uses fixed local credentials, plain HTTP, and no
authentication on ingestion or metrics. Ports bind to `127.0.0.1` only. Do not
expose it to a network as-is.
