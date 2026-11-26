package otlpingest

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	resourcepb "go.opentelemetry.io/proto/otlp/resource/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

var testNow = time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)

func testConverter() *Converter {
	return &Converter{Limits: DefaultLimits(), Now: func() time.Time { return testNow }}
}

func strAttr(k, v string) *commonpb.KeyValue {
	return &commonpb.KeyValue{Key: k, Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: v}}}
}

// validSpan returns a span that passes validation; tests mutate it.
func validSpan() *tracepb.Span {
	start := uint64(testNow.Add(-time.Second).UnixNano())
	return &tracepb.Span{
		TraceId:           []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanId:            []byte{1, 2, 3, 4, 5, 6, 7, 8},
		ParentSpanId:      []byte{8, 7, 6, 5, 4, 3, 2, 1},
		Name:              "GET /checkout",
		Kind:              tracepb.Span_SPAN_KIND_SERVER,
		StartTimeUnixNano: start,
		EndTimeUnixNano:   start + uint64(250*time.Millisecond),
		Attributes:        []*commonpb.KeyValue{strAttr("http.method", "GET")},
		Status:            &tracepb.Status{Code: tracepb.Status_STATUS_CODE_ERROR, Message: "boom"},
	}
}

func request(service string, spans ...*tracepb.Span) *coltracepb.ExportTraceServiceRequest {
	var attrs []*commonpb.KeyValue
	if service != "" {
		attrs = append(attrs, strAttr("service.name", service))
	}
	return &coltracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{
		Resource: &resourcepb.Resource{Attributes: attrs},
		ScopeSpans: []*tracepb.ScopeSpans{{
			Scope: &commonpb.InstrumentationScope{Name: "test-scope", Version: "1.2.3"},
			Spans: spans,
		}},
	}}}
}

func TestConvertValidSpanMapsAllFields(t *testing.T) {
	sp := validSpan()
	sp.TraceState = "vendor=x"
	sp.Events = []*tracepb.Span_Event{{Name: "retry", TimeUnixNano: sp.StartTimeUnixNano + 10, Attributes: []*commonpb.KeyValue{strAttr("attempt", "2")}}}
	sp.Links = []*tracepb.Span_Link{{TraceId: make16(9), SpanId: make8(9)}}
	sp.DroppedAttributesCount = 3

	res := testConverter().Convert(request("checkout", sp))
	if res.Rejected != 0 || len(res.Spans) != 1 {
		t.Fatalf("got rejected=%d spans=%d (%s)", res.Rejected, len(res.Spans), res.ErrorMessage())
	}
	got := res.Spans[0]
	if got.ServiceName != "checkout" || got.Name != "GET /checkout" || got.Kind != int16(tracepb.Span_SPAN_KIND_SERVER) {
		t.Errorf("identity fields wrong: %+v", got)
	}
	if got.ParentSpanID == nil || got.ParentSpanID[0] != 8 {
		t.Errorf("parent span id not mapped: %v", got.ParentSpanID)
	}
	if got.DurationNanos() != uint64(250*time.Millisecond) {
		t.Errorf("duration = %d", got.DurationNanos())
	}
	if got.StatusCode != 2 || got.StatusMessage != "boom" {
		t.Errorf("status = %d %q", got.StatusCode, got.StatusMessage)
	}
	if got.ScopeName != "test-scope" || got.ScopeVersion != "1.2.3" || got.TraceState != "vendor=x" {
		t.Errorf("scope/tracestate wrong: %+v", got)
	}
	if got.DroppedAttributesCount != 3 {
		t.Errorf("dropped attrs = %d", got.DroppedAttributesCount)
	}
	assertJSON(t, got.Attributes, `{"http.method":"GET"}`)
	assertJSON(t, got.ResourceAttributes, `{"service.name":"checkout"}`)
	assertJSON(t, got.Events, `[{"name":"retry","time_unix_nano":`+jsonNum(sp.StartTimeUnixNano+10)+`,"attributes":{"attempt":"2"}}]`)
	assertJSON(t, got.Links, `[{"trace_id":"09090909090909090909090909090909","span_id":"0909090909090909","attributes":{}}]`)
}

func TestConvertDefaultsServiceName(t *testing.T) {
	res := testConverter().Convert(request("", validSpan()))
	if len(res.Spans) != 1 || res.Spans[0].ServiceName != UnknownServiceName {
		t.Fatalf("expected %q, got %+v", UnknownServiceName, res.Spans)
	}
}

func TestConvertRootSpan(t *testing.T) {
	for name, parent := range map[string][]byte{"empty": nil, "all-zero": make([]byte, 8)} {
		t.Run(name, func(t *testing.T) {
			sp := validSpan()
			sp.ParentSpanId = parent
			res := testConverter().Convert(request("svc", sp))
			if len(res.Spans) != 1 || res.Spans[0].ParentSpanID != nil {
				t.Fatalf("expected root span, got %+v (%s)", res.Spans, res.ErrorMessage())
			}
		})
	}
}

