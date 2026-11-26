// Command loadgen is Aperture's reproducible ingestion benchmark.
//
// It sends OTLP/HTTP protobuf export requests straight to Aperture's
// /v1/traces endpoint from a configurable number of concurrent clients for a
// fixed duration, measures acknowledgment latency and throughput, and (when
// given a database URL) verifies that every acknowledged span was stored.
//
// Request bodies are encoded by separate goroutines into a buffer, so senders
// mostly just send; every body gets fresh random trace and span IDs, so no
// two requests collide. Measurements start after a warm-up period.
//
//	loadgen -url http://localhost:8080 -clients 8 -duration 30s -spans 100
package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

type config struct {
	URL           string
	Clients       int
	Duration      time.Duration
	Warmup        time.Duration
	SpansPerReq   int
	SpansPerTrace int
	Attributes    int
	Gzip          bool
	DatabaseURL   string
	Label         string
	JSONOut       string
}

type result struct {
	Label            string             `json:"label"`
	Config           config             `json:"config"`
	Environment      map[string]string  `json:"environment"`
	ElapsedSeconds   float64            `json:"elapsedSeconds"`
	Requests         int64              `json:"requests"`
	AckedRequests    int64              `json:"ackedRequests"`
	AckedSpans       int64              `json:"ackedSpans"`
	StatusCounts     map[string]int64   `json:"statusCounts"`
	TransportErrors  int64              `json:"transportErrors"`
	SpansPerSecond   float64            `json:"spansPerSecond"`
	RequestsPerSec   float64            `json:"requestsPerSecond"`
	LatencyMs        map[string]float64 `json:"ackLatencyMs"`
	RejectLatencyMs  map[string]float64 `json:"rejectLatencyMs,omitempty"`
	RequestBytes     int                `json:"requestBytesOnWire"`
	StoredSpans      *int64             `json:"storedSpans,omitempty"`
	DurabilityVerify string             `json:"durabilityCheck,omitempty"`
}

func main() {
	var c config
	flag.StringVar(&c.URL, "url", "http://localhost:8080", "Aperture base URL")
	flag.IntVar(&c.Clients, "clients", 8, "concurrent clients (each sends one request at a time)")
	flag.DurationVar(&c.Duration, "duration", 30*time.Second, "measured duration")
	flag.DurationVar(&c.Warmup, "warmup", 5*time.Second, "unmeasured warm-up before the measured phase")
	flag.IntVar(&c.SpansPerReq, "spans", 100, "spans per export request")
	flag.IntVar(&c.SpansPerTrace, "spans-per-trace", 10, "spans per trace inside a request")
	flag.IntVar(&c.Attributes, "attributes", 8, "attributes per span")
	flag.BoolVar(&c.Gzip, "gzip", true, "gzip request bodies (the Collector default)")
	flag.StringVar(&c.DatabaseURL, "verify-db", "", "PostgreSQL URL; if set, checks that every acknowledged span is stored")
	flag.StringVar(&c.Label, "label", "", "scenario name for the report")
	flag.StringVar(&c.JSONOut, "json", "", "write the result as JSON to this file")
	flag.Parse()

	if err := run(c); err != nil {
		fmt.Fprintln(os.Stderr, "loadgen:", err)
		os.Exit(1)
	}
}

// template is a pre-built request whose IDs are rewritten per send.
type template struct {
	req     *coltracepb.ExportTraceServiceRequest
	spans   []*tracepb.Span
	traceOf []int // trace index per span
	traces  int
}

func newTemplate(c config) *template {
	now := uint64(time.Now().UnixNano())
	t := &template{traces: (c.SpansPerReq + c.SpansPerTrace - 1) / c.SpansPerTrace}
	ss := &tracepb.ScopeSpans{Scope: &commonpb.InstrumentationScope{Name: "aperture/loadgen", Version: "1"}}
	for i := 0; i < c.SpansPerReq; i++ {
		attrs := make([]*commonpb.KeyValue, c.Attributes)
		for a := range attrs {
			attrs[a] = &commonpb.KeyValue{Key: fmt.Sprintf("attr.%d", a), Value: &commonpb.AnyValue{
				Value: &commonpb.AnyValue_StringValue{StringValue: fmt.Sprintf("value-%d-%d", i, a)}}}
		}
		start := now - uint64(time.Second) + uint64(i)*uint64(time.Microsecond)
		sp := &tracepb.Span{
			Name:              fmt.Sprintf("operation-%d", i%20),
			Kind:              tracepb.Span_SPAN_KIND_SERVER,
			StartTimeUnixNano: start,
			EndTimeUnixNano:   start + uint64(2*time.Millisecond),
			Attributes:        attrs,
			Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_UNSET},
		}
		ss.Spans = append(ss.Spans, sp)
		t.spans = append(t.spans, sp)
		t.traceOf = append(t.traceOf, i/c.SpansPerTrace)
	}
	t.req = &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: []*commonpb.KeyValue{
			{Key: "service.name", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "loadgen"}}},
		}},
		ScopeSpans: []*tracepb.ScopeSpans{ss},
	}}}
	return t
}

