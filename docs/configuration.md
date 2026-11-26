# Configuration

Aperture reads environment variables. Invalid values stop startup with an
error that lists every problem found.

| Variable | Default | Description |
|---|---|---|
| `APERTURE_DATABASE_URL` | (required) | PostgreSQL connection string |
| `APERTURE_HTTP_ADDR` | `:8080` | listen address for ingestion, API, health, and metrics |
| `APERTURE_DB_MAX_CONNS` | `16` | connection pool size; should be at least `APERTURE_WORKERS` plus room for queries |
| `APERTURE_AUTO_MIGRATE` | `true` | apply migrations on `serve`; set `false` and run `aperture migrate` to manage them separately |
| `APERTURE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `APERTURE_LOG_FORMAT` | `json` | `json` or `text` |
| `APERTURE_SHUTDOWN_TIMEOUT` | `30s` | upper bound on graceful shutdown |
| `APERTURE_PPROF_ADDR` | (off) | if set (e.g. `127.0.0.1:6060`), serves Go `net/http/pprof` there; keep it on localhost |
| **Pipeline** | | |
| `APERTURE_QUEUE_CAPACITY` | `1024` | queued export requests before shedding load with 503 |
| `APERTURE_WORKERS` | `4` | concurrent database writers |
| `APERTURE_BATCH_SIZE` | `2000` | target spans per transaction |
| `APERTURE_FLUSH_INTERVAL` | `50ms` | max wait to fill a batch |
| `APERTURE_WRITE_TIMEOUT` | `10s` | per-transaction timeout |
| **Requests** | | |
| `APERTURE_MAX_REQUEST_BYTES` | `8388608` | max body size on the wire |
| `APERTURE_MAX_DECOMPRESSED_BYTES` | `33554432` | max size after gzip decoding |
| `APERTURE_MAX_SPANS_PER_REQUEST` | `10000` | larger requests get 413 |
| `APERTURE_ACK_TIMEOUT` | `15s` | max wait for commit before answering 503 |
| `APERTURE_RETRY_AFTER` | `1s` | `Retry-After` value on 503 |
| **Span limits** (exceeding any rejects the span) | | |
| `APERTURE_MAX_ATTRIBUTES_PER_SPAN` | `128` | also bounds event/link attributes and array/map lengths |
| `APERTURE_MAX_RESOURCE_ATTRIBUTES` | `128` | |
| `APERTURE_MAX_ATTRIBUTE_VALUE_BYTES` | `16384` | string and bytes values |
| `APERTURE_MAX_EVENTS_PER_SPAN` | `128` | |
| `APERTURE_MAX_LINKS_PER_SPAN` | `128` | |
| `APERTURE_MAX_NAME_BYTES` | `1024` | span and service names |
| `APERTURE_MAX_FUTURE_SKEW` | `1h` | how far in the future a span may end |
| `APERTURE_MAX_SPAN_DURATION` | `24h` | longest accepted span |
