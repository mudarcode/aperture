package otlpingest

import (
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/grpc/codes"
	"google.golang.org/protobuf/proto"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"

	"github.com/mudarcode/aperture/internal/metrics"
	"github.com/mudarcode/aperture/internal/model"
	"github.com/mudarcode/aperture/internal/pipeline"
)

const (
	// ContentTypeProtobuf is the OTLP/HTTP binary protobuf media type.
	ContentTypeProtobuf = "application/x-protobuf"
	// TracesPath is the default OTLP/HTTP traces endpoint.
	TracesPath = "/v1/traces"
)

// Submitter is the subset of the pipeline the handler depends on.
type Submitter interface {
	Submit(ctx context.Context, spans []model.Span) error
}

// HandlerConfig bounds request handling.
type HandlerConfig struct {
	MaxRequestBytes      int64         // bytes on the wire (possibly compressed)
	MaxDecompressedBytes int64         // bytes after gzip decoding
	MaxSpansPerRequest   int           // larger requests are refused with 413
	AckTimeout           time.Duration // max wait for durable commit before answering 503
	RetryAfter           time.Duration // advertised on retryable failures
}

// Handler serves OTLP/HTTP trace exports (binary protobuf encoding).
//
// Status codes follow the OTLP/HTTP specification:
//
//	200  all spans committed, or some rejected (partial_success is set)
//	400  undecodable payload, or data the database permanently refused
//	405  method other than POST
//	413  body or span count over the configured limits
//	415  unsupported Content-Type or Content-Encoding
//	503  queue full, shutting down, database unavailable, or commit not
//	     confirmed within AckTimeout; carries Retry-After (retryable)
//
// Error bodies are google.rpc.Status messages encoded as protobuf.
type Handler struct {
	cfg       HandlerConfig
	converter *Converter
	sub       Submitter
	m         *metrics.Metrics
	log       *slog.Logger
}

// NewHandler constructs a Handler.
func NewHandler(cfg HandlerConfig, conv *Converter, sub Submitter, m *metrics.Metrics, log *slog.Logger) *Handler {
	return &Handler{cfg: cfg, converter: conv, sub: sub, m: m, log: log}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	code := h.serve(w, r)
	h.m.Requests.WithLabelValues(strconv.Itoa(code)).Inc()
	h.m.RequestDuration.Observe(time.Since(start).Seconds())
}

func (h *Handler) serve(w http.ResponseWriter, r *http.Request) int {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		return h.fail(w, http.StatusMethodNotAllowed, codes.Unimplemented, "only POST is supported")
	}

	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != ContentTypeProtobuf {
		return h.fail(w, http.StatusUnsupportedMediaType, codes.InvalidArgument,
			fmt.Sprintf("unsupported Content-Type %q; only %s is supported", r.Header.Get("Content-Type"), ContentTypeProtobuf))
	}

	body, status, msg := h.readBody(w, r)
	if status != 0 {
		grpcCode := codes.InvalidArgument
		if status == http.StatusRequestEntityTooLarge {
			grpcCode = codes.ResourceExhausted
		}
		return h.fail(w, status, grpcCode, msg)
	}

	var req coltracepb.ExportTraceServiceRequest
	if err := proto.Unmarshal(body, &req); err != nil {
		return h.fail(w, http.StatusBadRequest, codes.InvalidArgument, "malformed protobuf payload: "+err.Error())
	}

	total := 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			total += len(ss.GetSpans())
		}
	}
	if total > h.cfg.MaxSpansPerRequest {
		return h.fail(w, http.StatusRequestEntityTooLarge, codes.ResourceExhausted,
			fmt.Sprintf("request has %d spans; the limit is %d", total, h.cfg.MaxSpansPerRequest))
	}
	h.m.SpansReceived.Add(float64(total))

	res := h.converter.Convert(&req)
	for reason, n := range res.Reasons {
		h.m.SpansRejected.WithLabelValues(string(reason)).Add(float64(n))
	}

	if len(res.Spans) > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), h.cfg.AckTimeout)
		err := h.sub.Submit(ctx, res.Spans)
		cancel()
		if err != nil {
			return h.submitFailed(w, r, err, len(res.Spans))
		}
		h.m.SpansAccepted.Add(float64(len(res.Spans)))
	}

	resp := &coltracepb.ExportTraceServiceResponse{}
	if res.Rejected > 0 {
		resp.PartialSuccess = &coltracepb.ExportTracePartialSuccess{
			RejectedSpans: res.Rejected,
			ErrorMessage:  res.ErrorMessage(),
		}
		h.log.Debug("partial success", "accepted", len(res.Spans), "rejected", res.Rejected, "reasons", res.ErrorMessage())
	}
	return h.write(w, http.StatusOK, resp)
}