// encode assigns fresh random IDs (first span of each trace is its root)
// and returns the wire body.
func (t *template) encode(gz bool) ([]byte, error) {
	traceIDs := make([][]byte, t.traces)
	for i := range traceIDs {
		traceIDs[i] = randBytes(16)
	}
	var lastRoot []byte
	for i, sp := range t.spans {
		sp.TraceId = traceIDs[t.traceOf[i]]
		sp.SpanId = randBytes(8)
		if i == 0 || t.traceOf[i] != t.traceOf[i-1] {
			sp.ParentSpanId = nil
			lastRoot = sp.SpanId
		} else {
			sp.ParentSpanId = lastRoot
		}
	}
	raw, err := proto.Marshal(t.req)
	if err != nil || !gz {
		return raw, err
	}
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(raw)
	_ = w.Close()
	return buf.Bytes(), nil
}

func randBytes(n int) []byte {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return b
}

func run(c config) error {
	if c.Clients < 1 || c.SpansPerReq < 1 || c.SpansPerTrace < 1 || c.Duration <= 0 {
		return fmt.Errorf("clients, spans, spans-per-trace and duration must be positive")
	}
	ctx := context.Background()

	var before int64
	if c.DatabaseURL != "" {
		n, err := countSpans(ctx, c.DatabaseURL)
		if err != nil {
			return err
		}
		before = n
	}

	// Encoder goroutines keep a buffer of ready bodies so encoding is mostly
	// off the senders' hot path. Every body has fresh IDs: no duplicates.
	tmpl := newTemplate(c)
	sample, err := tmpl.encode(c.Gzip)
	if err != nil {
		return err
	}

	client := &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{MaxIdleConnsPerHost: c.Clients * 2, MaxConnsPerHost: c.Clients * 2},
	}
	endpoint := c.URL + "/v1/traces"

	var (
		measuring   atomic.Bool
		stop        atomic.Bool
		requests    atomic.Int64
		acked       atomic.Int64
		ackedSpans  atomic.Int64
		allAcked    atomic.Int64 // includes warm-up, for the durability check
		transport   atomic.Int64
		statusMu    sync.Mutex
		statusCount = map[string]int64{}
		latMu       sync.Mutex
		latencies   []float64
		rejects     []float64
	)

	// Body pipeline: a few encoder goroutines keep a buffered channel full.
	bodies := make(chan []byte, c.Clients*4)
	encCtx, stopEnc := context.WithCancel(ctx)
	defer stopEnc()
	for i := 0; i < max(2, runtime.NumCPU()/4); i++ {
		own := newTemplate(c) // templates are mutated per encode, so one each
		go func() {
			for {
				b, err := own.encode(c.Gzip)
				if err != nil {
					return
				}
				select {
				case bodies <- b:
				case <-encCtx.Done():
					return
				}
			}
		}()
	}

	var wg sync.WaitGroup
	for i := 0; i < c.Clients; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			local := make([]float64, 0, 4096)
			var localRej []float64
			for !stop.Load() {
				body := <-bodies
				req, _ := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
				req.Header.Set("Content-Type", "application/x-protobuf")
				if c.Gzip {
					req.Header.Set("Content-Encoding", "gzip")
				}
				t0 := time.Now()
				resp, err := client.Do(req)
				lat := time.Since(t0)
				m := measuring.Load()
				if err != nil {
					if m {
						transport.Add(1)
					}
					continue
				}
				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					allAcked.Add(int64(c.SpansPerReq))
				}
				if !m {
					continue
				}
				requests.Add(1)
				statusMu.Lock()
				statusCount[fmt.Sprint(resp.StatusCode)]++
				statusMu.Unlock()
				if resp.StatusCode == http.StatusOK {
					acked.Add(1)
					ackedSpans.Add(int64(c.SpansPerReq))
					local = append(local, float64(lat.Microseconds())/1000)
				} else if resp.StatusCode == http.StatusServiceUnavailable {
					localRej = append(localRej, float64(lat.Microseconds())/1000)
					time.Sleep(10 * time.Millisecond) // a polite client backs off briefly
				}
			}
			latMu.Lock()
			latencies = append(latencies, local...)
			rejects = append(rejects, localRej...)
			latMu.Unlock()
		}()
	}

	fmt.Fprintf(os.Stderr, "warming up for %s...\n", c.Warmup)
	time.Sleep(c.Warmup)
	measuring.Store(true)
	start := time.Now()
	fmt.Fprintf(os.Stderr, "measuring for %s with %d clients, %d spans/request...\n", c.Duration, c.Clients, c.SpansPerReq)
	time.Sleep(c.Duration)
	measuring.Store(false)
	elapsed := time.Since(start)
	stop.Store(true)
	wg.Wait()
	stopEnc()

	sort.Float64s(latencies)
	sort.Float64s(rejects)
	res := result{
		Label:           c.Label,
		Config:          c,
		Environment:     environment(ctx, c),
		ElapsedSeconds:  elapsed.Seconds(),
		Requests:        requests.Load(),
		AckedRequests:   acked.Load(),
		AckedSpans:      ackedSpans.Load(),
		StatusCounts:    statusCount,
		TransportErrors: transport.Load(),
		SpansPerSecond:  float64(ackedSpans.Load()) / elapsed.Seconds(),
		RequestsPerSec:  float64(acked.Load()) / elapsed.Seconds(),
		LatencyMs: map[string]float64{
			"p50": pct(latencies, 0.50), "p90": pct(latencies, 0.90), "p95": pct(latencies, 0.95),
			"p99": pct(latencies, 0.99), "max": pct(latencies, 1),
		},
		RequestBytes: len(sample),
	}
	if len(rejects) > 0 {
		res.RejectLatencyMs = map[string]float64{"p50": pct(rejects, 0.50), "p99": pct(rejects, 0.99)}
	}
	res.Config.DatabaseURL = "" // never write credentials into reports

	if c.DatabaseURL != "" {
		after, err := countSpans(ctx, c.DatabaseURL)
		if err != nil {
			return err
		}
		stored := after - before
		res.StoredSpans = &stored
		switch {
		case stored == allAcked.Load():
			res.DurabilityVerify = fmt.Sprintf("pass: %d acknowledged spans, %d stored", allAcked.Load(), stored)
		case stored > allAcked.Load():
			// Possible and correct: a request can commit after the client gave up on it.
			res.DurabilityVerify = fmt.Sprintf("pass: %d acknowledged, %d stored (extra spans committed after a client timeout)", allAcked.Load(), stored)
		default:
			res.DurabilityVerify = fmt.Sprintf("FAIL: %d acknowledged but only %d stored", allAcked.Load(), stored)
		}
	}

	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	if c.JSONOut != "" {
		if err := os.WriteFile(c.JSONOut, append(out, '\n'), 0o644); err != nil {
			return err
		}
	}
	if res.StoredSpans != nil && *res.StoredSpans < allAcked.Load() {
		return fmt.Errorf("durability check failed")
	}
	return nil
}

