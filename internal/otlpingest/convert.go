// Package otlpingest converts and validates OTLP trace export requests.
package otlpingest

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"

	"github.com/mudarcode/aperture/internal/model"
)

// UnknownServiceName is used when a resource has no service.name attribute,
// matching the OpenTelemetry SDK default.
const UnknownServiceName = "unknown_service"

// Limits bounds what a single span may contain. A span exceeding any limit is
// rejected (not truncated) and reported through OTLP partial success.
type Limits struct {
	MaxAttributesPerSpan   int           // span attributes; resource attributes are bounded separately
	MaxResourceAttributes  int           //
	MaxAttributeValueBytes int           // max bytes of any string/bytes attribute value
	MaxEventsPerSpan       int           //
	MaxLinksPerSpan        int           //
	MaxNameBytes           int           // span name and service name
	MaxFutureSkew          time.Duration // how far in the future a span may end
	MaxSpanDuration        time.Duration // longest accepted span
}

// DefaultLimits returns conservative limits suitable for local use.
func DefaultLimits() Limits {
	return Limits{
		MaxAttributesPerSpan:   128,
		MaxResourceAttributes:  128,
		MaxAttributeValueBytes: 16 * 1024,
		MaxEventsPerSpan:       128,
		MaxLinksPerSpan:        128,
		MaxNameBytes:           1024,
		MaxFutureSkew:          time.Hour,
		MaxSpanDuration:        24 * time.Hour,
	}
}

// RejectReason classifies why a span was rejected. Values are stable and are
// used as Prometheus label values.
type RejectReason string

const (
	ReasonInvalidTraceID   RejectReason = "invalid_trace_id"
	ReasonInvalidSpanID    RejectReason = "invalid_span_id"
	ReasonInvalidParentID  RejectReason = "invalid_parent_span_id"
	ReasonInvalidTimestamp RejectReason = "invalid_timestamp"
	ReasonInvalidKind      RejectReason = "invalid_kind"
	ReasonInvalidStatus    RejectReason = "invalid_status"
	ReasonLimitExceeded    RejectReason = "limit_exceeded"
	ReasonInvalidString    RejectReason = "invalid_string"
)

// Result is the outcome of converting one export request.
type Result struct {
	Spans    []model.Span
	Rejected int64
	// Reasons counts rejected spans by reason.
	Reasons map[RejectReason]int64
}

// ErrorMessage renders a deterministic, human-readable partial-success
// message, e.g. "rejected 3 spans: invalid_span_id=2, invalid_timestamp=1".
func (r *Result) ErrorMessage() string {
	if r.Rejected == 0 {
		return ""
	}
	keys := make([]string, 0, len(r.Reasons))
	for k := range r.Reasons {
		keys = append(keys, string(k))
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%d", k, r.Reasons[RejectReason(k)])
	}
	return fmt.Sprintf("rejected %d spans: %s", r.Rejected, strings.Join(parts, ", "))
}

func (r *Result) reject(reason RejectReason) {
	if r.Reasons == nil {
		r.Reasons = make(map[RejectReason]int64)
	}
	r.Rejected++
	r.Reasons[reason]++
}

type spanError struct{ reason RejectReason }

func (e spanError) Error() string { return string(e.reason) }

// Converter validates OTLP spans and converts them to model.Span.
type Converter struct {
	Limits Limits
	Now    func() time.Time // injectable for tests
}

// Convert validates every span in req. Valid spans are returned; invalid
// spans are counted in Result.Rejected. It never fails as a whole: a request
// that decodes successfully always yields a (possibly empty) Result, which
// maps onto OTLP partial-success semantics.
func (c *Converter) Convert(req *coltracepb.ExportTraceServiceRequest) Result {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	maxEnd := uint64(now().Add(c.Limits.MaxFutureSkew).UnixNano())

	var res Result
	total := 0
	for _, rs := range req.GetResourceSpans() {
		for _, ss := range rs.GetScopeSpans() {
			total += len(ss.GetSpans())
		}
	}
	res.Spans = make([]model.Span, 0, total)

	for _, rs := range req.GetResourceSpans() {
		resAttrs := rs.GetResource().GetAttributes()
		resJSON, serviceName, resErr := c.resource(resAttrs)
		for _, ss := range rs.GetScopeSpans() {
			scope := ss.GetScope()
			scopeErr := hasNUL(scope.GetName()) || hasNUL(scope.GetVersion())
			for _, sp := range ss.GetSpans() {
				if scopeErr {
					res.reject(ReasonInvalidString)
					continue
				}
				if resErr != nil {
					res.reject(resErr.reason)
					continue
				}
				s, err := c.span(sp, maxEnd)
				if err != nil {
					res.reject(err.reason)
					continue
				}
				s.ServiceName = serviceName
				s.ResourceAttributes = resJSON
				s.ScopeName = scope.GetName()
				s.ScopeVersion = scope.GetVersion()
				res.Spans = append(res.Spans, s)
			}
		}
	}
	return res
}