func TestConvertRejections(t *testing.T) {
	lim := DefaultLimits()
	cases := []struct {
		name   string
		mutate func(*tracepb.Span)
		reason RejectReason
	}{
		{"missing trace id", func(s *tracepb.Span) { s.TraceId = nil }, ReasonInvalidTraceID},
		{"short trace id", func(s *tracepb.Span) { s.TraceId = []byte{1, 2, 3} }, ReasonInvalidTraceID},
		{"zero trace id", func(s *tracepb.Span) { s.TraceId = make([]byte, 16) }, ReasonInvalidTraceID},
		{"missing span id", func(s *tracepb.Span) { s.SpanId = nil }, ReasonInvalidSpanID},
		{"zero span id", func(s *tracepb.Span) { s.SpanId = make([]byte, 8) }, ReasonInvalidSpanID},
		{"long span id", func(s *tracepb.Span) { s.SpanId = make16(1) }, ReasonInvalidSpanID},
		{"bad parent length", func(s *tracepb.Span) { s.ParentSpanId = []byte{1} }, ReasonInvalidParentID},
		{"self parent", func(s *tracepb.Span) { s.ParentSpanId = s.SpanId }, ReasonInvalidParentID},
		{"zero start", func(s *tracepb.Span) { s.StartTimeUnixNano = 0 }, ReasonInvalidTimestamp},
		{"zero end", func(s *tracepb.Span) { s.EndTimeUnixNano = 0 }, ReasonInvalidTimestamp},
		{"end before start", func(s *tracepb.Span) { s.EndTimeUnixNano = s.StartTimeUnixNano - 1 }, ReasonInvalidTimestamp},
		{"beyond int64", func(s *tracepb.Span) { s.EndTimeUnixNano = math.MaxUint64 }, ReasonInvalidTimestamp},
		{"too far in future", func(s *tracepb.Span) {
			s.EndTimeUnixNano = uint64(testNow.Add(lim.MaxFutureSkew + time.Minute).UnixNano())
		}, ReasonInvalidTimestamp},
		{"too long", func(s *tracepb.Span) {
			s.StartTimeUnixNano = s.EndTimeUnixNano - uint64(lim.MaxSpanDuration) - 1
		}, ReasonInvalidTimestamp},
		{"unknown kind", func(s *tracepb.Span) { s.Kind = 42 }, ReasonInvalidKind},
		{"unknown status", func(s *tracepb.Span) { s.Status.Code = 7 }, ReasonInvalidStatus},
		{"name too long", func(s *tracepb.Span) { s.Name = strings.Repeat("x", lim.MaxNameBytes+1) }, ReasonLimitExceeded},
		{"too many attributes", func(s *tracepb.Span) {
			for i := 0; i <= lim.MaxAttributesPerSpan; i++ {
				s.Attributes = append(s.Attributes, strAttr("k"+string(rune('a'+i%26))+strings.Repeat("x", i), "v"))
			}
		}, ReasonLimitExceeded},
		{"attribute value too long", func(s *tracepb.Span) {
			s.Attributes = []*commonpb.KeyValue{strAttr("big", strings.Repeat("x", lim.MaxAttributeValueBytes+1))}
		}, ReasonLimitExceeded},
		{"too many events", func(s *tracepb.Span) {
			s.Events = make([]*tracepb.Span_Event, lim.MaxEventsPerSpan+1)
		}, ReasonLimitExceeded},
		{"NUL in name", func(s *tracepb.Span) { s.Name = "a\x00b" }, ReasonInvalidString},
		{"NUL in attribute key", func(s *tracepb.Span) { s.Attributes = []*commonpb.KeyValue{strAttr("a\x00", "v")} }, ReasonInvalidString},
		{"NUL in attribute value", func(s *tracepb.Span) { s.Attributes = []*commonpb.KeyValue{strAttr("k", "v\x00")} }, ReasonInvalidString},
		{"bad link ids", func(s *tracepb.Span) { s.Links = []*tracepb.Span_Link{{TraceId: []byte{1}, SpanId: make8(1)}} }, ReasonInvalidParentID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sp := validSpan()
			tc.mutate(sp)
			res := testConverter().Convert(request("svc", sp))
			if res.Rejected != 1 || len(res.Spans) != 0 {
				t.Fatalf("expected 1 rejection, got rejected=%d spans=%d", res.Rejected, len(res.Spans))
			}
			if res.Reasons[tc.reason] != 1 {
				t.Fatalf("expected reason %s, got %v", tc.reason, res.Reasons)
			}
		})
	}
}

func TestConvertPartialSuccessKeepsValidSpans(t *testing.T) {
	good1, good2 := validSpan(), validSpan()
	good2.SpanId = make8(2)
	bad1, bad2, bad3 := validSpan(), validSpan(), validSpan()
	bad1.SpanId = nil
	bad2.SpanId = nil
	bad3.EndTimeUnixNano = 0

	res := testConverter().Convert(request("svc", good1, bad1, good2, bad2, bad3))
	if len(res.Spans) != 2 || res.Rejected != 3 {
		t.Fatalf("spans=%d rejected=%d", len(res.Spans), res.Rejected)
	}
	want := "rejected 3 spans: invalid_span_id=2, invalid_timestamp=1"
	if got := res.ErrorMessage(); got != want {
		t.Fatalf("message = %q, want %q", got, want)
	}
}

