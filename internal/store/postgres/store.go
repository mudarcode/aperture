// Package postgres implements span persistence on PostgreSQL.
package postgres

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/mudarcode/aperture/internal/model"
	"github.com/mudarcode/aperture/internal/pipeline"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Store writes spans to PostgreSQL.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to PostgreSQL and verifies connectivity.
func Open(ctx context.Context, dsn string, maxConns int32) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse database url: %w", err)
	}
	if maxConns > 0 {
		cfg.MaxConns = maxConns
	}
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping database: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }

// Pool exposes the underlying pool (used by tests and read APIs).
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

// Ping checks database reachability; used by the readiness probe.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Migrate applies all pending migrations.
func (s *Store) Migrate(ctx context.Context) error {
	db := stdlib.OpenDBFromPool(s.pool)
	defer db.Close()
	return migrate(ctx, db)
}

func migrate(ctx context.Context, db *sql.DB) error {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, mustSub(migrationsFS, "migrations"))
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// insertSQL inserts a whole batch with a single statement using parallel
// arrays + unnest, which avoids PostgreSQL's 65535 bind-parameter limit and
// costs one round trip regardless of batch size. ON CONFLICT DO NOTHING makes
// client retries idempotent.
const insertSQL = `
INSERT INTO spans (
    trace_id, span_id, parent_span_id, trace_state, service_name, name, kind,
    start_time_unix_nano, end_time_unix_nano, status_code, status_message,
    scope_name, scope_version, resource_attributes, attributes, events, links,
    dropped_attributes_count, dropped_events_count, dropped_links_count
)
SELECT t.trace_id, t.span_id, t.parent_span_id, t.trace_state, t.service_name, t.name, t.kind,
       t.start_ns, t.end_ns, t.status_code, t.status_message,
       t.scope_name, t.scope_version, t.resource_attributes::jsonb, t.attributes::jsonb,
       t.events::jsonb, t.links::jsonb,
       t.dropped_attributes, t.dropped_events, t.dropped_links
FROM unnest(
    $1::bytea[], $2::bytea[], $3::bytea[], $4::text[], $5::text[], $6::text[], $7::smallint[],
    $8::bigint[], $9::bigint[], $10::smallint[], $11::text[],
    $12::text[], $13::text[], $14::text[], $15::text[], $16::text[], $17::text[],
    $18::integer[], $19::integer[], $20::integer[]
) AS t(trace_id, span_id, parent_span_id, trace_state, service_name, name, kind,
       start_ns, end_ns, status_code, status_message,
       scope_name, scope_version, resource_attributes, attributes, events, links,
       dropped_attributes, dropped_events, dropped_links)
ON CONFLICT (trace_id, span_id) DO NOTHING`

// WriteSpans inserts spans in one transaction. It implements pipeline.Writer.
// Data errors (SQLSTATE classes 22 and 23) are wrapped with
// pipeline.ErrPermanent because retrying identical data cannot succeed.
func (s *Store) WriteSpans(ctx context.Context, spans []model.Span) error {
	n := len(spans)
	var (
		traceIDs, spanIDs, parentIDs              = make([][]byte, n), make([][]byte, n), make([][]byte, n)
		traceStates, services, names              = make([]string, n), make([]string, n), make([]string, n)
		kinds, statusCodes                        = make([]int16, n), make([]int16, n)
		starts, ends                              = make([]int64, n), make([]int64, n)
		statusMsgs, scopeNames, scopeVersions     = make([]string, n), make([]string, n), make([]string, n)
		resAttrs, attrs, events, links            = make([]string, n), make([]string, n), make([]string, n), make([]string, n)
		droppedAttrs, droppedEvents, droppedLinks = make([]int32, n), make([]int32, n), make([]int32, n)
	)
	for i := range spans {
		sp := &spans[i]
		traceIDs[i] = sp.TraceID[:]
		spanIDs[i] = sp.SpanID[:]
		if sp.ParentSpanID != nil {
			parentIDs[i] = sp.ParentSpanID[:]
		}
		traceStates[i] = sp.TraceState
		services[i] = sp.ServiceName
		names[i] = sp.Name
		kinds[i] = sp.Kind
		starts[i] = int64(sp.StartTimeUnixNano) // validated <= MaxInt64 at ingestion
		ends[i] = int64(sp.EndTimeUnixNano)
		statusCodes[i] = sp.StatusCode
		statusMsgs[i] = sp.StatusMessage
		scopeNames[i] = sp.ScopeName
		scopeVersions[i] = sp.ScopeVersion
		resAttrs[i] = jsonOr(sp.ResourceAttributes, "{}")
		attrs[i] = jsonOr(sp.Attributes, "{}")
		events[i] = jsonOr(sp.Events, "[]")
		links[i] = jsonOr(sp.Links, "[]")
		droppedAttrs[i] = clampInt32(sp.DroppedAttributesCount)
		droppedEvents[i] = clampInt32(sp.DroppedEventsCount)
		droppedLinks[i] = clampInt32(sp.DroppedLinksCount)
	}

	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, insertSQL,
			traceIDs, spanIDs, parentIDs, traceStates, services, names, kinds,
			starts, ends, statusCodes, statusMsgs,
			scopeNames, scopeVersions, resAttrs, attrs, events, links,
			droppedAttrs, droppedEvents, droppedLinks)
		return err
	})
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && len(pgErr.Code) == 5 && (pgErr.Code[:2] == "22" || pgErr.Code[:2] == "23") {
			return fmt.Errorf("insert spans: %w: %w", pipeline.ErrPermanent, err)
		}
		return fmt.Errorf("insert spans: %w", err)
	}
	return nil
}

func jsonOr(b []byte, def string) string {
	if len(b) == 0 {
		return def
	}
	return string(b)
}

func clampInt32(v uint32) int32 {
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	return int32(v)
}