func (c *Converter) resource(attrs []*commonpb.KeyValue) ([]byte, string, *spanError) {
	if len(attrs) > c.Limits.MaxResourceAttributes {
		return nil, "", &spanError{ReasonLimitExceeded}
	}
	m, err := c.attrMap(attrs)
	if err != nil {
		return nil, "", err
	}
	service := UnknownServiceName
	if v, ok := m["service.name"].(string); ok && v != "" {
		service = v
	}
	if len(service) > c.Limits.MaxNameBytes {
		return nil, "", &spanError{ReasonLimitExceeded}
	}
	b, jerr := json.Marshal(m)
	if jerr != nil { // unreachable: attrMap only produces JSON-safe values
		return nil, "", &spanError{ReasonInvalidString}
	}
	return b, service, nil
}

func (c *Converter) span(sp *tracepb.Span, maxEnd uint64) (model.Span, *spanError) {
	var s model.Span

	if len(sp.GetTraceId()) != 16 || allZero(sp.GetTraceId()) {
		return s, &spanError{ReasonInvalidTraceID}
	}
	if len(sp.GetSpanId()) != 8 || allZero(sp.GetSpanId()) {
		return s, &spanError{ReasonInvalidSpanID}
	}
	copy(s.TraceID[:], sp.GetTraceId())
	copy(s.SpanID[:], sp.GetSpanId())

	switch p := sp.GetParentSpanId(); {
	case len(p) == 0:
	case len(p) == 8 && !allZero(p):
		var pid model.SpanID
		copy(pid[:], p)
		if pid == s.SpanID {
			return s, &spanError{ReasonInvalidParentID}
		}
		s.ParentSpanID = &pid
	case len(p) == 8: // an all-zero parent is treated as "no parent"
	default:
		return s, &spanError{ReasonInvalidParentID}
	}

	start, end := sp.GetStartTimeUnixNano(), sp.GetEndTimeUnixNano()
	if start == 0 || end == 0 || end < start || end > math.MaxInt64 || end > maxEnd ||
		end-start > uint64(c.Limits.MaxSpanDuration) {
		return s, &spanError{ReasonInvalidTimestamp}
	}
	s.StartTimeUnixNano, s.EndTimeUnixNano = start, end

	if k := sp.GetKind(); k < tracepb.Span_SPAN_KIND_UNSPECIFIED || k > tracepb.Span_SPAN_KIND_CONSUMER {
		return s, &spanError{ReasonInvalidKind}
	}
	s.Kind = int16(sp.GetKind())

	st := sp.GetStatus()
	if c := st.GetCode(); c < tracepb.Status_STATUS_CODE_UNSET || c > tracepb.Status_STATUS_CODE_ERROR {
		return s, &spanError{ReasonInvalidStatus}
	}
	s.StatusCode = int16(st.GetCode())
	s.StatusMessage = st.GetMessage()

	if len(sp.GetName()) > c.Limits.MaxNameBytes {
		return s, &spanError{ReasonLimitExceeded}
	}
	if hasNUL(sp.GetName()) || hasNUL(st.GetMessage()) || hasNUL(sp.GetTraceState()) {
		return s, &spanError{ReasonInvalidString}
	}
	s.Name = sp.GetName()
	s.TraceState = sp.GetTraceState()

	if len(sp.GetAttributes()) > c.Limits.MaxAttributesPerSpan ||
		len(sp.GetEvents()) > c.Limits.MaxEventsPerSpan ||
		len(sp.GetLinks()) > c.Limits.MaxLinksPerSpan {
		return s, &spanError{ReasonLimitExceeded}
	}

	attrs, err := c.attrMap(sp.GetAttributes())
	if err != nil {
		return s, err
	}

	events := make([]map[string]any, 0, len(sp.GetEvents()))
	for _, ev := range sp.GetEvents() {
		if len(ev.GetAttributes()) > c.Limits.MaxAttributesPerSpan {
			return s, &spanError{ReasonLimitExceeded}
		}
		ea, err := c.attrMap(ev.GetAttributes())
		if err != nil {
			return s, err
		}
		if hasNUL(ev.GetName()) {
			return s, &spanError{ReasonInvalidString}
		}
		events = append(events, map[string]any{
			"name":           ev.GetName(),
			"time_unix_nano": ev.GetTimeUnixNano(),
			"attributes":     ea,
		})
	}

	links := make([]map[string]any, 0, len(sp.GetLinks()))
	for _, l := range sp.GetLinks() {
		if len(l.GetTraceId()) != 16 || len(l.GetSpanId()) != 8 {
			return s, &spanError{ReasonInvalidParentID}
		}
		if len(l.GetAttributes()) > c.Limits.MaxAttributesPerSpan {
			return s, &spanError{ReasonLimitExceeded}
		}
		la, err := c.attrMap(l.GetAttributes())
		if err != nil {
			return s, err
		}
		links = append(links, map[string]any{
			"trace_id":   fmt.Sprintf("%x", l.GetTraceId()),
			"span_id":    fmt.Sprintf("%x", l.GetSpanId()),
			"attributes": la,
		})
	}

	var jerr error
	if s.Attributes, jerr = json.Marshal(attrs); jerr != nil {
		return s, &spanError{ReasonInvalidString}
	}
	if s.Events, jerr = json.Marshal(events); jerr != nil {
		return s, &spanError{ReasonInvalidString}
	}
	if s.Links, jerr = json.Marshal(links); jerr != nil {
		return s, &spanError{ReasonInvalidString}
	}

	s.DroppedAttributesCount = sp.GetDroppedAttributesCount()
	s.DroppedEventsCount = sp.GetDroppedEventsCount()
	s.DroppedLinksCount = sp.GetDroppedLinksCount()
	return s, nil
}

