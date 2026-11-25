// Package e2e contains end-to-end tests that drive the real HTTP stack and a
// real PostgreSQL database with the official OpenTelemetry Go exporter.
package e2e

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/mudarcode/aperture/internal/app"
	"github.com/mudarcode/aperture/internal/config"
	"github.com/mudarcode/aperture/internal/otlpingest"
	"github.com/mudarcode/aperture/internal/pipeline"
	"github.com/mudarcode/aperture/internal/store/postgres"
	"github.com/mudarcode/aperture/internal/testutil/pgtest"
)

type stack struct {
	srv   *httptest.Server
	app   *app.App
	store *postgres.Store
}

func startStack(t *testing.T, mutate func(*config.Config)) *stack {
	t.Helper()
	ctx := context.Background()
	cfg := config.Config{
		Pipeline: pipeline.Config{QueueCapacity: 64, Workers: 2, BatchSize: 500, FlushInterval: 10 * time.Millisecond, WriteTimeout: 5 * time.Second},
		Handler: otlpingest.HandlerConfig{
			MaxRequestBytes: 4 << 20, MaxDecompressedBytes: 16 << 20, MaxSpansPerRequest: 5000,
			AckTimeout: 5 * time.Second, RetryAfter: time.Second,
		},
		Limits: otlpingest.DefaultLimits(),
	}
	if mutate != nil {
		mutate(&cfg)
	}
	store, err := postgres.Open(ctx, pgtest.NewDatabase(t), 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a, err := app.New(cfg, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(func() {
		srv.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = a.Pipeline.Shutdown(ctx)
		store.Close()
	})
	return &stack{srv: srv, app: a, store: store}
}

func newProvider(t *testing.T, endpoint, service string) *sdktrace.TracerProvider {
	t.Helper()
	exp, err := otlptracehttp.New(context.Background(),
		otlptracehttp.WithEndpointURL(endpoint+otlpingest.TracesPath),
		otlptracehttp.WithCompression(otlptracehttp.GzipCompression),
	)
	if err != nil {
		t.Fatal(err)
	}
	res := resource.NewSchemaless(semconv.ServiceName(service), attribute.String("deployment.environment.name", "test"))
	return sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
}

// TestOfficialExporterRoundTrip sends a small distributed trace through the
// official OTLP/HTTP exporter (protobuf + gzip) and verifies it is stored.
func TestOfficialExporterRoundTrip(t *testing.T) {
	st := startStack(t, nil)
	ctx := context.Background()

	frontend := newProvider(t, st.srv.URL, "frontend")
	checkout := newProvider(t, st.srv.URL, "checkout")

	rootCtx, root := frontend.Tracer("e2e").Start(ctx, "GET /checkout", trace.WithSpanKind(trace.SpanKindServer),
		trace.WithAttributes(attribute.String("http.request.method", "GET"), attribute.Int("http.response.status_code", 500)))
	_, child := checkout.Tracer("e2e").Start(rootCtx, "charge card", trace.WithSpanKind(trace.SpanKindInternal))
	child.AddEvent("retrying", trace.WithAttributes(attribute.Int("attempt", 2)))
	child.SetStatus(codes.Error, "card declined")
	time.Sleep(2 * time.Millisecond)
	child.End()
	root.End()

	// Shutdown flushes; it returns an error if the export was not acknowledged.
	if err := checkout.Shutdown(ctx); err != nil {
		t.Fatalf("checkout export: %v", err)
	}
	if err := frontend.Shutdown(ctx); err != nil {
		t.Fatalf("frontend export: %v", err)
	}

	traceID := root.SpanContext().TraceID()
	rows, err := st.store.Pool().Query(ctx, `
		SELECT span_id, parent_span_id, service_name, name, kind, status_code, status_message,
		       duration_nano, attributes, resource_attributes, events
		FROM spans WHERE trace_id = $1 ORDER BY start_time_unix_nano`, traceID[:])
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	type row struct {
		spanID, parentID         []byte
		service, name, statusMsg string
		kind, status             int16
		dur                      int64
		attrs, resAttrs, events  []byte
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.spanID, &r.parentID, &r.service, &r.name, &r.kind, &r.status, &r.statusMsg,
			&r.dur, &r.attrs, &r.resAttrs, &r.events); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("stored %d spans for trace, want 2", len(got))
	}

	r, c := got[0], got[1]
	rootID, childID := root.SpanContext().SpanID(), child.SpanContext().SpanID()
	if string(r.spanID) != string(rootID[:]) || r.parentID != nil {
		t.Errorf("root span: id=%x parent=%x", r.spanID, r.parentID)
	}
	if string(c.spanID) != string(childID[:]) || string(c.parentID) != string(rootID[:]) {
		t.Errorf("child span: id=%x parent=%x, want parent %x", c.spanID, c.parentID, rootID[:])
	}
	if r.service != "frontend" || c.service != "checkout" {
		t.Errorf("services = %q, %q", r.service, c.service)
	}
	if r.name != "GET /checkout" || r.kind != 2 /* SERVER */ || c.kind != 1 /* INTERNAL */ {
		t.Errorf("root name/kind = %q/%d, child kind %d", r.name, r.kind, c.kind)
	}
	if c.status != 2 || c.statusMsg != "card declined" {
		t.Errorf("child status = %d %q", c.status, c.statusMsg)
	}
	if c.dur < int64(2*time.Millisecond) {
		t.Errorf("child duration = %d", c.dur)
	}
	var attrs map[string]any
	_ = json.Unmarshal(r.attrs, &attrs)
	if attrs["http.response.status_code"] != float64(500) {
		t.Errorf("root attributes = %s", r.attrs)
	}
	var resAttrs map[string]any
	_ = json.Unmarshal(r.resAttrs, &resAttrs)
	if resAttrs["deployment.environment.name"] != "test" || resAttrs["service.name"] != "frontend" {
		t.Errorf("resource attributes = %s", r.resAttrs)
	}
	var events []map[string]any
	_ = json.Unmarshal(c.events, &events)
	if len(events) != 1 || events[0]["name"] != "retrying" {
		t.Errorf("events = %s", c.events)
	}
}

// TestConcurrentExportersNoLossNoDuplicates runs many exporters concurrently
// and checks every acknowledged span is stored exactly once.
func TestConcurrentExportersNoLossNoDuplicates(t *testing.T) {
	st := startStack(t, nil)
	ctx := context.Background()
	const exporters, spansEach = 8, 250

	var wg sync.WaitGroup
	errs := make(chan error, exporters)
	for i := 0; i < exporters; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			tp := newProvider(t, st.srv.URL, "svc")
			tr := tp.Tracer("load")
			for j := 0; j < spansEach; j++ {
				_, sp := tr.Start(ctx, "op")
				sp.End()
			}
			errs <- tp.Shutdown(ctx)
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("export: %v", err)
		}
	}

	var n int
	if err := st.store.Pool().QueryRow(ctx, `SELECT count(*) FROM spans`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != exporters*spansEach {
		t.Fatalf("stored %d spans, want %d", n, exporters*spansEach)
	}
}

