# Benchmarks

Every number on this page comes from a run that is checked into
[`bench/results/`](../bench/results). Each run directory holds the raw
load-generator JSON, the Aperture settings, a snapshot of Aperture's
Prometheus metrics, and a generated `report.md`. To reproduce:

```sh
docker compose stop      # free the CPUs; the bench uses its own Postgres
make bench               # = bench/run.sh 20s
```

`bench/run.sh` starts a dedicated PostgreSQL 18.7 container on port 5433. For
each scenario it then:

- builds and starts a fresh Aperture with empty tables;
- drives it with `cmd/loadgen` for 5 s of warm-up plus 20 s of measurement;
- runs a **durability check** that counts the stored spans and confirms that
  every span acknowledged with HTTP 200 is in the database.

## What is measured

- **Spans/s acked** counts only spans in requests that received HTTP 200.
  Aperture sends 200 only after the PostgreSQL transaction containing those
  spans has committed (see [ingestion.md](ingestion.md)), so this is durable
  throughput, not accept-into-memory throughput.
- **Latency** is the client-observed time from sending a request to receiving
  its response. That includes the full wait for the commit.
- Requests are pre-encoded OTLP protobuf (gzip off). Span IDs are unique per
  request so nothing is deduplicated away.

## Environment and caveats

| | |
|---|---|
| CPU | Intel Xeon @ 2.10 GHz, **4 vCPUs** (cloud VM) |
| Memory | 16 GB |
| PostgreSQL | 18.7 (Docker, default config, local volume) |
| Go | 1.27.4 |

The load generator, Aperture and PostgreSQL all share those 4 vCPUs. The
absolute numbers are therefore a floor for this design, not a capacity claim.
On a dedicated database host they would look different. The relative
comparisons below (workers, batch sizes, before/after) are what the runs are
good for. Each scenario ran once, and repeated runs of the same configuration
differed by a few percent.

## Results

Run [`20261125T205834Z`](../bench/results/20261125T205834Z/report.md),
the current code:

| Scenario | Settings | Clients × spans/req | Spans/s acked | p50 ms | p99 ms | Non-200 | Durability |
|---|---|--:|--:|--:|--:|--:|---|
| workers-1 | `WORKERS=1` | 16 × 100 | 11,760 | 133.2 | 165.2 | 0 | pass |
| workers-2 | `WORKERS=2` | 16 × 100 | 16,014 | 99.7 | 139.2 | 0 | pass |
| workers-4 | `WORKERS=4` (default) | 16 × 100 | 19,624 | 80.9 | 111.1 | 0 | pass |
| workers-8 | `WORKERS=8` | 16 × 100 | 20,419 | 78.6 | 110.0 | 0 | pass |
| spans-10 | default | 16 × 10 | 3,958 | 44.2 | 63.8 | 0 | pass |
| spans-1000 | default | 16 × 1000 | 47,596 | 319.8 | 576.2 | 0 | pass |
| small-nobatch | `BATCH_SIZE=1` | 32 × 10 | 8,250 | 21.6 | 54.6 | 0 | pass |
| small-batch | `FLUSH_INTERVAL=5ms` | 32 × 10 | 10,100 | 13.6 | 34.1 | 0 | pass |
| overload | `WORKERS=1 QUEUE_CAPACITY=4` | 64 × 100 | 15,596 | 124.0 | 300.6 | 13,854 × 503 | pass |

All nine scenarios passed the durability check. Together they acknowledged
about 3.9 million spans, and every one of them was in PostgreSQL afterwards.

## What the numbers say

**Workers scale until the machine runs out of CPU.** Going from 1 to 2 to 4
workers raised throughput by 36% and then 23%. Going from 4 to 8 added only 4%.
With 4 vCPUs shared by three processes, more concurrent transactions just
queue for CPU. This is why the default is 4 workers.

