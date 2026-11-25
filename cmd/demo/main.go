// Command demo runs one of the instrumented demo microservices that make up a
// small online shop. Every service is the same binary started with a
// different -role:
//
//	gateway   public API: GET /api/products, POST /api/checkout
//	catalog   product lookups backed by an in-memory table
//	checkout  orchestrates catalog, inventory, and payments
//	inventory stock reservations
//	payments  charges through a (simulated) external payment provider
//	traffic   drives steady user traffic against the gateway
//
// Services are instrumented with the OpenTelemetry Go SDK and otelhttp, so
// W3C trace context propagates over real HTTP calls between containers.
// Spans are exported over OTLP/gRPC to the OpenTelemetry Collector.
//
// Faults can be injected per service at start-up (FAULT_* variables) or at
// runtime with POST /admin/faults, to produce latency and error patterns
// worth diagnosing in the UI.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
)

func main() {
	role := flag.String("role", envOr("DEMO_ROLE", ""), "service role")
	addr := flag.String("addr", envOr("DEMO_ADDR", ":8080"), "listen address")
	flag.Parse()

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", *role)
	if err := run(*role, *addr, log); err != nil {
		log.Error("exit", "err", err)
		os.Exit(1)
	}
}

func run(role, addr string, log *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := setupTracing(ctx, role)
	if err != nil {
		return err
	}
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(c); err != nil {
			log.Warn("flush traces", "err", err)
		}
	}()

	faults, err := faultsFromEnv()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 10 * time.Second, Transport: otelhttp.NewTransport(http.DefaultTransport,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			path := r.URL.Path
			if strings.HasPrefix(path, "/products/") {
				path = "/products/{id}" // keep span names low-cardinality
			}
			return r.Method + " " + path
		}))}
	svc := &service{role: role, faults: faults, client: client, log: log}

	if role == "traffic" {
		return runTraffic(ctx, svc)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("ok")) })
	mux.HandleFunc("GET /admin/faults", svc.getFaults)
	mux.HandleFunc("POST /admin/faults", svc.setFaults)
	if err := svc.routes(mux); err != nil {
		return err
	}

	// otelhttp creates the SERVER span for each request; route patterns give
	// low-cardinality span names such as "GET /products/{id}".
	handler := otelhttp.NewHandler(mux, role,
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			if r.Pattern != "" {
				return r.Pattern
			}
			return r.Method
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/healthz" && r.URL.Path != "/admin/faults"
		}),
	)
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return srv.Shutdown(c)
}

func setupTracing(ctx context.Context, role string) (func(context.Context) error, error) {
	if role == "" {
		return nil, errors.New("role is required")
	}
	// Endpoint and protocol come from the standard OTEL_EXPORTER_OTLP_* env vars.
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create exporter: %w", err)
	}
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(role), semconv.ServiceVersion("1.0.0"),
			attribute.String("deployment.environment.name", "demo")),
		resource.WithHost(), resource.WithProcessRuntimeName(), resource.WithProcessRuntimeVersion(),
		resource.WithFromEnv(),
	)
	if err != nil {
		return nil, fmt.Errorf("create resource: %w", err)
	}
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}, propagation.Baggage{}))
	return tp.Shutdown, nil
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