// attrMap converts OTLP key/values to a JSON-safe map. Later duplicate keys
// win, matching how most backends resolve duplicates.
func (c *Converter) attrMap(kvs []*commonpb.KeyValue) (map[string]any, *spanError) {
	m := make(map[string]any, len(kvs))
	for _, kv := range kvs {
		if hasNUL(kv.GetKey()) {
			return nil, &spanError{ReasonInvalidString}
		}
		v, err := c.anyValue(kv.GetValue(), 0)
		if err != nil {
			return nil, err
		}
		m[kv.GetKey()] = v
	}
	return m, nil
}

const maxValueDepth = 8

func (c *Converter) anyValue(v *commonpb.AnyValue, depth int) (any, *spanError) {
	if depth > maxValueDepth {
		return nil, &spanError{ReasonLimitExceeded}
	}
	switch x := v.GetValue().(type) {
	case nil:
		return nil, nil
	case *commonpb.AnyValue_StringValue:
		if len(x.StringValue) > c.Limits.MaxAttributeValueBytes {
			return nil, &spanError{ReasonLimitExceeded}
		}
		if hasNUL(x.StringValue) {
			return nil, &spanError{ReasonInvalidString}
		}
		return x.StringValue, nil
	case *commonpb.AnyValue_BoolValue:
		return x.BoolValue, nil
	case *commonpb.AnyValue_IntValue:
		return x.IntValue, nil
	case *commonpb.AnyValue_DoubleValue:
		f := x.DoubleValue
		if math.IsNaN(f) || math.IsInf(f, 0) {
			// JSON cannot represent these; keep them visible as strings.
			return fmt.Sprint(f), nil
		}
		return f, nil
	case *commonpb.AnyValue_BytesValue:
		if len(x.BytesValue) > c.Limits.MaxAttributeValueBytes {
			return nil, &spanError{ReasonLimitExceeded}
		}
		return base64.StdEncoding.EncodeToString(x.BytesValue), nil
	case *commonpb.AnyValue_ArrayValue:
		vals := x.ArrayValue.GetValues()
		if len(vals) > c.Limits.MaxAttributesPerSpan {
			return nil, &spanError{ReasonLimitExceeded}
		}
		out := make([]any, len(vals))
		for i, e := range vals {
			ev, err := c.anyValue(e, depth+1)
			if err != nil {
				return nil, err
			}
			out[i] = ev
		}
		return out, nil
	case *commonpb.AnyValue_KvlistValue:
		kvs := x.KvlistValue.GetValues()
		if len(kvs) > c.Limits.MaxAttributesPerSpan {
			return nil, &spanError{ReasonLimitExceeded}
		}
		out := make(map[string]any, len(kvs))
		for _, kv := range kvs {
			if hasNUL(kv.GetKey()) {
				return nil, &spanError{ReasonInvalidString}
			}
			ev, err := c.anyValue(kv.GetValue(), depth+1)
			if err != nil {
				return nil, err
			}
			out[kv.GetKey()] = ev
		}
		return out, nil
	default:
		return nil, nil
	}
}

func allZero(b []byte) bool {
	for _, x := range b {
		if x != 0 {
			return false
		}
	}
	return true
}

// hasNUL reports whether s contains a NUL byte, which PostgreSQL text and
// jsonb values cannot store. Such spans are rejected rather than allowed to
// poison a batched insert.
func hasNUL(s string) bool { return strings.IndexByte(s, 0) >= 0 }
