package postgres_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/mudarcode/aperture/internal/model"
	"github.com/mudarcode/aperture/internal/pipeline"
	"github.com/mudarcode/aperture/internal/store/postgres"
	"github.com/mudarcode/aperture/internal/testutil/pgtest"
)

func openStore(t *testing.T) *postgres.Store {
	t.Helper()
	ctx := context.Background()
	s, err := postgres.Open(ctx, pgtest.NewDatabase(t), 4)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return s
}

func span(trace, id byte) model.Span {
	start := uint64(time.Date(2026, 11, 25, 12, 0, 0, 123456789, time.UTC).UnixNano())
	s := model.Span{
		ServiceName:        "checkout",
		Name:               "GET /cart",
		Kind:               2,
		StartTimeUnixNano:  start,
		EndTimeUnixNano:    start + 1_500_000,
		StatusCode:         2,
		StatusMessage:      "boom",
		ScopeName:          "scope",
		ScopeVersion:       "1.0",
		ResourceAttributes: []byte(`{"service.name":"checkout"}`),
		Attributes:         []byte(`{"http.status_code":500}`),
		Events:             []byte(`[]`),
		Links:              []byte(`[]`),
	}
	s.TraceID[15] = trace
	s.SpanID[7] = id
	return s
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := openStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
}

func TestWriteSpansRoundTrip(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()

	root := span(1, 1)
	child := span(1, 2)
	parent := root.SpanID
	child.ParentSpanID = &parent
	child.Name = "SELECT carts"
	child.Kind = 3

	if err := s.WriteSpans(ctx, []model.Span{root, child}); err != nil {
		t.Fatal(err)
	}

	var (
		parentID           []byte
		service, name      string
		kind, status       int16
		startNS, durNS     int64
		startTime          time.Time
		attrs, resAttrs    []byte
		statusMsg, scopeNm string
	)
	err := s.Pool().QueryRow(ctx, `
		SELECT parent_span_id, service_name, name, kind, status_code, status_message, scope_name,
		       start_time_unix_nano, duration_nano, start_time, attributes, resource_attributes
		FROM spans WHERE trace_id = $1 AND span_id = $2`, child.TraceID[:], child.SpanID[:]).
		Scan(&parentID, &service, &name, &kind, &status, &statusMsg, &scopeNm, &startNS, &durNS, &startTime, &attrs, &resAttrs)
	if err != nil {
		t.Fatal(err)
	}
	if string(parentID) != string(root.SpanID[:]) {
		t.Errorf("parent = %x", parentID)
	}
	if service != "checkout" || name != "SELECT carts" || kind != 3 || status != 2 || statusMsg != "boom" || scopeNm != "scope" {
		t.Errorf("fields: %s %s %d %d %s %s", service, name, kind, status, statusMsg, scopeNm)
	}
	if uint64(startNS) != child.StartTimeUnixNano {
		t.Errorf("start ns = %d, want exact %d", startNS, child.StartTimeUnixNano)
	}
	if durNS != 1_500_000 {
		t.Errorf("duration = %d", durNS)
	}
	if want := time.Unix(0, int64(child.StartTimeUnixNano)); startTime.Sub(want).Abs() > time.Microsecond {
		t.Errorf("start_time = %s, want ~%s", startTime, want)
	}
	var a map[string]any
	if err := json.Unmarshal(attrs, &a); err != nil || a["http.status_code"] != float64(500) {
		t.Errorf("attributes = %s", attrs)
	}

	var rootParent []byte
	if err := s.Pool().QueryRow(ctx, `SELECT parent_span_id FROM spans WHERE span_id = $1`, root.SpanID[:]).Scan(&rootParent); err != nil {
		t.Fatal(err)
	}
	if rootParent != nil {
		t.Errorf("root parent should be NULL, got %x", rootParent)
	}
}

func TestWriteSpansIsIdempotent(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	batch := []model.Span{span(1, 1), span(1, 2)}
	for i := 0; i < 3; i++ {
		if err := s.WriteSpans(ctx, batch); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	// Overlapping batch: one duplicate, one new.
	if err := s.WriteSpans(ctx, []model.Span{span(1, 2), span(1, 3)}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM spans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("rows = %d, want 3", n)
	}
}

func TestWriteSpansLargeBatch(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	// Well past the 65535 bind-parameter limit a naive multi-row INSERT would hit.
	batch := make([]model.Span, 10_000)
	for i := range batch {
		batch[i] = span(byte(i%7), 0)
		batch[i].SpanID[0], batch[i].SpanID[1] = byte(i>>8), byte(i)
	}
	if err := s.WriteSpans(ctx, batch); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM spans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != len(batch) {
		t.Fatalf("rows = %d", n)
	}
}

func TestWriteSpansIsAtomicAndClassifiesDataErrors(t *testing.T) {
	s := openStore(t)
	ctx := context.Background()
	bad := span(2, 2)
	bad.Kind = 9 // violates CHECK constraint; validation would normally prevent this
	err := s.WriteSpans(ctx, []model.Span{span(2, 1), bad})
	if !errors.Is(err, pipeline.ErrPermanent) {
		t.Fatalf("err = %v, want ErrPermanent", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT count(*) FROM spans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("batch must be atomic; found %d rows", n)
	}
}

func TestWriteSpansTransientErrorNotPermanent(t *testing.T) {
	s := openStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.WriteSpans(ctx, []model.Span{span(3, 1)})
	if err == nil || errors.Is(err, pipeline.ErrPermanent) {
		t.Fatalf("err = %v; want a non-permanent error", err)
	}
}
