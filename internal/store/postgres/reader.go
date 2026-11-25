package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound is returned when a trace does not exist.
var ErrNotFound = errors.New("not found")

// TimeRange is a closed-open interval [Start, End).
type TimeRange struct {
	Start, End time.Time
}

// TraceQuery filters trace search.
type TraceQuery struct {
	Range       TimeRange
	Service     string // trace includes a span from this service
	Operation   string // ... with this span name (requires Service)
	ErrorsOnly  bool
	MinDuration time.Duration
	MaxDuration time.Duration // 0 = unbounded
	Limit       int
	// Keyset pagination: return traces strictly older than this position.
	AfterStartNano int64
	AfterTraceID   []byte
}

// TraceSummary is one row of trace search results.
type TraceSummary struct {
	TraceID           []byte
	StartTimeUnixNano int64
	DurationNano      int64
	SpanCount         int
	ErrorCount        int
	Services          []string
	RootServiceName   *string
	RootSpanName      *string
}

// SearchTraces returns traces in Range, newest first.
func (s *Store) SearchTraces(ctx context.Context, q TraceQuery) ([]TraceSummary, error) {
	args := pgx.NamedArgs{
		"start": q.Range.Start, "end": q.Range.End, "limit": q.Limit,
		"min_dur": q.MinDuration.Nanoseconds(),
	}
	sql := `
SELECT t.trace_id, t.start_time_unix_nano, t.duration_nano, t.span_count, t.error_count,
       t.services, t.root_service_name, t.root_span_name
FROM traces t
WHERE t.start_time >= @start AND t.start_time < @end
  AND t.duration_nano >= @min_dur`
	if q.MaxDuration > 0 {
		sql += ` AND t.duration_nano <= @max_dur`
		args["max_dur"] = q.MaxDuration.Nanoseconds()
	}
	if q.ErrorsOnly {
		sql += ` AND t.error_count > 0`
	}
	if q.Service != "" {
		sql += ` AND t.services @> ARRAY[@service::text]`
		args["service"] = q.Service
		if q.Operation != "" {
			sql += ` AND EXISTS (SELECT 1 FROM spans s WHERE s.trace_id = t.trace_id
			                     AND s.service_name = @service AND s.name = @operation)`
			args["operation"] = q.Operation
		}
	}
	if q.AfterTraceID != nil {
		sql += ` AND (t.start_time_unix_nano, t.trace_id) < (@after_start, @after_id)`
		args["after_start"], args["after_id"] = q.AfterStartNano, q.AfterTraceID
	}
	sql += ` ORDER BY t.start_time DESC, t.start_time_unix_nano DESC, t.trace_id DESC LIMIT @limit`

	rows, err := s.pool.Query(ctx, sql, args)
	if err != nil {
		return nil, fmt.Errorf("search traces: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (TraceSummary, error) {
		var t TraceSummary
		err := r.Scan(&t.TraceID, &t.StartTimeUnixNano, &t.DurationNano, &t.SpanCount, &t.ErrorCount,
			&t.Services, &t.RootServiceName, &t.RootSpanName)
		return t, err
	})
}

// SpanRow is a fully materialized stored span.
type SpanRow struct {
	TraceID, SpanID, ParentSpanID          []byte
	TraceState, ServiceName, Name          string
	Kind, StatusCode                       int16
	StartTimeUnixNano, EndTimeUnixNano     int64
	StatusMessage, ScopeName, ScopeVersion string
	ResourceAttributes, Attributes         json.RawMessage
	Events, Links                          json.RawMessage
	DroppedAttributes, DroppedEvents       int32
	DroppedLinks                           int32
}

// GetTrace returns every span of a trace ordered by start time.
func (s *Store) GetTrace(ctx context.Context, traceID []byte) ([]SpanRow, error) {
	rows, err := s.pool.Query(ctx, `
SELECT trace_id, span_id, parent_span_id, trace_state, service_name, name, kind, status_code,
       start_time_unix_nano, end_time_unix_nano, status_message, scope_name, scope_version,
       resource_attributes, attributes, events, links,
       dropped_attributes_count, dropped_events_count, dropped_links_count
FROM spans WHERE trace_id = $1
ORDER BY start_time_unix_nano, span_id`, traceID)
	if err != nil {
		return nil, fmt.Errorf("get trace: %w", err)
	}
	spans, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (SpanRow, error) {
		var sp SpanRow
		err := r.Scan(&sp.TraceID, &sp.SpanID, &sp.ParentSpanID, &sp.TraceState, &sp.ServiceName, &sp.Name,
			&sp.Kind, &sp.StatusCode, &sp.StartTimeUnixNano, &sp.EndTimeUnixNano, &sp.StatusMessage,
			&sp.ScopeName, &sp.ScopeVersion, &sp.ResourceAttributes, &sp.Attributes, &sp.Events, &sp.Links,
			&sp.DroppedAttributes, &sp.DroppedEvents, &sp.DroppedLinks)
		return sp, err
	})
	if err != nil {
		return nil, fmt.Errorf("get trace: %w", err)
	}
	if len(spans) == 0 {
		return nil, ErrNotFound
	}
	return spans, nil
}

// LatencyStats summarizes a set of spans.
type LatencyStats struct {
	Count      int64
	ErrorCount int64
	P50Nano    float64
	P95Nano    float64
	P99Nano    float64
}

