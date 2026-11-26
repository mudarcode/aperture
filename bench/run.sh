#!/usr/bin/env bash
# Reproducible ingestion benchmarks.
#
# Starts a dedicated PostgreSQL container (port 5433), builds Aperture, and
# runs each scenario against a fresh Aperture process with empty tables.
# Results (JSON per scenario + report.md) go to bench/results/<timestamp>/.
#
# Stop other heavy workloads first (e.g. `docker compose stop`): the load
# generator, Aperture, and PostgreSQL share the machine's CPUs.
#
# Usage: bench/run.sh [duration]   (default 20s per scenario)
set -euo pipefail
cd "$(dirname "$0")/.."

DURATION="${1:-20s}"
PG_IMAGE="postgres:18.7-alpine"
PG_URL="postgres://aperture:aperture@127.0.0.1:5433/aperture?sslmode=disable"
PORT=8090
OUT="bench/results/$(date -u +%Y%m%dT%H%M%SZ)"
mkdir -p "$OUT" bin

echo "building..."
go build -o bin/aperture ./cmd/aperture
go build -o bin/loadgen ./cmd/loadgen
go build -o bin/benchreport ./cmd/benchreport

if ! docker inspect aperture-bench-pg >/dev/null 2>&1; then
  docker run -d --name aperture-bench-pg -p 127.0.0.1:5433:5432 \
    -e POSTGRES_USER=aperture -e POSTGRES_PASSWORD=aperture -e POSTGRES_DB=aperture \
    "$PG_IMAGE" >/dev/null
fi
until docker exec aperture-bench-pg pg_isready -U aperture -d aperture >/dev/null 2>&1; do sleep 1; done
sleep 2

APERTURE_PID=""
stop_aperture() {
  if [ -n "$APERTURE_PID" ]; then
    kill -TERM "$APERTURE_PID" 2>/dev/null || true
    wait "$APERTURE_PID" 2>/dev/null || true
    APERTURE_PID=""
  fi
}
trap stop_aperture EXIT

# scenario NAME "APERTURE ENV..." "LOADGEN FLAGS..."
scenario() {
  local name="$1" env="$2" flags="$3"
  echo "=== $name"
  echo "$env" > "$OUT/$name.env"
  stop_aperture
  # Apply migrations, then start from empty tables.
  env APERTURE_DATABASE_URL="$PG_URL" ./bin/aperture migrate >/dev/null
  docker exec aperture-bench-pg psql -q -U aperture -d aperture -c "TRUNCATE spans, traces" >/dev/null
  # shellcheck disable=SC2086
  env APERTURE_DATABASE_URL="$PG_URL" APERTURE_HTTP_ADDR="127.0.0.1:$PORT" APERTURE_LOG_LEVEL=warn \
      APERTURE_AUTO_MIGRATE=false $env ./bin/aperture serve > "$OUT/$name.server.log" 2>&1 &
  APERTURE_PID=$!
  until curl -fs "http://127.0.0.1:$PORT/readyz" >/dev/null; do sleep 0.2; done
  # shellcheck disable=SC2086
  ./bin/loadgen -url "http://127.0.0.1:$PORT" -duration "$DURATION" -warmup 5s \
      -verify-db "$PG_URL" -label "$name" -json "$OUT/$name.json" $flags > /dev/null
  curl -fs "http://127.0.0.1:$PORT/metrics" | grep -E '^aperture_pipeline_(batch_spans|batch_requests|flush_duration_seconds)_(sum|count)|^aperture_pipeline_overload' > "$OUT/$name.metrics.txt" || true
  stop_aperture
}

# 1. Worker scaling: 16 clients, 100 spans per request.
for w in 1 2 4 8; do
  scenario "workers-$w" "APERTURE_WORKERS=$w" "-clients 16 -spans 100"
done

# 2. Request size, 4 workers.
for s in 10 1000; do
  scenario "spans-$s" "APERTURE_WORKERS=4" "-clients 16 -spans $s"
done

# 3. Batching: small requests with and without cross-request coalescing.
scenario "small-nobatch" "APERTURE_WORKERS=4 APERTURE_BATCH_SIZE=1" "-clients 32 -spans 10"
scenario "small-batch"   "APERTURE_WORKERS=4 APERTURE_BATCH_SIZE=2000 APERTURE_FLUSH_INTERVAL=5ms" "-clients 32 -spans 10"

# 4. Overload: tiny queue, one worker, many clients. Expect fast 503s and no loss.
scenario "overload" "APERTURE_WORKERS=1 APERTURE_QUEUE_CAPACITY=4" "-clients 64 -spans 100"

./bin/benchreport "$OUT" > "$OUT/report.md"
echo "report: $OUT/report.md"
