# Ingestion benchmark results

Run directory: `20261125T204935Z`

| Environment | |
|---|---|
| cpuModel | Intel(R) Xeon(R) Processor @ 2.10GHz |
| cpus | 4 |
| loadgenGo | go1.27.4 |
| memory | 16473316 kB |
| os | linux/amd64 |
| postgres | 18.7 |

Load generator, Aperture, and PostgreSQL ran on the same machine and share its CPUs.
Throughput counts only spans acknowledged with HTTP 200, i.e. committed to PostgreSQL.

| Scenario | Aperture settings | Clients | Spans/req | Spans/s acked | Req/s | p50 ms | p95 ms | p99 ms | Non-200 | Avg batch (spans / reqs) | Avg commit ms | Durability check |
|---|---|--:|--:|--:|--:|--:|--:|--:|--:|--:|--:|---|
| workers-1 | WORKERS=1 | 16 | 100 | 11,600 | 116 | 132.1 | 169.2 | 183.1 | 0 | 1600 / 16.0 | 83.0 | pass: 292800 acknowledged spans, 292800 stored |
| workers-2 | WORKERS=2 | 16 | 100 | 14,848 | 148 | 102.2 | 149.7 | 225.6 | 0 | 789 / 7.9 | 50.0 | pass: 377000 acknowledged spans, 377000 stored |
| workers-4 | WORKERS=4 | 16 | 100 | 19,195 | 192 | 82.5 | 103.2 | 111.9 | 0 | 404 / 4.0 | 29.9 | pass: 474900 acknowledged spans, 474900 stored |
| workers-8 | WORKERS=8 | 16 | 100 | 20,975 | 210 | 77.5 | 101.0 | 112.2 | 0 | 246 / 2.5 | 26.6 | pass: 524100 acknowledged spans, 524100 stored |
| spans-10 | WORKERS=4 | 16 | 10 | 3,892 | 389 | 45.7 | 61.4 | 65.2 | 0 | 58 / 5.8 | 5.9 | pass: 99050 acknowledged spans, 99050 stored |
| spans-1000 | WORKERS=4 | 16 | 1000 | 45,999 | 46 | 329.3 | 505.1 | 692.5 | 0 | 1998 / 2.0 | 169.3 | pass: 1179000 acknowledged spans, 1179000 stored |
| small-nobatch | WORKERS=4 BATCH_SIZE=1 | 32 | 10 | 8,680 | 868 | 23.2 | 45.1 | 54.9 | 0 | 10 / 1.0 | 4.2 | pass: 215360 acknowledged spans, 215360 stored |
| small-batch | WORKERS=4 BATCH_SIZE=2000 FLUSH_INTERVAL=5ms | 32 | 10 | 9,519 | 952 | 14.1 | 27.7 | 39.5 | 0 | 35 / 3.5 | 6.1 | pass: 239580 acknowledged spans, 239580 stored |
| overload | WORKERS=1 QUEUE_CAPACITY=4 | 64 | 100 | 15,194 | 152 | 134.3 | 267.2 | 304.9 | 503×12623 | 1992 / 19.9 | 110.8 | pass: 380400 acknowledged spans, 380400 stored |

Average batch and commit figures come from Aperture's own Prometheus metrics and include the warm-up period.
