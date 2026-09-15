package otlp

import (
	"compress/gzip"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"math"

	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	ContentTypeProto = "application/x-protobuf"
	ContentTypeJSON  = "application/json"
)

var (
	ErrUnsupportedMediaType = errors.New("unsupported media type")
	ErrTooLarge             = errors.New("payload too large")
)

type RawSpan struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	Name          string
	StartNs       int64
	EndNs         int64
	StatusCode    int32
	StatusMessage string
	TraceState    string
	Attrs         map[string]any
	Events        []map[string]any
	Links         []map[string]any
	Resource      map[string]any
	Scope         map[string]any
}

func Decode(body []byte, mediaType string) ([]RawSpan, error) {
	var request collectortracepb.ExportTraceServiceRequest
	switch mediaType {
	case ContentTypeProto:
		if err := proto.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode protobuf: %w", err)
		}
	case ContentTypeJSON:
		if err := protojson.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode JSON: %w", err)
		}
	default:
		return nil, ErrUnsupportedMediaType
	}

	var result []RawSpan
	for _, resourceSpans := range request.ResourceSpans {
		resource := attributes(resourceSpans.GetResource().GetAttributes())
		for _, scopeSpans := range resourceSpans.ScopeSpans {
			scope := map[string]any{
				"name":       scopeSpans.GetScope().GetName(),
				"version":    scopeSpans.GetScope().GetVersion(),
				"attributes": attributes(scopeSpans.GetScope().GetAttributes()),
			}
			for _, span := range scopeSpans.Spans {
				raw, err := rawSpan(span, resource, scope)
				if err != nil {
					return nil, err
				}
				result = append(result, raw)
			}
		}
	}
	return result, nil
}

func rawSpan(span *tracepb.Span, resource, scope map[string]any) (RawSpan, error) {
	if err := validID("trace id", span.TraceId, 16, false); err != nil {
		return RawSpan{}, fmt.Errorf("span %q: %w", span.Name, err)
	}
	if err := validID("span id", span.SpanId, 8, false); err != nil {
		return RawSpan{}, fmt.Errorf("span %q: %w", span.Name, err)
	}
	if err := validID("parent span id", span.ParentSpanId, 8, true); err != nil {
		return RawSpan{}, fmt.Errorf("span %q: %w", span.Name, err)
	}
	if span.StartTimeUnixNano > math.MaxInt64 || span.EndTimeUnixNano > math.MaxInt64 {
		return RawSpan{}, fmt.Errorf("span %q: timestamp exceeds int64", span.Name)
	}
	if span.EndTimeUnixNano < span.StartTimeUnixNano {
		return RawSpan{}, fmt.Errorf("span %q: end precedes start", span.Name)
	}

	events := make([]map[string]any, 0, len(span.Events))
	for _, event := range span.Events {
		events = append(events, map[string]any{
			"name":       event.Name,
			"time_ns":    event.TimeUnixNano,
			"attributes": attributes(event.Attributes),
		})
	}
	links := make([]map[string]any, 0, len(span.Links))
	for _, link := range span.Links {
		links = append(links, map[string]any{
			"trace_id":    hex.EncodeToString(link.TraceId),
			"span_id":     hex.EncodeToString(link.SpanId),
			"trace_state": link.TraceState,
			"attributes":  attributes(link.Attributes),
		})
	}

	return RawSpan{
		TraceID:       hex.EncodeToString(span.TraceId),
		SpanID:        hex.EncodeToString(span.SpanId),
		ParentSpanID:  hex.EncodeToString(span.ParentSpanId),
		Name:          span.Name,
		StartNs:       int64(span.StartTimeUnixNano),
		EndNs:         int64(span.EndTimeUnixNano),
		StatusCode:    int32(span.GetStatus().GetCode()),
		StatusMessage: span.GetStatus().GetMessage(),
		TraceState:    span.TraceState,
		Attrs:         attributes(span.Attributes),
		Events:        events,
		Links:         links,
		Resource:      resource,
		Scope:         scope,
	}, nil
}

func validID(name string, id []byte, size int, optional bool) error {
	if optional && len(id) == 0 {
		return nil
	}
	if len(id) != size {
		return fmt.Errorf("%s must be %d bytes", name, size)
	}
	for _, b := range id {
		if b != 0 {
			return nil
		}
	}
	return fmt.Errorf("%s must not be all zero", name)
}

func attributes(values []*commonpb.KeyValue) map[string]any {
	result := make(map[string]any, len(values))
	for _, value := range values {
		if _, exists := result[value.Key]; exists {
			log.Printf("warning: duplicate OTLP attribute key %q", value.Key)
		}
		result[value.Key] = ToAny(value.Value)
	}
	return result
}

func ToAny(v *commonpb.AnyValue) any {
	if v == nil {
		return nil
	}
	switch value := v.Value.(type) {
	case *commonpb.AnyValue_StringValue:
		return value.StringValue
	case *commonpb.AnyValue_BoolValue:
		return value.BoolValue
	case *commonpb.AnyValue_IntValue:
		return value.IntValue
	case *commonpb.AnyValue_DoubleValue:
		switch {
		case math.IsNaN(value.DoubleValue):
			return "NaN"
		case math.IsInf(value.DoubleValue, 1):
			return "Infinity"
		case math.IsInf(value.DoubleValue, -1):
			return "-Infinity"
		default:
			return value.DoubleValue
		}
	case *commonpb.AnyValue_BytesValue:
		return base64.StdEncoding.EncodeToString(value.BytesValue)
	case *commonpb.AnyValue_ArrayValue:
		if value.ArrayValue == nil {
			return []any(nil)
		}
		items := make([]any, len(value.ArrayValue.Values))
		for i, item := range value.ArrayValue.Values {
			items[i] = ToAny(item)
		}
		return items
	case *commonpb.AnyValue_KvlistValue:
		if value.KvlistValue == nil {
			return map[string]any(nil)
		}
		return attributes(value.KvlistValue.Values)
	default:
		return nil
	}
}

func Gunzip(r io.Reader, maxBytes int64) ([]byte, error) {
	reader, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	body, err := io.ReadAll(io.LimitReader(reader, maxBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(body)) > maxBytes {
		return nil, ErrTooLarge
	}
	return body, nil
}