// ServiceStats is per-service RED data for entry spans (server/consumer
// spans, or root spans), which represent requests the service handled.
type ServiceStats struct {
	Name      string
	SpanCount int64
	LatencyStats
}

// entrySpan selects spans that represent work a service was asked to do.
const entrySpan = `(kind IN (2, 5) OR parent_span_id IS NULL)`

// ListServices returns every service seen in the range with RED statistics.
func (s *Store) ListServices(ctx context.Context, r TimeRange) ([]ServiceStats, error) {
	rows, err := s.pool.Query(ctx, `
SELECT service_name,
       count(*) AS spans,
       count(*) FILTER (WHERE `+entrySpan+`) AS requests,
       count(*) FILTER (WHERE `+entrySpan+` AND status_code = 2) AS errors,
       coalesce(percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_nano) FILTER (WHERE `+entrySpan+`), 0),
       coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_nano) FILTER (WHERE `+entrySpan+`), 0),
       coalesce(percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_nano) FILTER (WHERE `+entrySpan+`), 0)
FROM spans
WHERE start_time >= $1 AND start_time < $2
GROUP BY service_name
ORDER BY service_name`, r.Start, r.End)
	if err != nil {
		return nil, fmt.Errorf("list services: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (ServiceStats, error) {
		var st ServiceStats
		err := row.Scan(&st.Name, &st.SpanCount, &st.Count, &st.ErrorCount, &st.P50Nano, &st.P95Nano, &st.P99Nano)
		return st, err
	})
}

// OperationStats is per-(service, span name) latency data.
type OperationStats struct {
	Name string
	Kind int16
	LatencyStats
}

// ListOperations returns statistics for each span name of a service.
func (s *Store) ListOperations(ctx context.Context, service string, r TimeRange) ([]OperationStats, error) {
	rows, err := s.pool.Query(ctx, `
SELECT name, mode() WITHIN GROUP (ORDER BY kind),
       count(*), count(*) FILTER (WHERE status_code = 2),
       percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_nano),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_nano),
       percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_nano)
FROM spans
WHERE service_name = $1 AND start_time >= $2 AND start_time < $3
GROUP BY name
ORDER BY count(*) DESC, name
LIMIT 500`, service, r.Start, r.End)
	if err != nil {
		return nil, fmt.Errorf("list operations: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (OperationStats, error) {
		var o OperationStats
		err := row.Scan(&o.Name, &o.Kind, &o.Count, &o.ErrorCount, &o.P50Nano, &o.P95Nano, &o.P99Nano)
		return o, err
	})
}

// TimeBucket is one point of a service time series.
type TimeBucket struct {
	Time time.Time
	LatencyStats
}

// ServiceTimeSeries buckets a service's entry spans by step.
func (s *Store) ServiceTimeSeries(ctx context.Context, service string, r TimeRange, step time.Duration) ([]TimeBucket, error) {
	rows, err := s.pool.Query(ctx, `
SELECT date_bin($4::interval, start_time, $2) AS bucket,
       count(*), count(*) FILTER (WHERE status_code = 2),
       percentile_cont(0.50) WITHIN GROUP (ORDER BY duration_nano),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY duration_nano),
       percentile_cont(0.99) WITHIN GROUP (ORDER BY duration_nano)
FROM spans
WHERE service_name = $1 AND start_time >= $2 AND start_time < $3 AND `+entrySpan+`
GROUP BY bucket
ORDER BY bucket`, service, r.Start, r.End, step)
	if err != nil {
		return nil, fmt.Errorf("service time series: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (TimeBucket, error) {
		var b TimeBucket
		err := row.Scan(&b.Time, &b.Count, &b.ErrorCount, &b.P50Nano, &b.P95Nano, &b.P99Nano)
		return b, err
	})
}

// DependencyEdge is an observed call from one service to another, derived
// from parent/child span pairs that cross a service boundary.
type DependencyEdge struct {
	Parent, Child string
	LatencyStats  // of the child spans
}

// Dependencies returns the service call graph for the range.
func (s *Store) Dependencies(ctx context.Context, r TimeRange) ([]DependencyEdge, error) {
	rows, err := s.pool.Query(ctx, `
SELECT p.service_name, c.service_name,
       count(*), count(*) FILTER (WHERE c.status_code = 2),
       percentile_cont(0.50) WITHIN GROUP (ORDER BY c.duration_nano),
       percentile_cont(0.95) WITHIN GROUP (ORDER BY c.duration_nano),
       percentile_cont(0.99) WITHIN GROUP (ORDER BY c.duration_nano)
FROM spans c
JOIN spans p ON p.trace_id = c.trace_id AND p.span_id = c.parent_span_id
WHERE c.start_time >= $1 AND c.start_time < $2
  AND c.parent_span_id IS NOT NULL
  AND p.service_name <> c.service_name
GROUP BY 1, 2
ORDER BY 1, 2`, r.Start, r.End)
	if err != nil {
		return nil, fmt.Errorf("dependencies: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (DependencyEdge, error) {
		var e DependencyEdge
		err := row.Scan(&e.Parent, &e.Child, &e.Count, &e.ErrorCount, &e.P50Nano, &e.P95Nano, &e.P99Nano)
		return e, err
	})
}
