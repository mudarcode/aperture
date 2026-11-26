package otlpingest

import (
	"fmt"
	"testing"
	"time"

	coltracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
)

// benchRequest builds a request shaped like real HTTP-server telemetry:
// 100 spans, 12 string/int attributes each, one event on every 10th span.
func benchRequest() *coltracepb.ExportTraceServiceRequest {
	spans := make([]*tracepb.Span, 100)
	for i := range spans {
		sp := validSpan()
		sp.SpanId = []byte{byte(i + 1), 2, 3, 4, 5, 6, 7, 8}
		sp.Attributes = nil
		for a := 0; a < 10; a++ {
			sp.Attributes = append(sp.Attributes, strAttr(fmt.Sprintf("http.attr_%d", a), fmt.Sprintf("value number %d with some text", a)))
		}
		sp.Attributes = append(sp.Attributes,
			&commonpb.KeyValue{Key: "http.response.status_code", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 200}}},
			&commonpb.KeyValue{Key: "server.port", Value: &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 8080}}})
		if i%10 == 0 {
			sp.Events = []*tracepb.Span_Event{{Name: "retry", TimeUnixNano: sp.StartTimeUnixNano + 1, Attributes: []*commonpb.KeyValue{strAttr("reason", "timeout")}}}
		}
		spans[i] = sp
	}
	return request("checkout", spans...)
}

func BenchmarkConvert100Spans(b *testing.B) {
	req := benchRequest()
	conv := &Converter{Limits: DefaultLimits(), Now: func() time.Time { return testNow }}
	b.ReportAllocs()
	for b.Loop() {
		res := conv.Convert(req)
		if len(res.Spans) != 100 {
			b.Fatalf("converted %d spans: %s", len(res.Spans), res.ErrorMessage())
		}
	}
}
