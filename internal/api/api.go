// Package api serves Aperture's read-only JSON query API used by the web UI.
//
// All times are returned as RFC 3339 strings with nanosecond precision, and
// raw nanosecond timestamps as decimal strings, because JavaScript numbers
// cannot represent int64 nanoseconds exactly. Durations are float
// milliseconds, which are exact enough for display and fit in a JS number.
package api

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mudarcode/aperture/internal/store/postgres"
)

// Store is the subset of the PostgreSQL store the API uses.
type Store interface {
	SearchTraces(context.Context, postgres.TraceQuery) ([]postgres.TraceSummary, error)
	GetTrace(context.Context, []byte) ([]postgres.SpanRow, error)
	ListServices(context.Context, postgres.TimeRange) ([]postgres.ServiceStats, error)
	ListOperations(context.Context, string, postgres.TimeRange) ([]postgres.OperationStats, error)
	ServiceTimeSeries(context.Context, string, postgres.TimeRange, time.Duration) ([]postgres.TimeBucket, error)
	Dependencies(context.Context, postgres.TimeRange) ([]postgres.DependencyEdge, error)
}

const (
	defaultWindow = time.Hour
	maxWindow     = 7 * 24 * time.Hour
	defaultLimit  = 50
	maxLimit      = 500
	queryTimeout  = 15 * time.Second
	maxBuckets    = 720
)

// API serves the query endpoints.
type API struct {
	store Store
	log   *slog.Logger
	now   func() time.Time
}

// New creates an API.
func New(store Store, log *slog.Logger) *API {
	return &API{store: store, log: log, now: time.Now}
}

// Register adds the API routes to mux.
func (a *API) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/traces", a.searchTraces)
	mux.HandleFunc("GET /api/v1/traces/{traceID}", a.getTrace)
	mux.HandleFunc("GET /api/v1/services", a.listServices)
	mux.HandleFunc("GET /api/v1/services/{service}/operations", a.listOperations)
	mux.HandleFunc("GET /api/v1/services/{service}/timeseries", a.timeSeries)
	mux.HandleFunc("GET /api/v1/dependencies", a.dependencies)
}

// ---- Response types ----

// LatencyStats is shared by several responses.
type LatencyStats struct {
	Count      int64   `json:"count"`
	ErrorCount int64   `json:"errorCount"`
	ErrorRate  float64 `json:"errorRate"`
	P50Ms      float64 `json:"p50Ms"`
	P95Ms      float64 `json:"p95Ms"`
	P99Ms      float64 `json:"p99Ms"`
}

func latency(s postgres.LatencyStats) LatencyStats {
	out := LatencyStats{Count: s.Count, ErrorCount: s.ErrorCount, P50Ms: s.P50Nano / 1e6, P95Ms: s.P95Nano / 1e6, P99Ms: s.P99Nano / 1e6}
	if s.Count > 0 {
		out.ErrorRate = float64(s.ErrorCount) / float64(s.Count)
	}
	return out
}

// TraceSummary is a trace search result.
type TraceSummary struct {
	TraceID           string   `json:"traceId"`
	StartTime         string   `json:"startTime"`
	StartTimeUnixNano string   `json:"startTimeUnixNano"`
	DurationMs        float64  `json:"durationMs"`
	SpanCount         int      `json:"spanCount"`
	ErrorCount        int      `json:"errorCount"`
	Services          []string `json:"services"`
	RootService       *string  `json:"rootService"`
	RootName          *string  `json:"rootName"`
}

// TraceList is the search response.
type TraceList struct {
	Traces     []TraceSummary `json:"traces"`
	NextCursor string         `json:"nextCursor,omitempty"`
}

