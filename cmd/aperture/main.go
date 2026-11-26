// Command aperture runs the Aperture trace ingestion server.
//
// Usage:
//
//	aperture serve     run the server (default)
//	aperture migrate   apply database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/mudarcode/aperture/internal/app"
	"github.com/mudarcode/aperture/internal/config"
	"github.com/mudarcode/aperture/internal/store/postgres"
)

func main() {
	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}
	if err := run(cmd); err != nil {
		fmt.Fprintln(os.Stderr, "aperture:", err)
		os.Exit(1)
	}
}

func run(cmd string) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	log := newLogger(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store, err := postgres.Open(startCtx, cfg.DatabaseURL, cfg.DBMaxConns)
	if err != nil {
		return err
	}
	defer store.Close()

	switch cmd {
	case "migrate":
		if err := store.Migrate(startCtx); err != nil {
			return err
		}
		log.Info("migrations applied")
		return nil
	case "serve":
	default:
		return fmt.Errorf("unknown command %q (want serve or migrate)", cmd)
	}

	if cfg.AutoMigrate {
		if err := store.Migrate(startCtx); err != nil {
			return err
		}
		log.Info("migrations applied")
	}

	a, err := app.New(cfg, store, log)
	if err != nil {
		return err
	}
	pl := a.Pipeline

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           a.Handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// Must exceed the ack timeout so a handler can report a commit result.
		WriteTimeout: cfg.Handler.AckTimeout + 10*time.Second,
		IdleTimeout:  120 * time.Second,
		ErrorLog:     slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}

	if cfg.PprofAddr != "" {
		// Opt-in profiling on a separate listener (keep it on localhost).
		pprofMux := http.NewServeMux()
		pprofMux.HandleFunc("/debug/pprof/", pprof.Index)
		pprofMux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		pprofMux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		pprofMux.HandleFunc("/debug/pprof/trace", pprof.Trace)
		go func() {
			log.Info("pprof listening", "addr", cfg.PprofAddr)
			if err := http.ListenAndServe(cfg.PprofAddr, pprofMux); err != nil {
				log.Error("pprof listener", "err", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.HTTPAddr,
			"workers", cfg.Pipeline.Workers, "queue_capacity", cfg.Pipeline.QueueCapacity,
			"batch_size", cfg.Pipeline.BatchSize, "flush_interval", cfg.Pipeline.FlushInterval.String())
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		if err != nil {
			return fmt.Errorf("http server: %w", err)
		}
	case <-ctx.Done():
	}

	// Shutdown order: stop accepting HTTP requests and let in-flight handlers
	// receive their commit results, then drain the pipeline, then close the
	// database pool (deferred above).
	log.Info("shutting down", "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
	defer cancelShutdown()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("http shutdown", "err", err)
	}
	if err := pl.Shutdown(shutdownCtx); err != nil {
		log.Error("pipeline shutdown", "err", err)
		return err
	}
	log.Info("shutdown complete")
	return nil
}

func newLogger(cfg config.Config) *slog.Logger {
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewJSONHandler(os.Stdout, opts)
	if cfg.LogFormat == "text" {
		h = slog.NewTextHandler(os.Stdout, opts)
	}
	return slog.New(h).With("service", "aperture")
}
