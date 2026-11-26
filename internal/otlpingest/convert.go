// Package otlpingest converts and validates OTLP trace export requests.
package otlpingest

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

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
	b, err := c.appendAttrs(nil, attrs, 0)
	if err != nil {
		return nil, "", err
	}
	service := UnknownServiceName
	for _, kv := range attrs { // last occurrence wins, as in the stored JSON
		if kv.GetKey() == "service.name" {
			if v := kv.GetValue().GetStringValue(); v != "" {
				service = v
			}
		}
	}
	if len(service) > c.Limits.MaxNameBytes {
		return nil, "", &spanError{ReasonLimitExceeded}
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

	// Attribute, event, and link JSON is written directly into byte buffers
	// rather than via map[string]any + encoding/json: profiling showed the
	// reflection-based path was the largest CPU cost in ingestion.
	var err *spanError
	if s.Attributes, err = c.appendAttrs(make([]byte, 0, 64*len(sp.GetAttributes())+2), sp.GetAttributes(), 0); err != nil {
		return s, err
	}

	ev := make([]byte, 0, 64)
	ev = append(ev, '[')
	for i, e := range sp.GetEvents() {
		if len(e.GetAttributes()) > c.Limits.MaxAttributesPerSpan {
			return s, &spanError{ReasonLimitExceeded}
		}
		if hasNUL(e.GetName()) {
			return s, &spanError{ReasonInvalidString}
		}
		if i > 0 {
			ev = append(ev, ',')
		}
		ev = append(ev, `{"name":`...)
		ev = appendJSONString(ev, e.GetName())
		ev = append(ev, `,"time_unix_nano":`...)
		ev = strconv.AppendUint(ev, e.GetTimeUnixNano(), 10)
		ev = append(ev, `,"attributes":`...)
		if ev, err = c.appendAttrs(ev, e.GetAttributes(), 0); err != nil {
			return s, err
		}
		ev = append(ev, '}')
	}
	s.Events = append(ev, ']')

	lk := make([]byte, 0, 16)
	lk = append(lk, '[')
	for i, l := range sp.GetLinks() {
		if len(l.GetTraceId()) != 16 || len(l.GetSpanId()) != 8 {
			return s, &spanError{ReasonInvalidParentID}
		}
		if len(l.GetAttributes()) > c.Limits.MaxAttributesPerSpan {
			return s, &spanError{ReasonLimitExceeded}
		}
		if i > 0 {
			lk = append(lk, ',')
		}
		lk = append(lk, `{"trace_id":"`...)
		lk = hex.AppendEncode(lk, l.GetTraceId())
		lk = append(lk, `","span_id":"`...)
		lk = hex.AppendEncode(lk, l.GetSpanId())
		lk = append(lk, `","attributes":`...)
		if lk, err = c.appendAttrs(lk, l.GetAttributes(), 0); err != nil {
			return s, err
		}
		lk = append(lk, '}')
	}
	s.Links = append(lk, ']')

	s.DroppedAttributesCount = sp.GetDroppedAttributesCount()
	s.DroppedEventsCount = sp.GetDroppedEventsCount()
	s.DroppedLinksCount = sp.GetDroppedLinksCount()
	return s, nil
}

// appendAttrs appends kvs as a JSON object. When a key repeats, only its
// last occurrence is written ("last wins", which is also what PostgreSQL
// jsonb would keep).
func (c *Converter) appendAttrs(dst []byte, kvs []*commonpb.KeyValue, depth int) ([]byte, *spanError) {
	dst = append(dst, '{')
	first := true
	for i, kv := range kvs {
		key := kv.GetKey()
		if hasNUL(key) {
			return nil, &spanError{ReasonInvalidString}
		}
		if supersededLater(kvs, i, key) {
			continue
		}
		if !first {
			dst = append(dst, ',')
		}
		first = false
		dst = appendJSONString(dst, key)
		dst = append(dst, ':')
		var err *spanError
		if dst, err = c.appendValue(dst, kv.GetValue(), depth); err != nil {
			return nil, err
		}
	}
	return append(dst, '}'), nil
}

func supersededLater(kvs []*commonpb.KeyValue, i int, key string) bool {
	for _, later := range kvs[i+1:] {
		if later.GetKey() == key {
			return true
		}
	}
	return false
}

