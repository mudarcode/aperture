# Ingestion benchmark results

Run directory: `20261125T205834Z`

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
| workers-1 | WORKERS=1 | 16 | 100 | 11,760 | 118 | 133.2 | 159.1 | 165.2 | 0 | 1600 / 16.0 | 81.7 | pass: 297600 acknowledged spans, 297600 stored |
| workers-2 | WORKERS=2 | 16 | 100 | 16,014 | 160 | 99.7 | 127.2 | 139.2 | 0 | 796 / 8.0 | 45.4 | pass: 403600 acknowledged spans, 403600 stored |
| workers-4 | WORKERS=4 | 16 | 100 | 19,624 | 196 | 80.9 | 100.7 | 111.1 | 0 | 407 / 4.1 | 28.5 | pass: 490200 acknowledged spans, 490200 stored |
| workers-8 | WORKERS=8 | 16 | 100 | 20,419 | 204 | 78.6 | 98.3 | 110.0 | 0 | 233 / 2.3 | 26.3 | pass: 512800 acknowledged spans, 512800 stored |
| spans-10 | WORKERS=4 | 16 | 10 | 3,958 | 396 | 44.2 | 60.6 | 63.8 | 0 | 56 / 5.6 | 6.2 | pass: 95960 acknowledged spans, 95960 stored |
| spans-1000 | WORKERS=4 | 16 | 1000 | 47,596 | 48 | 319.8 | 471.2 | 576.2 | 0 | 1995 / 2.0 | 165.1 | pass: 1207000 acknowledged spans, 1207000 stored |
| small-nobatch | WORKERS=4 BATCH_SIZE=1 | 32 | 10 | 8,250 | 825 | 21.6 | 44.6 | 54.6 | 0 | 10 / 1.0 | 4.4 | pass: 206010 acknowledged spans, 206010 stored |
| small-batch | WORKERS=4 BATCH_SIZE=2000 FLUSH_INTERVAL=5ms | 32 | 10 | 10,100 | 1010 | 13.6 | 25.4 | 34.1 | 0 | 37 / 3.7 | 6.1 | pass: 255830 acknowledged spans, 255830 stored |
| overload | WORKERS=1 QUEUE_CAPACITY=4 | 64 | 100 | 15,596 | 156 | 124.0 | 250.3 | 300.6 | 503×13854 (503 p50 4.3 ms, p99 26.3 ms) | 1989 / 19.9 | 108.4 | pass: 393800 acknowledged spans, 393800 stored |

Average batch and commit figures come from Aperture's own Prometheus metrics and include the warm-up period.