func TestConvertResourceOverLimitRejectsItsSpans(t *testing.T) {
	conv := testConverter()
	conv.Limits.MaxResourceAttributes = 1
	req := request("svc", validSpan(), validSpan())
	req.ResourceSpans[0].Resource.Attributes = append(req.ResourceSpans[0].Resource.Attributes, strAttr("host", "a"))
	res := conv.Convert(req)
	if res.Rejected != 2 || res.Reasons[ReasonLimitExceeded] != 2 {
		t.Fatalf("got %+v", res)
	}
}

func TestAttributeValueTypes(t *testing.T) {
	sp := validSpan()
	sp.Attributes = []*commonpb.KeyValue{
		{Key: "b", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}},
		{Key: "i", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: -7}}},
		{Key: "d", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}},
		{Key: "nan", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.NaN()}}},
		{Key: "inf", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.Inf(-1)}}},
		{Key: "bytes", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte("hi")}}},
		{Key: "empty", Value: &commonpb.AnyValue{}},
		{Key: "nil"},
		{Key: "arr", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{
			Values: []*commonpb.AnyValue{{Value: &commonpb.AnyValue_IntValue{IntValue: 1}}, {Value: &commonpb.AnyValue_StringValue{StringValue: "x"}}},
		}}}},
		{Key: "kv", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{
			Values: []*commonpb.KeyValue{strAttr("inner", "y")},
		}}}},
		strAttr("dup", "first"),
		strAttr("dup", "second"),
	}
	res := testConverter().Convert(request("svc", sp))
	if len(res.Spans) != 1 {
		t.Fatalf("rejected: %s", res.ErrorMessage())
	}
	assertJSON(t, res.Spans[0].Attributes, `{
		"b": true, "i": -7, "d": 1.5, "nan": "NaN", "inf": "-Inf", "bytes": "aGk=",
		"empty": null, "nil": null, "arr": [1, "x"], "kv": {"inner": "y"}, "dup": "second"}`)
}

func TestDeeplyNestedValueRejected(t *testing.T) {
	v := &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "leaf"}}
	for i := 0; i < maxValueDepth+2; i++ {
		v = &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{v}}}}
	}
	sp := validSpan()
	sp.Attributes = []*commonpb.KeyValue{{Key: "deep", Value: v}}
	res := testConverter().Convert(request("svc", sp))
	if res.Reasons[ReasonLimitExceeded] != 1 {
		t.Fatalf("expected limit_exceeded, got %v", res.Reasons)
	}
}

func make16(b byte) []byte { return []byte(strings.Repeat(string(rune(b)), 16)) }
func make8(b byte) []byte  { return []byte(strings.Repeat(string(rune(b)), 8)) }

func jsonNum(v uint64) string { b, _ := json.Marshal(v); return string(b) }

func assertJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var g, w any
	if err := json.Unmarshal(got, &g); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("bad expectation %s: %v", want, err)
	}
	gb, _ := json.Marshal(g)
	wb, _ := json.Marshal(w)
	if string(gb) != string(wb) {
		t.Errorf("JSON mismatch\n got: %s\nwant: %s", gb, wb)
	}
}

// The streaming encoder must agree with encoding/json on every string.
func TestAppendJSONStringMatchesEncodingJSON(t *testing.T) {
	cases := []string{"", "plain", `q"uote`, `back\slash`, "tab\tnew\nline\r", "\x00\x01\x1f\x7f",
		"héllo 世界 🎉", "line sep para", "</script>&<>"}
	for _, s := range cases {
		got := appendJSONString(nil, s)
		var back string
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("%q: invalid JSON %s: %v", s, got, err)
		}
		if back != s {
			t.Errorf("%q round-tripped to %q", s, back)
		}
	}
}

func FuzzAppendJSONString(f *testing.F) {
	f.Add("a\"b\\c\n ")
	f.Fuzz(func(t *testing.T, s string) {
		if !utf8.ValidString(s) {
			t.Skip() // protobuf rejects invalid UTF-8 before we see it
		}
		var back string
		if err := json.Unmarshal(appendJSONString(nil, s), &back); err != nil || back != s {
			t.Fatalf("%q: got %q, err %v", s, back, err)
		}
	})
}

func TestAppendJSONFloatIsValidJSON(t *testing.T) {
	for _, f := range []float64{0, -0.0, 1, -1.5, 1e-7, 123456789, 1e21, 1e300, 5e-324, math.MaxFloat64} {
		var back float64
		if err := json.Unmarshal(appendJSONFloat(nil, f), &back); err != nil || back != f {
			t.Errorf("%v: got %v, err %v (%s)", f, back, err, appendJSONFloat(nil, f))
		}
	}
}
