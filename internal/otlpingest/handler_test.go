package otlpingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/model"
	"github.com/mudarcode/aperture/internal/pipeline"
)

type fakeSubmitter struct {
	mu    sync.Mutex
	calls [][]model.Span
	err   error
	block bool // wait for ctx to end, simulating a slow commit
}

func (f *fakeSubmitter) Submit(ctx context.Context, spans []model.Span) error {
	f.mu.Lock()
	f.calls = append(f.calls, spans)
	err, block := f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeSubmitter) submitted() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		n += len(c)
	}
	return n
}

func testHandler(sub Submitter) *Handler {
	cfg := HandlerConfig{
		MaxRequestBytes:      64 << 10,
		MaxDecompressedBytes: 256 << 10,
		MaxSpansPerRequest:   100,
		AckTimeout:           200 * time.Millisecond,
		RetryAfter:           2 * time.Second,
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return NewHandler(cfg, testConverter(), sub, metrics.New(), log)
}

func marshal(t *testing.T, m proto.Message) []byte {
	t.Helper()
	b, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func post(h http.Handler, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, TracesPath, bytes.NewReader(body))
	req.Header.Set("Content-Type", ContentTypeProtobuf)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeStatus(t *testing.T, rec *httptest.ResponseRecorder) *statuspb.Status {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != ContentTypeProtobuf {
		t.Fatalf("Content-Type = %q", ct)
	}
	var st statuspb.Status
	if err := proto.Unmarshal(rec.Body.Bytes(), &st); err != nil {
		t.Fatalf("body is not a google.rpc.Status: %v", err)
	}
	return &st
}

func decodeExportResponse(t *testing.T, rec *httptest.ResponseRecorder) *coltracepb.ExportTraceServiceResponse {
	t.Helper()
	var resp coltracepb.ExportTraceServiceResponse
	if err := proto.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("body is not an ExportTraceServiceResponse: %v", err)
	}
	return &resp
}

func TestHandlerAcceptsValidRequest(t *testing.T) {
	sub := &fakeSubmitter{}
	rec := post(testHandler(sub), marshal(t, request("svc", validSpan())), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if resp := decodeExportResponse(t, rec); resp.PartialSuccess != nil {
		t.Fatalf("partial_success must be unset on full success, got %v", resp.PartialSuccess)
	}
	if sub.submitted() != 1 {
		t.Fatalf("submitted %d spans", sub.submitted())
	}
}

func TestHandlerContentTypeWithParameters(t *testing.T) {
	rec := post(testHandler(&fakeSubmitter{}), marshal(t, request("svc", validSpan())),
		map[string]string{"Content-Type": "application/x-protobuf; charset=binary"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandlerGzip(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(marshal(t, request("svc", validSpan())))
	_ = gz.Close()
	sub := &fakeSubmitter{}
	rec := post(testHandler(sub), buf.Bytes(), map[string]string{"Content-Encoding": "gzip"})
	if rec.Code != http.StatusOK || sub.submitted() != 1 {
		t.Fatalf("status = %d submitted = %d", rec.Code, sub.submitted())
	}
}

func TestHandlerPartialSuccess(t *testing.T) {
	bad := validSpan()
	bad.TraceId = nil
	sub := &fakeSubmitter{}
	rec := post(testHandler(sub), marshal(t, request("svc", validSpan(), bad)), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	ps := decodeExportResponse(t, rec).PartialSuccess
	if ps == nil || ps.RejectedSpans != 1 || !strings.Contains(ps.ErrorMessage, "invalid_trace_id=1") {
		t.Fatalf("partial_success = %v", ps)
	}
	if sub.submitted() != 1 {
		t.Fatalf("valid span not submitted")
	}
}

func TestHandlerAllRejectedDoesNotSubmit(t *testing.T) {
	bad := validSpan()
	bad.SpanId = nil
	sub := &fakeSubmitter{}
	rec := post(testHandler(sub), marshal(t, request("svc", bad)), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if ps := decodeExportResponse(t, rec).PartialSuccess; ps == nil || ps.RejectedSpans != 1 {
		t.Fatalf("partial_success = %v", ps)
	}
	if len(sub.calls) != 0 {
		t.Fatal("submitter should not be called when nothing is valid")
	}
}

func TestHandlerEmptyRequest(t *testing.T) {
	rec := post(testHandler(&fakeSubmitter{}), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandlerClientErrors(t *testing.T) {
	valid := func(t *testing.T) []byte { return marshal(t, request("svc", validSpan())) }
	tooMany := func(t *testing.T) []byte {
		spans := make([]*tracepb.Span, 101)
		for i := range spans {
			spans[i] = validSpan()
		}
		return marshal(t, request("svc", spans...))
	}
	cases := []struct {
		name    string
		method  string
		body    func(*testing.T) []byte
		headers map[string]string
		want    int
	}{
		{"GET not allowed", http.MethodGet, valid, nil, http.StatusMethodNotAllowed},
		{"JSON not supported", "", valid, map[string]string{"Content-Type": "application/json"}, http.StatusUnsupportedMediaType},
		{"missing content type", "", valid, map[string]string{"Content-Type": ""}, http.StatusUnsupportedMediaType},
		{"unknown encoding", "", valid, map[string]string{"Content-Encoding": "br"}, http.StatusUnsupportedMediaType},
		{"malformed protobuf", "", func(*testing.T) []byte { return []byte{0xff, 0xff, 0xff, 0x01} }, nil, http.StatusBadRequest},
		{"corrupt gzip", "", func(*testing.T) []byte { return []byte("not gzip") }, map[string]string{"Content-Encoding": "gzip"}, http.StatusBadRequest},
		{"body over limit", "", func(*testing.T) []byte { return make([]byte, 65<<10) }, nil, http.StatusRequestEntityTooLarge},
		{"too many spans", "", tooMany, nil, http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sub := &fakeSubmitter{}
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, TracesPath, bytes.NewReader(tc.body(t)))
			req.Header.Set("Content-Type", ContentTypeProtobuf)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			testHandler(sub).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tc.want, rec.Body.String())
			}
			if st := decodeStatus(t, rec); st.Message == "" {
				t.Fatal("error status must carry a message")
			}
			if len(sub.calls) != 0 {
				t.Fatal("nothing should be submitted on client errors")
			}
		})
	}
}

// A chunked body without Content-Length must still be bounded.
func TestHandlerStreamingBodyOverLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, TracesPath, io.LimitReader(zeroReader{}, 1<<20))
	req.ContentLength = -1
	req.Header.Set("Content-Type", ContentTypeProtobuf)
	rec := httptest.NewRecorder()
	testHandler(&fakeSubmitter{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

// A small gzip body that inflates past MaxDecompressedBytes is refused.
func TestHandlerGzipBomb(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	_, _ = gz.Write(make([]byte, 1<<20)) // 1 MiB of zeros compresses to ~1 KiB
	_ = gz.Close()
	if buf.Len() > 64<<10 {
		t.Fatalf("test setup: compressed size %d exceeds wire limit", buf.Len())
	}
	rec := post(testHandler(&fakeSubmitter{}), buf.Bytes(), map[string]string{"Content-Encoding": "gzip"})
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestHandlerSubmitFailures(t *testing.T) {
	cases := []struct {
		name       string
		sub        *fakeSubmitter
		want       int
		retryAfter bool
	}{
		{"overloaded", &fakeSubmitter{err: pipeline.ErrOverloaded}, http.StatusServiceUnavailable, true},
		{"shutting down", &fakeSubmitter{err: pipeline.ErrShuttingDown}, http.StatusServiceUnavailable, true},
		{"database down", &fakeSubmitter{err: errors.New("connection refused")}, http.StatusServiceUnavailable, true},
		{"ack timeout", &fakeSubmitter{block: true}, http.StatusServiceUnavailable, true},
		{"permanent", &fakeSubmitter{err: fmt.Errorf("x: %w", pipeline.ErrPermanent)}, http.StatusBadRequest, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(testHandler(tc.sub), marshal(t, request("svc", validSpan())), nil)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d", rec.Code, tc.want)
			}
			decodeStatus(t, rec)
			if got := rec.Header().Get("Retry-After"); (got != "") != tc.retryAfter {
				t.Fatalf("Retry-After = %q", got)
			}
			if tc.retryAfter && rec.Header().Get("Retry-After") != "2" {
				t.Fatalf("Retry-After = %q, want 2", rec.Header().Get("Retry-After"))
			}
		})
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}