**Batching is the main lever, and request size dominates it.** At 1,000 spans
per request, Aperture commits about 47.6k spans/s with the default settings: 2.4 times
the rate at 100 spans/request and 12 times the rate at 10 spans/request.
Per-transaction and per-request costs (HTTP, decode, commit fsync) are paid
less often. This is also why the Collector in `compose.yaml` batches before
exporting.

**Coalescing small requests helps both throughput and latency.** With 32
clients sending 10-span requests:

- `BATCH_SIZE=1` (one transaction per request) reached 8,250 spans/s at
  p50 21.6 ms.
- Letting workers coalesce requests for up to 5 ms reached 10,100 spans/s
  (+22%) at p50 13.6 ms (-37%).

Fewer commits means less time waiting on WAL flushes. The flush interval is
a latency/throughput dial. In the spans-10 scenario, the default 50 ms
interval sets most of the 44 ms p50.

**Overload sheds fast and loses nothing.** With 1 worker and a 4-slot queue
under 64 clients, Aperture kept committing at about the 1-worker rate. It
rejected the excess 13,854 requests with `503 Retry-After`, at a median of
4.3 ms (p99 26.3 ms), instead of letting latency grow without bound. Every
acknowledged span was stored. Rejected spans were never acknowledged, so a
real exporter would retry them.

## Profile-guided optimization: streaming JSON in the converter

Before optimizing, I took a 15 s CPU profile of the workers-4 scenario with
`APERTURE_PPROF_ADDR`. Within the Aperture process:

| | Share of CPU samples |
|---|--:|
| OTLP → span conversion (`Converter.Convert`) | ~27% |
| …of which `encoding/json.Marshal` of attributes, events, links | ~21% |
| protobuf unmarshal | ~23% |
| PostgreSQL write path (pgx encode + network) | ~12% |

Conversion built a `map[string]any` per attribute set, then reflected over it
with `json.Marshal`. I replaced that with an encoder that appends JSON
straight from the protobuf values. Its rules:

- last duplicate key wins, as before;
- NaN and ±Inf are written as strings, as before;
- strings are escaped like `encoding/json`, including U+2028/2029.

The existing converter tests passed unchanged. New tests round-trip tricky
strings and floats through `encoding/json`, and a fuzz test checks that any
valid UTF-8 string round-trips.

Microbenchmark (`BenchmarkConvert100Spans`: 100 spans with 12 attributes each,
`-benchtime 2s -count 3`, same machine):

| | ns/op | B/op | allocs/op |
|---|--:|--:|--:|
| Before | 662,833 – 765,371 | 225,910 | 5,379 |
| After | 184,188 – 205,375 | 127,000 | 414 |
| Change | **~3.6× faster** | −44% | **−92%** |

**End-to-end, it barely moved the needle**, which is worth stating plainly.
Comparing the full suite before (run
[`20261125T204935Z`](../bench/results/20261125T204935Z/report.md)) with
after (run `20261125T205834Z`):

| Scenario | Before spans/s | After spans/s |
|---|--:|--:|
| workers-1 | 11,600 | 11,760 |
| workers-4 | 19,195 | 19,624 |
| workers-8 | 20,975 | 20,419 |
| spans-1000 | 45,999 | 47,596 |
| small-batch | 9,519 | 10,100 |

The differences are within run-to-run noise. My reading, which I have not
separately measured: on this box the limit is the CPU and fsync time that
PostgreSQL itself spends on the shared 4 vCPUs, not Aperture's CPU. Freeing
Aperture CPU doesn't raise throughput while Postgres is the bottleneck. The
optimization still pays off where Aperture runs on its own host, because it
cuts conversion time per span by about 70% and allocations by over 90% on
each ingest node. The next measurement worth making is the same suite with PostgreSQL
on a separate machine.

## Not measured here

- Query API latency under a large table (millions of traces over days).
- PostgreSQL tuning (`synchronous_commit`, `wal_buffers`, unlogged tables).
  The defaults were left on deliberately, because ack-after-commit is only
  meaningful with a durable commit.
- gzip-compressed requests (the Collector sends gzip by default). Expect
  slightly more Aperture CPU and less network.