const maxValueDepth = 8

func (c *Converter) appendValue(dst []byte, v *commonpb.AnyValue, depth int) ([]byte, *spanError) {
	if depth > maxValueDepth {
		return nil, &spanError{ReasonLimitExceeded}
	}
	switch x := v.GetValue().(type) {
	case *commonpb.AnyValue_StringValue:
		if len(x.StringValue) > c.Limits.MaxAttributeValueBytes {
			return nil, &spanError{ReasonLimitExceeded}
		}
		if hasNUL(x.StringValue) {
			return nil, &spanError{ReasonInvalidString}
		}
		return appendJSONString(dst, x.StringValue), nil
	case *commonpb.AnyValue_BoolValue:
		return strconv.AppendBool(dst, x.BoolValue), nil
	case *commonpb.AnyValue_IntValue:
		return strconv.AppendInt(dst, x.IntValue, 10), nil
	case *commonpb.AnyValue_DoubleValue:
		f := x.DoubleValue
		if math.IsNaN(f) || math.IsInf(f, 0) {
			// JSON cannot represent these; keep them visible as strings.
			return appendJSONString(dst, fmt.Sprint(f)), nil
		}
		return appendJSONFloat(dst, f), nil
	case *commonpb.AnyValue_BytesValue:
		if len(x.BytesValue) > c.Limits.MaxAttributeValueBytes {
			return nil, &spanError{ReasonLimitExceeded}
		}
		dst = append(dst, '"')
		dst = base64.StdEncoding.AppendEncode(dst, x.BytesValue)
		return append(dst, '"'), nil
	case *commonpb.AnyValue_ArrayValue:
		vals := x.ArrayValue.GetValues()
		if len(vals) > c.Limits.MaxAttributesPerSpan {
			return nil, &spanError{ReasonLimitExceeded}
		}
		dst = append(dst, '[')
		for i, e := range vals {
			if i > 0 {
				dst = append(dst, ',')
			}
			var err *spanError
			if dst, err = c.appendValue(dst, e, depth+1); err != nil {
				return nil, err
			}
		}
		return append(dst, ']'), nil
	case *commonpb.AnyValue_KvlistValue:
		kvs := x.KvlistValue.GetValues()
		if len(kvs) > c.Limits.MaxAttributesPerSpan {
			return nil, &spanError{ReasonLimitExceeded}
		}
		return c.appendAttrs(dst, kvs, depth+1)
	default: // empty AnyValue
		return append(dst, "null"...), nil
	}
}

// appendJSONFloat formats like encoding/json: shortest representation,
// exponent form only for very large or small magnitudes.
func appendJSONFloat(dst []byte, f float64) []byte {
	abs := math.Abs(f)
	format := byte('f')
	if abs != 0 && (abs < 1e-6 || abs >= 1e21) {
		format = 'e'
	}
	return strconv.AppendFloat(dst, f, format, -1, 64)
}

const hexDigits = "0123456789abcdef"

// appendJSONString appends s as a JSON string literal. Protobuf has already
// rejected invalid UTF-8, so only quotes, backslashes, and control
// characters need escaping; U+2028/U+2029 are escaped too so the output is
// also safe to embed in JavaScript.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	start := 0
	for i := 0; i < len(s); {
		b := s[i]
		if b >= 0x20 && b != '"' && b != '\\' && b < 0x80 {
			i++
			continue
		}
		if b >= 0x80 {
			r, size := utf8.DecodeRuneInString(s[i:])
			if r == '\u2028' || r == '\u2029' || r == utf8.RuneError && size == 1 {
				dst = append(dst, s[start:i]...)
				if r == utf8.RuneError {
					dst = append(dst, `\ufffd`...)
				} else {
					dst = append(dst, `\u202`...)
					dst = append(dst, hexDigits[r&0xF])
				}
				i += size
				start = i
				continue
			}
			i += size
			continue
		}
		dst = append(dst, s[start:i]...)
		switch b {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		default:
			dst = append(dst, '\\', 'u', '0', '0', hexDigits[b>>4], hexDigits[b&0xF])
		}
		i++
		start = i
	}
	dst = append(dst, s[start:]...)
	return append(dst, '"')
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
