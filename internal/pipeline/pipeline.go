// Package pipeline implements Aperture's bounded, batched ingestion pipeline.
//
// # Acknowledgment and durability semantics
//
// Submit blocks until the spans it was given are part of a PostgreSQL
// transaction that has COMMITTED, or until it fails. Only then does the HTTP
// layer answer the OTLP client with success. The persistence boundary is
// therefore "committed to PostgreSQL"; durability beyond that is whatever the
// database is configured for (synchronous_commit=on by default, i.e. WAL
// flushed to local disk).
//
// Consequences:
//
//   - A success response always means the spans are stored.
//   - If the client times out or the request context ends while the spans are
//     queued or being written, the client gets an error and will typically
//     retry. The spans may still be committed. Writes are idempotent on
//     (trace_id, span_id), so retries never create duplicates: delivery is
//     at-least-once with idempotent storage.
//   - Queued spans live only in memory. If the process crashes, queued spans
//     are lost, but none of them were acknowledged, so a retrying client
//     (such as the OpenTelemetry Collector) resends them.
//
// # Overload
//
// The queue is a bounded channel of export requests. When it is full, Submit
// fails immediately with ErrOverloaded instead of blocking; the HTTP layer
// maps that to 503 + Retry-After, which OTLP clients treat as retryable.
// Memory is bounded by QueueCapacity * MaxSpansPerRequest.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/model"
)

var (
	// ErrOverloaded is returned when the queue is full.
	ErrOverloaded = errors.New("pipeline: queue full")
	// ErrShuttingDown is returned once Shutdown has begun.
	ErrShuttingDown = errors.New("pipeline: shutting down")
	// ErrPermanent marks write errors that retrying the same data will not
	// fix (for example a constraint or data-format violation). Writers wrap
	// such errors with it so the HTTP layer can tell clients not to retry.
	ErrPermanent = errors.New("permanent write error")
)

// Writer persists a batch of spans atomically: either all spans in the call
// are committed or none are.
type Writer interface {
	WriteSpans(ctx context.Context, spans []model.Span) error
}

// Config controls pipeline sizing and timing.
type Config struct {
	QueueCapacity int           // max queued export requests
	Workers       int           // concurrent database writers
	BatchSize     int           // target spans per database transaction
	FlushInterval time.Duration // max time a partial batch waits for more requests
	WriteTimeout  time.Duration // per-transaction timeout
}

// Validate reports configuration errors.
func (c Config) Validate() error {
	switch {
	case c.QueueCapacity < 1:
		return fmt.Errorf("queue capacity must be >= 1, got %d", c.QueueCapacity)
	case c.Workers < 1:
		return fmt.Errorf("workers must be >= 1, got %d", c.Workers)
	case c.BatchSize < 1:
		return fmt.Errorf("batch size must be >= 1, got %d", c.BatchSize)
	case c.FlushInterval <= 0:
		return fmt.Errorf("flush interval must be > 0, got %s", c.FlushInterval)
	case c.WriteTimeout <= 0:
		return fmt.Errorf("write timeout must be > 0, got %s", c.WriteTimeout)
	}
	return nil
}

type job struct {
	spans []model.Span
	done  chan error // buffered(1); receives exactly one result
}

// Pipeline is a bounded queue drained by a pool of batching writers.
type Pipeline struct {
	cfg    Config
	writer Writer
	m      *metrics.Metrics
	log    *slog.Logger

	queue chan *job

	mu      sync.RWMutex // guards closed and the close of queue
	closed  bool
	wg      sync.WaitGroup
	started bool
}

// New creates a pipeline. Call Start before Submit.
func New(cfg Config, w Writer, m *metrics.Metrics, log *slog.Logger) (*Pipeline, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	p := &Pipeline{cfg: cfg, writer: w, m: m, log: log, queue: make(chan *job, cfg.QueueCapacity)}
	m.QueueCapacity.Set(float64(cfg.QueueCapacity))
	m.RegisterQueueDepth(func() float64 { return float64(len(p.queue)) })
	return p, nil
}

