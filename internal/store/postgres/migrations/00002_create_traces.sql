-- +goose Up
-- Per-trace summary maintained in the same transaction as span inserts.
-- Trace search reads this table instead of aggregating spans on every query.
CREATE TABLE traces (
    trace_id             bytea       PRIMARY KEY CHECK (octet_length(trace_id) = 16),
    start_time_unix_nano bigint      NOT NULL,
    end_time_unix_nano  bigint      NOT NULL,
    duration_nano        bigint      NOT NULL GENERATED ALWAYS AS (end_time_unix_nano - start_time_unix_nano) STORED,
    start_time           timestamptz NOT NULL GENERATED ALWAYS AS (to_timestamp(start_time_unix_nano / 1000000000.0)) STORED,
    span_count           integer     NOT NULL,
    error_count          integer     NOT NULL,
    services             text[]      NOT NULL,
    -- Root span fields; NULL until the root span arrives (spans of one trace
    -- can arrive in any order and in different requests).
    root_service_name    text,
    root_span_name       text,
    updated_at           timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX traces_start_time_idx ON traces (start_time DESC, trace_id);
CREATE INDEX traces_services_idx ON traces USING gin (services);
CREATE INDEX traces_errors_idx ON traces (start_time DESC) WHERE error_count > 0;

-- Parent/child joins for the service dependency graph.
CREATE INDEX spans_parent_idx ON spans (trace_id, parent_span_id) WHERE parent_span_id IS NOT NULL;
-- Operation statistics per service.
CREATE INDEX spans_service_name_start_idx ON spans (service_name, name, start_time DESC);

-- Backfill from spans ingested before this migration.
INSERT INTO traces (trace_id, start_time_unix_nano, end_time_unix_nano, span_count, error_count,
                    services, root_service_name, root_span_name)
SELECT trace_id,
       min(start_time_unix_nano),
       max(end_time_unix_nano),
       count(*),
       count(*) FILTER (WHERE status_code = 2),
       array_agg(DISTINCT service_name ORDER BY service_name),
       (array_agg(service_name ORDER BY start_time_unix_nano) FILTER (WHERE parent_span_id IS NULL))[1],
       (array_agg(name ORDER BY start_time_unix_nano) FILTER (WHERE parent_span_id IS NULL))[1]
FROM spans
GROUP BY trace_id;

-- +goose Down
DROP INDEX spans_service_name_start_idx;
DROP INDEX spans_parent_idx;
DROP TABLE traces;
