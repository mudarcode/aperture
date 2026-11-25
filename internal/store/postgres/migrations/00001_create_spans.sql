-- +goose Up
-- One row per span. Identifiers are stored as raw bytes (16/8 bytes) rather
-- than hex text to halve index size; the API layer renders them as hex.
CREATE TABLE spans (
    trace_id                 bytea       NOT NULL CHECK (octet_length(trace_id) = 16),
    span_id                  bytea       NOT NULL CHECK (octet_length(span_id) = 8),
    parent_span_id           bytea                CHECK (parent_span_id IS NULL OR octet_length(parent_span_id) = 8),
    trace_state              text        NOT NULL DEFAULT '',
    service_name             text        NOT NULL,
    name                     text        NOT NULL,
    kind                     smallint    NOT NULL CHECK (kind BETWEEN 0 AND 5),
    -- Nanosecond timestamps are kept exactly; start_time is a derived
    -- microsecond-precision column for range queries and indexing.
    start_time_unix_nano     bigint      NOT NULL,
    end_time_unix_nano       bigint      NOT NULL,
    duration_nano            bigint      NOT NULL GENERATED ALWAYS AS (end_time_unix_nano - start_time_unix_nano) STORED,
    start_time               timestamptz NOT NULL GENERATED ALWAYS AS (to_timestamp(start_time_unix_nano / 1000000000.0)) STORED,
    status_code              smallint    NOT NULL CHECK (status_code BETWEEN 0 AND 2),
    status_message           text        NOT NULL DEFAULT '',
    scope_name               text        NOT NULL DEFAULT '',
    scope_version            text        NOT NULL DEFAULT '',
    resource_attributes      jsonb       NOT NULL DEFAULT '{}',
    attributes               jsonb       NOT NULL DEFAULT '{}',
    events                   jsonb       NOT NULL DEFAULT '[]',
    links                    jsonb       NOT NULL DEFAULT '[]',
    dropped_attributes_count integer     NOT NULL DEFAULT 0,
    dropped_events_count     integer     NOT NULL DEFAULT 0,
    dropped_links_count      integer     NOT NULL DEFAULT 0,
    ingested_at              timestamptz NOT NULL DEFAULT now(),
    CHECK (end_time_unix_nano >= start_time_unix_nano),
    -- Primary key makes ingestion idempotent: OTLP clients retry on timeouts,
    -- and a retried span is ignored via ON CONFLICT DO NOTHING.
    PRIMARY KEY (trace_id, span_id)
);

-- Recent-activity and per-service searches.
CREATE INDEX spans_start_time_idx ON spans (start_time DESC);
CREATE INDEX spans_service_start_time_idx ON spans (service_name, start_time DESC);
-- Error drill-down without scanning successful spans.
CREATE INDEX spans_errors_idx ON spans (service_name, start_time DESC) WHERE status_code = 2;

-- +goose Down
DROP TABLE spans;