func pct(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	i := int(math.Ceil(p*float64(len(sorted)))) - 1
	i = max(0, min(i, len(sorted)-1))
	return math.Round(sorted[i]*100) / 100
}

func countSpans(ctx context.Context, url string) (int64, error) {
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return 0, fmt.Errorf("verify-db: %w", err)
	}
	defer conn.Close(ctx)
	var n int64
	err = conn.QueryRow(ctx, "SELECT count(*) FROM spans").Scan(&n)
	return n, err
}

func environment(ctx context.Context, c config) map[string]string {
	env := map[string]string{
		"loadgenGo": runtime.Version(),
		"os":        runtime.GOOS + "/" + runtime.GOARCH,
		"cpus":      fmt.Sprint(runtime.NumCPU()),
	}
	if b, err := os.ReadFile("/proc/cpuinfo"); err == nil {
		for _, line := range bytes.Split(b, []byte("\n")) {
			if k, v, ok := bytes.Cut(line, []byte(":")); ok && bytes.Equal(bytes.TrimSpace(k), []byte("model name")) {
				env["cpuModel"] = string(bytes.TrimSpace(v))
				break
			}
		}
	}
	if b, err := os.ReadFile("/proc/meminfo"); err == nil {
		line, _, _ := bytes.Cut(b, []byte("\n"))
		env["memory"] = string(bytes.Join(bytes.Fields(line)[1:], []byte(" ")))
	}
	if c.DatabaseURL != "" {
		if conn, err := pgx.Connect(ctx, c.DatabaseURL); err == nil {
			var v string
			if conn.QueryRow(ctx, "SHOW server_version").Scan(&v) == nil {
				env["postgres"] = v
			}
			conn.Close(ctx)
		}
	}
	return env
}
