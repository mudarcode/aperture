// Package app assembles Aperture's components. It is shared by the server
// binary and end-to-end tests so both exercise identical wiring.
package app

import (
	"log/slog"
	"net/http"

	"github.com/mudarcode/aperture/internal/config"
	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/otlpingest"
	"github.com/mudarcode/aperture/internal/pipeline"
	"github.com/mudarcode/aperture/internal/server"
	"github.com/mudarcode/aperture/internal/store/postgres"
)

// App is a fully wired, started ingestion stack.
type App struct {
	Handler  http.Handler
	Pipeline *pipeline.Pipeline
	Metrics  *metrics.Metrics
}

// New wires the pipeline, OTLP handler, and HTTP routes on top of store and
// starts the pipeline workers. Callers own shutdown: stop the HTTP server,
// then call Pipeline.Shutdown, then close the store.
func New(cfg config.Config, store *postgres.Store, log *slog.Logger) (*App, error) {
	m := metrics.New()
	pl, err := pipeline.New(cfg.Pipeline, store, m, log.With("component", "pipeline"))
	if err != nil {
		return nil, err
	}
	pl.Start()
	ingest := otlpingest.NewHandler(cfg.Handler, &otlpingest.Converter{Limits: cfg.Limits}, pl, m,
		log.With("component", "otlp"))
	mux := server.NewMux(ingest, server.Readiness{Accepting: pl.Accepting, PingDB: store.Ping}, m)
	return &App{Handler: mux, Pipeline: pl, Metrics: m}, nil
}