// Span is one span in a trace detail response.
type Span struct {
	SpanID             string          `json:"spanId"`
	ParentSpanID       *string         `json:"parentSpanId"`
	ServiceName        string          `json:"serviceName"`
	Name               string          `json:"name"`
	Kind               string          `json:"kind"`
	StartTime          string          `json:"startTime"`
	StartTimeUnixNano  string          `json:"startTimeUnixNano"`
	EndTimeUnixNano    string          `json:"endTimeUnixNano"`
	StartOffsetMs      float64         `json:"startOffsetMs"` // relative to trace start
	DurationMs         float64         `json:"durationMs"`
	StatusCode         string          `json:"statusCode"`
	StatusMessage      string          `json:"statusMessage,omitempty"`
	TraceState         string          `json:"traceState,omitempty"`
	ScopeName          string          `json:"scopeName,omitempty"`
	ScopeVersion       string          `json:"scopeVersion,omitempty"`
	Attributes         json.RawMessage `json:"attributes"`
	ResourceAttributes json.RawMessage `json:"resourceAttributes"`
	Events             []Event         `json:"events"`
	Links              json.RawMessage `json:"links"`
	DroppedAttributes  int32           `json:"droppedAttributesCount"`
	DroppedEvents      int32           `json:"droppedEventsCount"`
	DroppedLinks       int32           `json:"droppedLinksCount"`
}

// Event is a span event with its offset from the trace start.
type Event struct {
	Name          string          `json:"name"`
	TimeUnixNano  string          `json:"timeUnixNano"`
	StartOffsetMs float64         `json:"startOffsetMs"`
	Attributes    json.RawMessage `json:"attributes"`
}

// Trace is the trace detail response.
type Trace struct {
	TraceID           string   `json:"traceId"`
	StartTime         string   `json:"startTime"`
	StartTimeUnixNano string   `json:"startTimeUnixNano"`
	DurationMs        float64  `json:"durationMs"`
	SpanCount         int      `json:"spanCount"`
	ErrorCount        int      `json:"errorCount"`
	Services          []string `json:"services"`
	Spans             []Span   `json:"spans"`
}

// Service is a service overview row.
type Service struct {
	Name      string `json:"name"`
	SpanCount int64  `json:"spanCount"`
	LatencyStats
}

// Operation is a per-operation statistics row.
type Operation struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	LatencyStats
}

// Bucket is a time series point.
type Bucket struct {
	Time string `json:"time"`
	LatencyStats
}

// TimeSeries is the time series response.
type TimeSeries struct {
	Step    string   `json:"step"`
	Buckets []Bucket `json:"buckets"`
}

// DependencyGraph is the service map response.
type DependencyGraph struct {
	Nodes []DependencyNode `json:"nodes"`
	Edges []DependencyEdge `json:"edges"`
}

// DependencyNode is a service in the graph.
type DependencyNode struct {
	ID string `json:"id"`
}

// DependencyEdge is a parent→child call relationship.
type DependencyEdge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	LatencyStats
}

// ---- Handlers ----

