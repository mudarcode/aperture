// Package model defines Aperture's internal, storage-oriented span representation.
//
// It is deliberately decoupled from the OTLP protobuf types so that the
// pipeline and storage layers do not depend on wire-format details.
package model

// TraceID is a 16-byte W3C/OTLP trace identifier.
type TraceID [16]byte

// SpanID is an 8-byte W3C/OTLP span identifier.
type SpanID [8]byte

// Span is a single validated span ready for persistence.
type Span struct {
	TraceID      TraceID
	SpanID       SpanID
	ParentSpanID *SpanID // nil for root spans
	TraceState   string

	ServiceName string
	Name        string
	Kind        int16 // OTLP SpanKind enum value (0-5)

	StartTimeUnixNano uint64
	EndTimeUnixNano   uint64

	StatusCode    int16 // OTLP StatusCode enum value (0 unset, 1 ok, 2 error)
	StatusMessage string

	ScopeName    string
	ScopeVersion string

	// JSON-encoded objects/arrays, produced by the ingestion layer.
	ResourceAttributes []byte // {"key": value, ...}
	Attributes         []byte // {"key": value, ...}
	Events             []byte // [{"name":..., "time_unix_nano":..., "attributes":{...}}]
	Links              []byte // [{"trace_id":..., "span_id":..., "attributes":{...}}]

	DroppedAttributesCount uint32
	DroppedEventsCount     uint32
	DroppedLinksCount      uint32
}

// DurationNanos returns the span duration. Callers must only use this on
// validated spans, where End >= Start is guaranteed.
func (s *Span) DurationNanos() uint64 { return s.EndTimeUnixNano - s.StartTimeUnixNano }