func TestHealthReadinessAndMetrics(t *testing.T) {
	st := startStack(t, nil)

	for path, want := range map[string]int{"/healthz": 200, "/readyz": 200, "/metrics": 200} {
		resp, err := http.Get(st.srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != want {
			t.Fatalf("%s = %d: %s", path, resp.StatusCode, body)
		}
		if path == "/metrics" && !strings.Contains(string(body), "aperture_pipeline_queue_depth") {
			t.Fatalf("metrics missing pipeline gauges")
		}
	}

	// After pipeline shutdown the instance must report not ready.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.app.Pipeline.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(st.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz after shutdown = %d", resp.StatusCode)
	}
}

// TestReadinessFailsWhenDatabaseDown closes the pool and expects /readyz 503
// and retryable 503 responses from ingestion.
func TestReadinessFailsWhenDatabaseDown(t *testing.T) {
	st := startStack(t, nil)
	st.store.Close()

	resp, err := http.Get(st.srv.URL + "/readyz")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("/readyz = %d", resp.StatusCode)
	}

	tp := newProvider(t, st.srv.URL, "svc")
	_, sp := tp.Tracer("x").Start(context.Background(), "op")
	sp.End()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := tp.ForceFlush(ctx); err == nil {
		t.Fatal("export should fail while the database is down")
	}
}