func (a *API) searchTraces(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, err := a.timeRange(q.Get("start"), q.Get("end"), q.Get("window"))
	if err != nil {
		badRequest(w, err)
		return
	}
	tq := postgres.TraceQuery{
		Range:      rng,
		Service:    q.Get("service"),
		Operation:  q.Get("operation"),
		ErrorsOnly: q.Get("status") == "error",
	}
	if s := q.Get("status"); s != "" && s != "error" && s != "any" {
		badRequest(w, fmt.Errorf("status must be error or any"))
		return
	}
	if tq.Operation != "" && tq.Service == "" {
		badRequest(w, errors.New("operation requires service"))
		return
	}
	if tq.MinDuration, err = msParam(q.Get("minDurationMs")); err != nil {
		badRequest(w, fmt.Errorf("minDurationMs: %w", err))
		return
	}
	if tq.MaxDuration, err = msParam(q.Get("maxDurationMs")); err != nil {
		badRequest(w, fmt.Errorf("maxDurationMs: %w", err))
		return
	}
	if tq.MaxDuration > 0 && tq.MaxDuration < tq.MinDuration {
		badRequest(w, errors.New("maxDurationMs must be >= minDurationMs"))
		return
	}
	if tq.Limit, err = intParam(q.Get("limit"), defaultLimit, 1, maxLimit); err != nil {
		badRequest(w, fmt.Errorf("limit: %w", err))
		return
	}
	if c := q.Get("cursor"); c != "" {
		if tq.AfterStartNano, tq.AfterTraceID, err = decodeCursor(c); err != nil {
			badRequest(w, errors.New("invalid cursor"))
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.SearchTraces(ctx, tq)
	if err != nil {
		a.internalError(w, err)
		return
	}
	out := TraceList{Traces: make([]TraceSummary, len(rows))}
	for i, t := range rows {
		out.Traces[i] = TraceSummary{
			TraceID:           hex.EncodeToString(t.TraceID),
			StartTime:         formatNano(t.StartTimeUnixNano),
			StartTimeUnixNano: strconv.FormatInt(t.StartTimeUnixNano, 10),
			DurationMs:        float64(t.DurationNano) / 1e6,
			SpanCount:         t.SpanCount,
			ErrorCount:        t.ErrorCount,
			Services:          t.Services,
			RootService:       t.RootServiceName,
			RootName:          t.RootSpanName,
		}
	}
	if len(rows) == tq.Limit {
		last := rows[len(rows)-1]
		out.NextCursor = encodeCursor(last.StartTimeUnixNano, last.TraceID)
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) getTrace(w http.ResponseWriter, r *http.Request) {
	id, err := hex.DecodeString(r.PathValue("traceID"))
	if err != nil || len(id) != 16 {
		badRequest(w, errors.New("trace ID must be 32 hex characters"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.GetTrace(ctx, id)
	if errors.Is(err, postgres.ErrNotFound) {
		writeJSON(w, http.StatusNotFound, errorBody{"trace not found"})
		return
	}
	if err != nil {
		a.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, buildTrace(rows))
}

func buildTrace(rows []postgres.SpanRow) Trace {
	start, end := rows[0].StartTimeUnixNano, rows[0].EndTimeUnixNano
	for _, s := range rows {
		start = min(start, s.StartTimeUnixNano)
		end = max(end, s.EndTimeUnixNano)
	}
	t := Trace{
		TraceID:           hex.EncodeToString(rows[0].TraceID),
		StartTime:         formatNano(start),
		StartTimeUnixNano: strconv.FormatInt(start, 10),
		DurationMs:        float64(end-start) / 1e6,
		SpanCount:         len(rows),
		Spans:             make([]Span, len(rows)),
	}
	seen := map[string]bool{}
	for i, s := range rows {
		if s.StatusCode == 2 {
			t.ErrorCount++
		}
		if !seen[s.ServiceName] {
			seen[s.ServiceName] = true
			t.Services = append(t.Services, s.ServiceName)
		}
		var parent *string
		if s.ParentSpanID != nil {
			p := hex.EncodeToString(s.ParentSpanID)
			parent = &p
		}
		t.Spans[i] = Span{
			SpanID:             hex.EncodeToString(s.SpanID),
			ParentSpanID:       parent,
			ServiceName:        s.ServiceName,
			Name:               s.Name,
			Kind:               kindName(s.Kind),
			StartTime:          formatNano(s.StartTimeUnixNano),
			StartTimeUnixNano:  strconv.FormatInt(s.StartTimeUnixNano, 10),
			EndTimeUnixNano:    strconv.FormatInt(s.EndTimeUnixNano, 10),
			StartOffsetMs:      float64(s.StartTimeUnixNano-start) / 1e6,
			DurationMs:         float64(s.EndTimeUnixNano-s.StartTimeUnixNano) / 1e6,
			StatusCode:         statusName(s.StatusCode),
			StatusMessage:      s.StatusMessage,
			TraceState:         s.TraceState,
			ScopeName:          s.ScopeName,
			ScopeVersion:       s.ScopeVersion,
			Attributes:         s.Attributes,
			ResourceAttributes: s.ResourceAttributes,
			Events:             buildEvents(s.Events, start),
			Links:              s.Links,
			DroppedAttributes:  s.DroppedAttributes,
			DroppedEvents:      s.DroppedEvents,
			DroppedLinks:       s.DroppedLinks,
		}
	}
	return t
}

func buildEvents(raw json.RawMessage, traceStart int64) []Event {
	var stored []struct {
		Name         string          `json:"name"`
		TimeUnixNano json.Number     `json:"time_unix_nano"`
		Attributes   json.RawMessage `json:"attributes"`
	}
	out := []Event{}
	if err := json.Unmarshal(raw, &stored); err != nil {
		return out
	}
	for _, e := range stored {
		ns, _ := strconv.ParseInt(e.TimeUnixNano.String(), 10, 64)
		attrs := e.Attributes
		if len(attrs) == 0 {
			attrs = json.RawMessage(`{}`)
		}
		out = append(out, Event{Name: e.Name, TimeUnixNano: e.TimeUnixNano.String(),
			StartOffsetMs: float64(ns-traceStart) / 1e6, Attributes: attrs})
	}
	return out
}

func (a *API) listServices(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, err := a.timeRange(q.Get("start"), q.Get("end"), q.Get("window"))
	if err != nil {
		badRequest(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.ListServices(ctx, rng)
	if err != nil {
		a.internalError(w, err)
		return
	}
	out := make([]Service, len(rows))
	for i, s := range rows {
		out[i] = Service{Name: s.Name, SpanCount: s.SpanCount, LatencyStats: latency(s.LatencyStats)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

func (a *API) listOperations(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, err := a.timeRange(q.Get("start"), q.Get("end"), q.Get("window"))
	if err != nil {
		badRequest(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.ListOperations(ctx, r.PathValue("service"), rng)
	if err != nil {
		a.internalError(w, err)
		return
	}
	out := make([]Operation, len(rows))
	for i, o := range rows {
		out[i] = Operation{Name: o.Name, Kind: kindName(o.Kind), LatencyStats: latency(o.LatencyStats)}
	}
	writeJSON(w, http.StatusOK, map[string]any{"operations": out})
}

func (a *API) timeSeries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, err := a.timeRange(q.Get("start"), q.Get("end"), q.Get("window"))
	if err != nil {
		badRequest(w, err)
		return
	}
	span := rng.End.Sub(rng.Start)
	step := span / 60
	if s := q.Get("step"); s != "" {
		if step, err = time.ParseDuration(s); err != nil || step <= 0 {
			badRequest(w, errors.New("step must be a positive duration such as 30s or 1m"))
			return
		}
	}
	step = max(step.Truncate(time.Second), time.Second)
	if span/step > maxBuckets {
		badRequest(w, fmt.Errorf("step too small: at most %d buckets per request", maxBuckets))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.ServiceTimeSeries(ctx, r.PathValue("service"), rng, step)
	if err != nil {
		a.internalError(w, err)
		return
	}
	out := TimeSeries{Step: step.String(), Buckets: make([]Bucket, len(rows))}
	for i, b := range rows {
		out.Buckets[i] = Bucket{Time: b.Time.UTC().Format(time.RFC3339), LatencyStats: latency(b.LatencyStats)}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) dependencies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	rng, err := a.timeRange(q.Get("start"), q.Get("end"), q.Get("window"))
	if err != nil {
		badRequest(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	rows, err := a.store.Dependencies(ctx, rng)
	if err != nil {
		a.internalError(w, err)
		return
	}
	out := DependencyGraph{Nodes: []DependencyNode{}, Edges: make([]DependencyEdge, len(rows))}
	seen := map[string]bool{}
	for i, e := range rows {
		out.Edges[i] = DependencyEdge{Source: e.Parent, Target: e.Child, LatencyStats: latency(e.LatencyStats)}
		for _, n := range []string{e.Parent, e.Child} {
			if !seen[n] {
				seen[n] = true
				out.Nodes = append(out.Nodes, DependencyNode{ID: n})
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ---- Parameter parsing ----

// timeRange resolves start/end (RFC 3339) or a lookback window ending now.
func (a *API) timeRange(startS, endS, windowS string) (postgres.TimeRange, error) {
	end := a.now()
	if endS != "" {
		t, err := time.Parse(time.RFC3339Nano, endS)
		if err != nil {
			return postgres.TimeRange{}, errors.New("end must be RFC 3339")
		}
		end = t
	}
	var start time.Time
	switch {
	case startS != "" && windowS != "":
		return postgres.TimeRange{}, errors.New("use either start or window, not both")
	case startS != "":
		t, err := time.Parse(time.RFC3339Nano, startS)
		if err != nil {
			return postgres.TimeRange{}, errors.New("start must be RFC 3339")
		}
		start = t
	default:
		w := defaultWindow
		if windowS != "" {
			d, err := parseWindow(windowS)
			if err != nil {
				return postgres.TimeRange{}, err
			}
			w = d
		}
		start = end.Add(-w)
	}
	if !start.Before(end) {
		return postgres.TimeRange{}, errors.New("start must be before end")
	}
	if end.Sub(start) > maxWindow {
		return postgres.TimeRange{}, fmt.Errorf("time range must not exceed %s", maxWindow)
	}
	return postgres.TimeRange{Start: start, End: end}, nil
}

// parseWindow accepts Go durations plus a "d" (day) suffix, e.g. 15m, 6h, 7d.
func parseWindow(s string) (time.Duration, error) {
	if n, ok := strings.CutSuffix(s, "d"); ok {
		days, err := strconv.Atoi(n)
		if err != nil || days <= 0 {
			return 0, errors.New("window must be a positive duration such as 15m, 1h, or 7d")
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil || d <= 0 {
		return 0, errors.New("window must be a positive duration such as 15m, 1h, or 7d")
	}
	return d, nil
}

func msParam(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f < 0 || f > float64(maxWindow/time.Millisecond) {
		return 0, errors.New("must be a non-negative number of milliseconds")
	}
	return time.Duration(f * float64(time.Millisecond)), nil
}

func intParam(s string, def, lo, hi int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("must be an integer in [%d, %d]", lo, hi)
	}
	return n, nil
}

// Cursor: base64url(8-byte big-endian start ns || 16-byte trace ID).
func encodeCursor(startNano int64, traceID []byte) string {
	b := make([]byte, 8, 24)
	binary.BigEndian.PutUint64(b, uint64(startNano))
	return base64.RawURLEncoding.EncodeToString(append(b, traceID...))
}

func decodeCursor(s string) (int64, []byte, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(b) != 24 {
		return 0, nil, errors.New("invalid cursor")
	}
	return int64(binary.BigEndian.Uint64(b[:8])), b[8:], nil
}

// ---- Helpers ----

var kindNames = [...]string{"unspecified", "internal", "server", "client", "producer", "consumer"}

func kindName(k int16) string {
	if k >= 0 && int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unspecified"
}

func statusName(c int16) string {
	switch c {
	case 1:
		return "ok"
	case 2:
		return "error"
	default:
		return "unset"
	}
}

func formatNano(ns int64) string { return time.Unix(0, ns).UTC().Format(time.RFC3339Nano) }

type errorBody struct {
	Error string `json:"error"`
}

func badRequest(w http.ResponseWriter, err error) {
	writeJSON(w, http.StatusBadRequest, errorBody{err.Error()})
}

func (a *API) internalError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.DeadlineExceeded) {
		writeJSON(w, http.StatusGatewayTimeout, errorBody{"query timed out; narrow the time range"})
		return
	}
	a.log.Error("query failed", "err", err)
	writeJSON(w, http.StatusInternalServerError, errorBody{"internal error"})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
