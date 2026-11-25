// Command tracegen sends synthetic multi-service traces to an OTLP/HTTP
// endpoint using the official OpenTelemetry Go SDK. It is a smoke-test tool
// for the ingestion path (directly to Aperture, or through the Collector);
// it is not the benchmark load generator.
//
//	tracegen -endpoint http://localhost:4318 -traces 100
package main

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

func main() {
	endpoint := flag.String("endpoint", "http://localhost:4318", "OTLP/HTTP base URL (the /v1/traces path is appended)")
	traces := flag.Int("traces", 50, "number of traces to send")
	services := flag.String("services", "frontend,checkout,payments,inventory", "comma-separated service names; the first is the entry point")
	errorRate := flag.Float64("error-rate", 0.1, "probability that a leaf span fails")
	seed := flag.Uint64("seed", 1, "random seed for reproducible shapes")
	flag.Parse()

	if err := run(*endpoint, *traces, strings.Split(*services, ","), *errorRate, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "tracegen:", err)
		os.Exit(1)
	}
}

func run(endpoint string, traces int, services []string, errorRate float64, seed uint64) error {
	ctx := context.Background()
	rng := rand.New(rand.NewPCG(seed, seed))

	providers := make([]*sdktrace.TracerProvider, len(services))
	tracers := make([]trace.Tracer, len(services))
	for i, name := range services {
		exp, err := otlptracehttp.New(ctx,
			otlptracehttp.WithEndpointURL(strings.TrimRight(endpoint, "/")+"/v1/traces"),
			otlptracehttp.WithCompression(otlptracehttp.GzipCompression))
		if err != nil {
			return err
		}
		providers[i] = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(exp),
			sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName(name), semconv.ServiceVersion("0.1.0"))),
		)
		tracers[i] = providers[i].Tracer("aperture/tracegen")
	}

	spans := 0
	base := time.Now().Add(-time.Duration(traces) * 50 * time.Millisecond)
	for t := 0; t < traces; t++ {
		start := base.Add(time.Duration(t) * 50 * time.Millisecond)
		spans += emit(ctx, rng, tracers, services, 0, start, errorRate, "GET /checkout")
	}

	var firstErr error
	for _, p := range providers {
		if err := p.Shutdown(ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if firstErr != nil {
		return fmt.Errorf("export failed: %w", firstErr)
	}
	fmt.Printf("sent %d traces (%d spans) to %s\n", traces, spans, endpoint)
	return nil
}

// emit creates a span for service idx and recursively calls downstream
// services, returning the number of spans created and the span's end time.
func emit(ctx context.Context, rng *rand.Rand, tracers []trace.Tracer, names []string, idx int, start time.Time, errorRate float64, name string) int {
	kind := trace.SpanKindServer
	ctx, span := tracers[idx].Start(ctx, name, trace.WithTimestamp(start), trace.WithSpanKind(kind),
		trace.WithAttributes(attribute.String("rpc.system", "http")))
	n := 1
	cursor := start.Add(time.Duration(1+rng.IntN(3)) * time.Millisecond)

	failed := false
	if idx+1 < len(tracers) {
		calls := 1 + rng.IntN(2)
		for c := 0; c < calls; c++ {
			child := idx + 1 + rng.IntN(len(tracers)-idx-1)
			sub := emit(ctx, rng, tracers, names, child, cursor, errorRate, fmt.Sprintf("POST /%s/v1/op%d", names[child], c))
			n += sub
			cursor = cursor.Add(time.Duration(5+rng.IntN(40)) * time.Millisecond)
		}
	} else {
		// Leaf: a database call.
		_, db := tracers[idx].Start(ctx, "SELECT items", trace.WithTimestamp(cursor), trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(attribute.String("db.system.name", "postgresql")))
		d := time.Duration(1+rng.IntN(30)) * time.Millisecond
		if rng.Float64() < errorRate {
			db.SetStatus(codes.Error, "deadline exceeded")
			failed = true
		}
		db.End(trace.WithTimestamp(cursor.Add(d)))
		cursor = cursor.Add(d)
		n++
	}
	if failed && idx == len(tracers)-1 {
		span.SetStatus(codes.Error, "downstream failure")
	}
	span.End(trace.WithTimestamp(cursor.Add(time.Millisecond)))
	return n
}