// Start launches the worker goroutines.
func (p *Pipeline) Start() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.started {
		return
	}
	p.started = true
	for i := 0; i < p.cfg.Workers; i++ {
		p.wg.Add(1)
		go p.worker(i)
	}
}

// QueueDepth returns the number of queued export requests.
func (p *Pipeline) QueueDepth() int { return len(p.queue) }

// Accepting reports whether Submit can currently accept work.
func (p *Pipeline) Accepting() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.started && !p.closed
}

// Submit enqueues spans and waits until they are committed or fail.
// See the package documentation for the exact semantics.
func (p *Pipeline) Submit(ctx context.Context, spans []model.Span) error {
	if len(spans) == 0 {
		return nil
	}
	j := &job{spans: spans, done: make(chan error, 1)}

	p.mu.RLock()
	if p.closed {
		p.mu.RUnlock()
		return ErrShuttingDown
	}
	select {
	case p.queue <- j:
		p.mu.RUnlock()
	default:
		p.mu.RUnlock()
		p.m.Overloaded.Inc()
		return ErrOverloaded
	}

	select {
	case err := <-j.done:
		return err
	case <-ctx.Done():
		// The job stays queued and will still be written; the client must
		// treat this as a failure and retry (writes are idempotent).
		return ctx.Err()
	}
}

// Shutdown stops accepting new work, flushes everything already queued, and
// waits for workers to finish or ctx to expire. Callers should stop the HTTP
// server first so in-flight handlers can still receive their results.
func (p *Pipeline) Shutdown(ctx context.Context) error {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.queue)
	}
	p.mu.Unlock()

	finished := make(chan struct{})
	go func() { p.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("pipeline shutdown: %w (%d requests still queued)", ctx.Err(), len(p.queue))
	}
}

func (p *Pipeline) worker(id int) {
	defer p.wg.Done()
	log := p.log.With("worker", id)
	timer := time.NewTimer(time.Hour)
	timer.Stop()

	for {
		first, ok := <-p.queue
		if !ok {
			return
		}
		batch := []*job{first}
		n := len(first.spans)

		timer.Reset(p.cfg.FlushInterval)
		open := true
	fill:
		for n < p.cfg.BatchSize {
			select {
			case j, ok := <-p.queue:
				if !ok {
					open = false
					break fill
				}
				batch = append(batch, j)
				n += len(j.spans)
			case <-timer.C:
				break fill
			}
		}
		if !timer.Stop() {
			select { // drain if it fired and wasn't consumed
			case <-timer.C:
			default:
			}
		}

		p.flush(log, batch, n)
		if !open {
			return
		}
	}
}

// flush writes a batch in one transaction. If that fails and the batch
// contains several requests, each request is retried in its own transaction
// so that one bad or unlucky request cannot fail its neighbours.
func (p *Pipeline) flush(log *slog.Logger, batch []*job, n int) {
	p.m.BatchJobs.Observe(float64(len(batch)))
	p.m.BatchSpans.Observe(float64(n))

	spans := batch[0].spans
	if len(batch) > 1 {
		spans = make([]model.Span, 0, n)
		for _, j := range batch {
			spans = append(spans, j.spans...)
		}
	}

	err := p.write(spans)
	if err == nil || len(batch) == 1 {
		if err != nil {
			log.Error("batch write failed", "spans", n, "err", err)
		}
		for _, j := range batch {
			j.done <- err
		}
		return
	}

	log.Warn("batch write failed; retrying requests individually", "requests", len(batch), "spans", n, "err", err)
	for _, j := range batch {
		jerr := p.write(j.spans)
		if jerr != nil {
			log.Error("request write failed", "spans", len(j.spans), "err", jerr)
		}
		j.done <- jerr
	}
}

func (p *Pipeline) write(spans []model.Span) error {
	ctx, cancel := context.WithTimeout(context.Background(), p.cfg.WriteTimeout)
	defer cancel()
	start := time.Now()
	err := p.writer.WriteSpans(ctx, spans)
	p.m.FlushDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		p.m.WriteErrors.Inc()
		return err
	}
	p.m.SpansWritten.Add(float64(len(spans)))
	return nil
}
