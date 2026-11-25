package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/mudarcode/aperture/internal/model"
	"github.com/mudarcode/aperture/internal/store/postgres"
	"github.com/mudarcode/aperture/internal/testutil/pgtest"
)

type fixture struct {
	store *postgres.Store
	srv   *httptest.Server
	base  time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	s, err := postgres.Open(ctx, pgtest.NewDatabase(t), 8)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(s, slog.New(slog.NewTextHandler(io.Discard, nil))).Register(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &fixture{store: s, srv: srv, base: time.Now().Add(-10 * time.Minute).Truncate(time.Millisecond)}
}

type spanSpec struct {
	trace, id, parent byte
	service, name     string
	kind              int16
	offset, dur       time.Duration
	err               bool
}

func (f *fixture) mk(s spanSpec) model.Span {
	sp := model.Span{
		ServiceName:       s.service,
		Name:              s.name,
		Kind:              s.kind,
		StartTimeUnixNano: uint64(f.base.Add(s.offset).UnixNano()),
		EndTimeUnixNano:   uint64(f.base.Add(s.offset + s.dur).UnixNano()),
		Attributes:        []byte(`{"k":"v"}`),
		Events:            []byte(`[{"name":"ev","time_unix_nano":` + itoa(f.base.Add(s.offset).UnixNano()+1000) + `,"attributes":{}}]`),
	}
	sp.TraceID[15] = s.trace
	sp.SpanID[7] = s.id
	if s.parent != 0 {
		p := model.SpanID{}
		p[7] = s.parent
		sp.ParentSpanID = &p
	}
	if s.err {
		sp.StatusCode = 2
		sp.StatusMessage = "failed"
	}
	return sp
}

func itoa(n int64) string { b, _ := json.Marshal(n); return string(b) }

func traceHex(b byte) string {
	var id model.TraceID
	id[15] = b
	return hex.EncodeToString(id[:])
}

// seed writes two traces:
//
//	trace 1 (t=0):   frontend GET /checkout 100ms → checkout POST /pay 60ms (error) → payments charge 20ms
//	trace 2 (t=1s):  frontend GET /items 30ms → inventory list 10ms
func (f *fixture) seed(t *testing.T) {
	t.Helper()
	specs := []spanSpec{
		{1, 1, 0, "frontend", "GET /checkout", 2, 0, 100 * time.Millisecond, true},
		{1, 2, 1, "checkout", "POST /pay", 2, 10 * time.Millisecond, 60 * time.Millisecond, true},
		{1, 3, 2, "payments", "charge", 2, 20 * time.Millisecond, 20 * time.Millisecond, false},
		{2, 4, 0, "frontend", "GET /items", 2, time.Second, 30 * time.Millisecond, false},
		{2, 5, 4, "inventory", "list", 2, time.Second + 5*time.Millisecond, 10 * time.Millisecond, false},
	}
	// Write children before parents and in separate batches, as happens when
	// different services export independently.
	for i := len(specs) - 1; i >= 0; i-- {
		if err := f.store.WriteSpans(context.Background(), []model.Span{f.mk(specs[i])}); err != nil {
			t.Fatal(err)
		}
	}
}

func (f *fixture) get(t *testing.T, path string, want int, out any) {
	t.Helper()
	resp, err := http.Get(f.srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != want {
		t.Fatalf("GET %s = %d, want %d: %s", path, resp.StatusCode, want, body)
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
	}
}

func TestTraceSummariesFromOutOfOrderBatches(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	// Re-sending a span (client retry) must not double count.
	if err := f.store.WriteSpans(context.Background(), []model.Span{f.mk(spanSpec{1, 3, 2, "payments", "charge", 2, 20 * time.Millisecond, 20 * time.Millisecond, false})}); err != nil {
		t.Fatal(err)
	}

	var list TraceList
	f.get(t, "/api/v1/traces?window=1h", 200, &list)
	if len(list.Traces) != 2 {
		t.Fatalf("got %d traces", len(list.Traces))
	}
	newest, oldest := list.Traces[0], list.Traces[1]
	if newest.TraceID != traceHex(2) || oldest.TraceID != traceHex(1) {
		t.Fatalf("order: %s, %s", newest.TraceID, oldest.TraceID)
	}
	if oldest.SpanCount != 3 || oldest.ErrorCount != 2 || oldest.DurationMs != 100 {
		t.Fatalf("trace 1 summary = %+v", oldest)
	}
	if oldest.RootService == nil || *oldest.RootService != "frontend" || *oldest.RootName != "GET /checkout" {
		t.Fatalf("root fields = %v %v", oldest.RootService, oldest.RootName)
	}
	if got := oldest.Services; len(got) != 3 || got[0] != "checkout" || got[1] != "frontend" || got[2] != "payments" {
		t.Fatalf("services = %v", got)
	}
}

func TestTraceSearchFilters(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	cases := []struct {
		query string
		want  []string
	}{
		{"service=payments", []string{traceHex(1)}},
		{"service=frontend", []string{traceHex(2), traceHex(1)}},
		{"service=frontend&operation=GET+%2Fitems", []string{traceHex(2)}},
		{"service=checkout&operation=GET+%2Fitems", nil},
		{"status=error", []string{traceHex(1)}},
		{"minDurationMs=50", []string{traceHex(1)}},
		{"maxDurationMs=50", []string{traceHex(2)}},
		{"service=nope", nil},
		{"start=" + url.QueryEscape(time.Now().Add(-time.Minute).Format(time.RFC3339)), nil},
	}
	for _, tc := range cases {
		t.Run(tc.query, func(t *testing.T) {
			var list TraceList
			f.get(t, "/api/v1/traces?"+tc.query, 200, &list)
			var got []string
			for _, tr := range list.Traces {
				got = append(got, tr.TraceID)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %v, want %v", got, tc.want)
				}
			}
		})
	}
}

func TestTraceSearchPagination(t *testing.T) {
	f := newFixture(t)
	var batch []model.Span
	for i := 1; i <= 7; i++ {
		// Traces 1 and 2 share a start time to exercise the trace_id tiebreak.
		off := time.Duration(i) * time.Millisecond
		if i == 2 {
			off = time.Millisecond
		}
		batch = append(batch, f.mk(spanSpec{byte(i), byte(i), 0, "svc", "op", 2, off, time.Millisecond, false}))
	}
	if err := f.store.WriteSpans(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	cursor := ""
	pages := 0
	for {
		var list TraceList
		f.get(t, "/api/v1/traces?limit=3&cursor="+cursor, 200, &list)
		pages++
		for _, tr := range list.Traces {
			if seen[tr.TraceID] {
				t.Fatalf("trace %s returned twice", tr.TraceID)
			}
			seen[tr.TraceID] = true
		}
		if list.NextCursor == "" {
			break
		}
		cursor = list.NextCursor
		if pages > 5 {
			t.Fatal("pagination did not terminate")
		}
	}
	if len(seen) != 7 {
		t.Fatalf("saw %d traces across pages", len(seen))
	}
}

func TestGetTrace(t *testing.T) {
	f := newFixture(t)
	f.seed(t)
	var tr Trace
	f.get(t, "/api/v1/traces/"+traceHex(1), 200, &tr)
	if tr.SpanCount != 3 || len(tr.Spans) != 3 || tr.DurationMs != 100 || tr.ErrorCount != 2 {
		t.Fatalf("trace = %+v", tr)
	}
	root, child := tr.Spans[0], tr.Spans[1]
	if root.ParentSpanID != nil || root.Kind != "server" || root.StartOffsetMs != 0 {
		t.Fatalf("root = %+v", root)
	}
	if child.ParentSpanID == nil || *child.ParentSpanID != root.SpanID || child.StartOffsetMs != 10 || child.DurationMs != 60 {
		t.Fatalf("child = %+v", child)
	}
	if child.StatusCode != "error" || child.StatusMessage != "failed" {
		t.Fatalf("child status = %s %q", child.StatusCode, child.StatusMessage)
	}
	if len(child.Events) != 1 || child.Events[0].StartOffsetMs != 10.001 {
		t.Fatalf("events = %+v", child.Events)
	}
	if string(child.Attributes) != `{"k": "v"}` && string(child.Attributes) != `{"k":"v"}` {
		t.Fatalf("attributes = %s", child.Attributes)
	}

	f.get(t, "/api/v1/traces/"+traceHex(9), 404, nil)
	f.get(t, "/api/v1/traces/xyz", 400, nil)
}

func TestServicesOperationsAndDependencies(t *testing.T) {
	f := newFixture(t)
	f.seed(t)

	var svc struct{ Services []Service }
	f.get(t, "/api/v1/services", 200, &svc)
	byName := map[string]Service{}
	for _, s := range svc.Services {
		byName[s.Name] = s
	}
	if len(byName) != 4 {
		t.Fatalf("services = %+v", svc.Services)
	}
	fe := byName["frontend"]
	if fe.Count != 2 || fe.ErrorCount != 1 || fe.ErrorRate != 0.5 {
		t.Fatalf("frontend = %+v", fe)
	}
	// percentile_cont over {30ms, 100ms}: p50 = 65ms.
	if fe.P50Ms != 65 {
		t.Fatalf("frontend p50 = %v", fe.P50Ms)
	}

	var ops struct{ Operations []Operation }
	f.get(t, "/api/v1/services/frontend/operations", 200, &ops)
	if len(ops.Operations) != 2 || ops.Operations[0].Kind != "server" {
		t.Fatalf("operations = %+v", ops.Operations)
	}

	var graph DependencyGraph
	f.get(t, "/api/v1/dependencies", 200, &graph)
	edges := map[string]DependencyEdge{}
	for _, e := range graph.Edges {
		edges[e.Source+"->"+e.Target] = e
	}
	if len(edges) != 3 || len(graph.Nodes) != 4 {
		t.Fatalf("graph = %+v", graph)
	}
	if e := edges["frontend->checkout"]; e.Count != 1 || e.ErrorCount != 1 || e.P50Ms != 60 {
		t.Fatalf("frontend->checkout = %+v", e)
	}
	if _, ok := edges["checkout->payments"]; !ok {
		t.Fatal("missing checkout->payments")
	}

	var ts TimeSeries
	f.get(t, "/api/v1/services/frontend/timeseries?window=1h&step=1m", 200, &ts)
	var total int64
	for _, b := range ts.Buckets {
		total += b.Count
	}
	if total != 2 || ts.Step != "1m0s" {
		t.Fatalf("timeseries = %+v", ts)
	}
}

func TestParameterValidation(t *testing.T) {
	f := newFixture(t)
	for _, q := range []string{
		"/api/v1/traces?window=0s",
		"/api/v1/traces?window=30d",
		"/api/v1/traces?window=abc",
		"/api/v1/traces?start=yesterday",
		"/api/v1/traces?start=2026-01-01T00:00:00Z&window=1h",
		"/api/v1/traces?limit=0",
		"/api/v1/traces?limit=9999",
		"/api/v1/traces?minDurationMs=-1",
		"/api/v1/traces?minDurationMs=10&maxDurationMs=5",
		"/api/v1/traces?operation=x",
		"/api/v1/traces?status=weird",
		"/api/v1/traces?cursor=AAAA",
		"/api/v1/services/frontend/timeseries?window=7d&step=1s",
		"/api/v1/services/frontend/timeseries?step=-1m",
	} {
		f.get(t, q, 400, nil)
	}
}

func TestConcurrentWritesToSameTrace(t *testing.T) {
	f := newFixture(t)
	const writers, perWriter = 8, 50
	var wg sync.WaitGroup
	errs := make(chan error, writers)
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				// Every write touches traces 1 and 2, and some re-send spans.
				id := byte(w*perWriter/4 + i/4 + 1)
				batch := []model.Span{
					f.mk(spanSpec{1, id, 0, "a", "op", 2, 0, time.Millisecond, false}),
					f.mk(spanSpec{2, id, 0, "b", "op", 2, 0, time.Millisecond, i%3 == 0}),
				}
				if err := f.store.WriteSpans(context.Background(), batch); err != nil {
					errs <- err
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	// The summary must agree with the spans actually stored.
	var mismatches int
	err := f.store.Pool().QueryRow(context.Background(), `
		SELECT count(*) FROM traces t
		WHERE span_count <> (SELECT count(*) FROM spans s WHERE s.trace_id = t.trace_id)
		   OR error_count <> (SELECT count(*) FROM spans s WHERE s.trace_id = t.trace_id AND status_code = 2)`).Scan(&mismatches)
	if err != nil {
		t.Fatal(err)
	}
	if mismatches != 0 {
		t.Fatalf("%d trace summaries disagree with stored spans", mismatches)
	}
}

func TestCursorRoundTrip(t *testing.T) {
	id := []byte("0123456789abcdef")
	ns, gotID, err := decodeCursor(encodeCursor(1234567890123, id))
	if err != nil || ns != 1234567890123 || string(gotID) != string(id) {
		t.Fatalf("round trip: %d %q %v", ns, gotID, err)
	}
	if _, _, err := decodeCursor("AAAA"); err == nil {
		t.Fatal("short cursor accepted")
	}
}
