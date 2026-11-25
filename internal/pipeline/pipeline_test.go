package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/model"
)

// fakeWriter records committed batches. If gate is non-nil, each write waits
// for a value on it before "committing".
type fakeWriter struct {
	mu      sync.Mutex
	batches [][]model.Span
	gate    chan struct{}
	started chan struct{} // receives once per write attempt (if non-nil)
	fail    func([]model.Span) error
}

func (w *fakeWriter) WriteSpans(ctx context.Context, spans []model.Span) error {
	if w.started != nil {
		w.started <- struct{}{}
	}
	if w.gate != nil {
		select {
		case <-w.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if w.fail != nil {
		if err := w.fail(spans); err != nil {
			return err
		}
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.batches = append(w.batches, append([]model.Span(nil), spans...))
	return nil
}

func (w *fakeWriter) committed() (batches, spans int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, b := range w.batches {
		spans += len(b)
	}
	return len(w.batches), spans
}

func spans(n int, tag byte) []model.Span {
	out := make([]model.Span, n)
	for i := range out {
		out[i].SpanID[0] = tag
		out[i].SpanID[1] = byte(i)
	}
	return out
}

func newTestPipeline(t *testing.T, cfg Config, w Writer) *Pipeline {
	t.Helper()
	p, err := New(cfg, w, metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	p.Start()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.Shutdown(ctx)
	})
	return p
}

func cfg() Config {
	return Config{QueueCapacity: 16, Workers: 1, BatchSize: 100, FlushInterval: 20 * time.Millisecond, WriteTimeout: 5 * time.Second}
}

func TestConfigValidate(t *testing.T) {
	bad := []Config{
		{Workers: 1, BatchSize: 1, FlushInterval: 1, WriteTimeout: 1},
		{QueueCapacity: 1, BatchSize: 1, FlushInterval: 1, WriteTimeout: 1},
		{QueueCapacity: 1, Workers: 1, FlushInterval: 1, WriteTimeout: 1},
		{QueueCapacity: 1, Workers: 1, BatchSize: 1, WriteTimeout: 1},
		{QueueCapacity: 1, Workers: 1, BatchSize: 1, FlushInterval: 1},
	}
	for i, c := range bad {
		if c.Validate() == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	if err := cfg().Validate(); err != nil {
		t.Fatal(err)
	}
}

// The core durability guarantee: Submit must not return success until the
// writer has committed.
func TestSubmitWaitsForCommit(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	p := newTestPipeline(t, cfg(), w)

	result := make(chan error, 1)
	go func() { result <- p.Submit(context.Background(), spans(3, 1)) }()

	<-w.started
	select {
	case err := <-result:
		t.Fatalf("Submit returned (%v) before the write committed", err)
	case <-time.After(50 * time.Millisecond):
	}

	w.gate <- struct{}{}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if _, n := w.committed(); n != 3 {
		t.Fatalf("committed %d spans", n)
	}
}

func TestSubmitEmptyIsNoop(t *testing.T) {
	w := &fakeWriter{}
	p := newTestPipeline(t, cfg(), w)
	if err := p.Submit(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if b, _ := w.committed(); b != 0 {
		t.Fatal("empty submit should not write")
	}
}

func TestBatchesCoalesceConcurrentRequests(t *testing.T) {
	c := cfg()
	c.BatchSize = 50
	c.FlushInterval = time.Second // only size should trigger the flush
	w := &fakeWriter{}
	p := newTestPipeline(t, c, w)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := p.Submit(context.Background(), spans(5, byte(i))); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	batches, n := w.committed()
	if n != 50 {
		t.Fatalf("committed %d spans, want 50", n)
	}
	if batches != 1 {
		t.Fatalf("expected 10 requests to coalesce into 1 batch, got %d", batches)
	}
}

func TestPartialBatchFlushesOnInterval(t *testing.T) {
	c := cfg()
	c.BatchSize = 1000
	c.FlushInterval = 30 * time.Millisecond
	p := newTestPipeline(t, c, &fakeWriter{})

	start := time.Now()
	if err := p.Submit(context.Background(), spans(1, 1)); err != nil {
		t.Fatal(err)
	}
	if el := time.Since(start); el < c.FlushInterval || el > c.FlushInterval+500*time.Millisecond {
		t.Fatalf("flush after %s, want about %s", el, c.FlushInterval)
	}
}

func TestOverloadFailsFast(t *testing.T) {
	c := cfg()
	c.QueueCapacity = 1
	c.BatchSize = 1
	w := &fakeWriter{gate: make(chan struct{}), started: make(chan struct{}, 4)}
	p := newTestPipeline(t, c, w)

	errs := make(chan error, 2)
	go func() { errs <- p.Submit(context.Background(), spans(1, 1)) }()
	<-w.started // worker holds request 1 inside a write
	go func() { errs <- p.Submit(context.Background(), spans(1, 2)) }()
	waitFor(t, func() bool { return p.QueueDepth() == 1 }) // request 2 fills the queue

	start := time.Now()
	err := p.Submit(context.Background(), spans(1, 3))
	if !errors.Is(err, ErrOverloaded) {
		t.Fatalf("err = %v, want ErrOverloaded", err)
	}
	if time.Since(start) > 50*time.Millisecond {
		t.Fatal("overload rejection should not block")
	}

	w.gate <- struct{}{}
	<-w.started
	w.gate <- struct{}{}
	for i := 0; i < 2; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestSubmitContextCancelledStillWrites(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	p := newTestPipeline(t, cfg(), w)

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- p.Submit(ctx, spans(2, 1)) }()
	<-w.started
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	// The write still completes (and is idempotent in real storage).
	w.gate <- struct{}{}
	waitFor(t, func() bool { _, n := w.committed(); return n == 2 })
}

func TestShutdownDrainsQueue(t *testing.T) {
	c := cfg()
	c.Workers = 2
	c.BatchSize = 1
	var writes atomic.Int64
	w := &fakeWriter{fail: func(s []model.Span) error { writes.Add(1); time.Sleep(5 * time.Millisecond); return nil }}
	p, err := New(c, w, metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	p.Start()

	var wg sync.WaitGroup
	var ok atomic.Int64
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if p.Submit(context.Background(), spans(1, byte(i))) == nil {
				ok.Add(1)
			}
		}(i)
	}
	waitFor(t, func() bool { return writes.Load() > 0 })

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	_, committed := w.committed()
	if int64(committed) != ok.Load() {
		t.Fatalf("acknowledged %d but committed %d", ok.Load(), committed)
	}
	if p.Accepting() {
		t.Fatal("pipeline should not accept after shutdown")
	}
	if err := p.Submit(context.Background(), spans(1, 99)); !errors.Is(err, ErrShuttingDown) {
		t.Fatalf("err = %v, want ErrShuttingDown", err)
	}
	// Shutdown is idempotent.
	if err := p.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestShutdownTimeout(t *testing.T) {
	w := &fakeWriter{gate: make(chan struct{}), started: make(chan struct{}, 1)}
	p, _ := New(cfg(), w, metrics.New(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.Start()
	go func() { _ = p.Submit(context.Background(), spans(1, 1)) }()
	<-w.started

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := p.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", err)
	}
	close(w.gate) // let the worker finish
}

// One bad request in a coalesced batch must not fail its neighbours.
func TestFailedBatchIsolatesBadRequest(t *testing.T) {
	c := cfg()
	c.BatchSize = 30
	c.FlushInterval = time.Second
	poison := errors.New("poison")
	w := &fakeWriter{fail: func(s []model.Span) error {
		for _, sp := range s {
			if sp.SpanID[0] == 0xBA {
				return poison
			}
		}
		return nil
	}}
	p := newTestPipeline(t, c, w)

	results := make([]error, 3)
	var wg sync.WaitGroup
	for i, tag := range []byte{1, 0xBA, 2} {
		wg.Add(1)
		go func(i int, tag byte) {
			defer wg.Done()
			results[i] = p.Submit(context.Background(), spans(10, tag))
		}(i, tag)
	}
	wg.Wait()

	if results[0] != nil || results[2] != nil {
		t.Fatalf("good requests failed: %v", results)
	}
	if !errors.Is(results[1], poison) {
		t.Fatalf("bad request err = %v", results[1])
	}
	if _, n := w.committed(); n != 20 {
		t.Fatalf("committed %d spans, want 20", n)
	}
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(time.Millisecond)
	}
}