// readBody enforces size limits on both the wire body and the decompressed
// payload (guarding against decompression bombs).
func (h *Handler) readBody(w http.ResponseWriter, r *http.Request) ([]byte, int, string) {
	if r.ContentLength > h.cfg.MaxRequestBytes {
		return nil, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("request body of %d bytes exceeds the %d byte limit", r.ContentLength, h.cfg.MaxRequestBytes)
	}
	var src io.Reader = http.MaxBytesReader(w, r.Body, h.cfg.MaxRequestBytes)

	switch enc := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding"))); enc {
	case "", "identity":
	case "gzip":
		gz, err := gzip.NewReader(src)
		if err != nil {
			if isMaxBytes(err) {
				return nil, http.StatusRequestEntityTooLarge, "request body exceeds the configured limit"
			}
			return nil, http.StatusBadRequest, "invalid gzip body: " + err.Error()
		}
		defer gz.Close()
		src = gz
	default:
		return nil, http.StatusUnsupportedMediaType, fmt.Sprintf("unsupported Content-Encoding %q", enc)
	}

	limit := h.cfg.MaxDecompressedBytes
	body, err := io.ReadAll(io.LimitReader(src, limit+1))
	switch {
	case isMaxBytes(err):
		return nil, http.StatusRequestEntityTooLarge, "request body exceeds the configured limit"
	case err != nil:
		return nil, http.StatusBadRequest, "failed to read body: " + err.Error()
	case int64(len(body)) > limit:
		return nil, http.StatusRequestEntityTooLarge,
			fmt.Sprintf("decoded payload exceeds the %d byte limit", limit)
	}
	return body, 0, ""
}

func isMaxBytes(err error) bool {
	var mbe *http.MaxBytesError
	return errors.As(err, &mbe)
}

func (h *Handler) submitFailed(w http.ResponseWriter, r *http.Request, err error, n int) int {
	switch {
	case errors.Is(err, pipeline.ErrPermanent):
		h.log.Error("spans permanently refused by storage", "spans", n, "err", err)
		return h.fail(w, http.StatusBadRequest, codes.InvalidArgument, "storage rejected the data")
	case errors.Is(err, pipeline.ErrOverloaded):
		return h.retryable(w, codes.Unavailable, "ingestion queue is full; retry later")
	case errors.Is(err, pipeline.ErrShuttingDown):
		return h.retryable(w, codes.Unavailable, "server is shutting down")
	case errors.Is(err, context.DeadlineExceeded) && r.Context().Err() == nil:
		h.log.Warn("commit not confirmed within ack timeout", "spans", n, "timeout", h.cfg.AckTimeout)
		return h.retryable(w, codes.Unavailable, "persistence not confirmed in time; retry (writes are idempotent)")
	case r.Context().Err() != nil:
		// Client went away; the response will not be read.
		return 499
	default:
		h.log.Error("storage write failed", "spans", n, "err", err)
		return h.retryable(w, codes.Unavailable, "storage temporarily unavailable")
	}
}

func (h *Handler) retryable(w http.ResponseWriter, c codes.Code, msg string) int {
	secs := int(h.cfg.RetryAfter.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(secs))
	return h.fail(w, http.StatusServiceUnavailable, c, msg)
}

func (h *Handler) fail(w http.ResponseWriter, httpStatus int, c codes.Code, msg string) int {
	return h.write(w, httpStatus, &statuspb.Status{Code: int32(c), Message: msg})
}

func (h *Handler) write(w http.ResponseWriter, httpStatus int, msg proto.Message) int {
	b, err := proto.Marshal(msg)
	if err != nil { // cannot happen for these message types
		h.log.Error("marshal response", "err", err)
		w.WriteHeader(http.StatusInternalServerError)
		return http.StatusInternalServerError
	}
	w.Header().Set("Content-Type", ContentTypeProtobuf)
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(httpStatus)
	_, _ = w.Write(b)
	return httpStatus
}
