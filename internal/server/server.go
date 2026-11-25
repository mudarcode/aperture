// Package server wires HTTP routes for ingestion and operations.
package server

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/otlpingest"
)

// Readiness reports dependencies needed to serve traffic.
type Readiness struct {
	Accepting func() bool
	PingDB    func(context.Context) error
}

// NewMux builds the HTTP routes:
//
//	POST /v1/traces  OTLP/HTTP trace ingestion
//	GET  /healthz    liveness: the process is running
//	GET  /readyz     readiness: database reachable and pipeline accepting
//	GET  /metrics    Prometheus metrics
//
// extra registers additional routes (the query API).
func NewMux(ingest http.Handler, ready Readiness, m *metrics.Metrics, extra ...func(*http.ServeMux)) *http.ServeMux {
	mux := http.NewServeMux()
	for _, register := range extra {
		register(mux)
	}
	// Registered without a method so the handler can answer 405 with an
	// OTLP-style Status body.
	mux.Handle(otlpingest.TracesPath, ingest)

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		checks := map[string]string{"pipeline": "ok", "database": "ok"}
		ok := true
		if !ready.Accepting() {
			checks["pipeline"] = "not accepting"
			ok = false
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ready.PingDB(ctx); err != nil {
			checks["database"] = "unreachable"
			ok = false
		}
		code := http.StatusOK
		status := "ready"
		if !ok {
			code, status = http.StatusServiceUnavailable, "not ready"
		}
		writeJSON(w, code, map[string]any{"status": status, "checks": checks})
	})

	mux.Handle("GET /metrics", promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{}))
	return mux
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
