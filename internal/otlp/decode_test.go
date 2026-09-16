package otlp

import (
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"errors"
	"math"
	"os"
	"reflect"
	"testing"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	commonpb "go.opentelemetry.io/proto/otlp/common/v1"
	tracepb "go.opentelemetry.io/proto/otlp/trace/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func fixtureBytes(t *testing.T) ([]byte, []byte) {
	t.Helper()
	jsonBody, err := os.ReadFile("testdata/trace.json")
	if err != nil {
		t.Fatal(err)
	}
	var request collectortracepb.ExportTraceServiceRequest
	if err := protojson.Unmarshal(jsonBody, &request); err != nil {
		t.Fatal(err)
	}
	protoBody, err := proto.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}
	return jsonBody, protoBody
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func TestDecodeFourEncodings(t *testing.T) {
	jsonBody, protoBody := fixtureBytes(t)
	jsonGzip, err := Gunzip(bytes.NewReader(gzipBytes(t, jsonBody)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	protoGzip, err := Gunzip(bytes.NewReader(gzipBytes(t, protoBody)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	inputs := []struct {
		body      []byte
		mediaType string
	}{
		{jsonBody, ContentTypeJSON},
		{protoBody, ContentTypeProto},
		{jsonGzip, ContentTypeJSON},
		{protoGzip, ContentTypeProto},
	}
	var want []RawSpan
	for i, input := range inputs {
		got, err := Decode(input.body, input.mediaType)
		if err != nil {
			t.Fatalf("input %d: %v", i, err)
		}
		if i == 0 {
			want = got
		} else if !reflect.DeepEqual(got, want) {
			t.Fatalf("input %d differs:\n got: %#v\nwant: %#v", i, got, want)
		}
	}
}

func TestDecodeHexIDs(t *testing.T) {
	jsonBody, _ := fixtureBytes(t)
	spans, err := Decode(jsonBody, ContentTypeJSON)
	if err != nil {
		t.Fatal(err)
	}
	if spans[0].TraceID != "0102030405060708090a0b0c0d0e0f10" || len(spans[0].TraceID) != 32 {
		t.Fatalf("unexpected trace ID %q", spans[0].TraceID)
	}
	if spans[0].SpanID != "1112131415161718" || len(spans[0].SpanID) != 16 {
		t.Fatalf("unexpected span ID %q", spans[0].SpanID)
	}
	if spans[0].ParentSpanID != "" {
		t.Fatalf("root parent = %q", spans[0].ParentSpanID)
	}
}

func validRequest() *collectortracepb.ExportTraceServiceRequest {
	span := &tracepb.Span{
		TraceId:           bytes.Repeat([]byte{1}, 16),
		SpanId:            bytes.Repeat([]byte{2}, 8),
		StartTimeUnixNano: 1,
		EndTimeUnixNano:   2,
		Name:              "valid",
	}
	return &collectortracepb.ExportTraceServiceRequest{ResourceSpans: []*tracepb.ResourceSpans{{ScopeSpans: []*tracepb.ScopeSpans{{Spans: []*tracepb.Span{span}}}}}}
}

func marshalRequest(t *testing.T, request *collectortracepb.ExportTraceServiceRequest) []byte {
	t.Helper()
	body, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDecodeRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*tracepb.Span)
	}{
		{"trace id length", func(s *tracepb.Span) { s.TraceId = bytes.Repeat([]byte{1}, 15) }},
		{"zero span id", func(s *tracepb.Span) { s.SpanId = make([]byte, 8) }},
		{"parent id length", func(s *tracepb.Span) { s.ParentSpanId = bytes.Repeat([]byte{3}, 4) }},
		{"end before start", func(s *tracepb.Span) { s.StartTimeUnixNano, s.EndTimeUnixNano = 3, 2 }},
		{"start over max int64", func(s *tracepb.Span) { s.StartTimeUnixNano, s.EndTimeUnixNano = math.MaxUint64, math.MaxUint64 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			request := validRequest()
			tt.mutate(request.ResourceSpans[0].ScopeSpans[0].Spans[0])
			if _, err := Decode(marshalRequest(t, request), ContentTypeProto); err == nil {
				t.Fatal("expected error")
			}
		})
	}
	if _, err := Decode(nil, "text/plain"); !errors.Is(err, ErrUnsupportedMediaType) {
		t.Fatalf("got %v, want ErrUnsupportedMediaType", err)
	}
}

func TestToAny(t *testing.T) {
	kv := func(key string, value *commonpb.AnyValue) *commonpb.KeyValue {
		return &commonpb.KeyValue{Key: key, Value: value}
	}
	tests := []struct {
		name string
		in   *commonpb.AnyValue
		want any
	}{
		{"string", &commonpb.AnyValue{Value: &commonpb.AnyValue_StringValue{StringValue: "x"}}, "x"},
		{"bool", &commonpb.AnyValue{Value: &commonpb.AnyValue_BoolValue{BoolValue: true}}, true},
		{"int", &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 42}}, int64(42)},
		{"double", &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: 1.5}}, 1.5},
		{"bytes", &commonpb.AnyValue{Value: &commonpb.AnyValue_BytesValue{BytesValue: []byte{0, 1, 2}}}, base64.StdEncoding.EncodeToString([]byte{0, 1, 2})},
		{"array", &commonpb.AnyValue{Value: &commonpb.AnyValue_ArrayValue{ArrayValue: &commonpb.ArrayValue{Values: []*commonpb.AnyValue{{Value: &commonpb.AnyValue_StringValue{StringValue: "x"}}}}}}, []any{"x"}},
		{"kvlist duplicate", &commonpb.AnyValue{Value: &commonpb.AnyValue_KvlistValue{KvlistValue: &commonpb.KeyValueList{Values: []*commonpb.KeyValue{kv("x", &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 1}}), kv("x", &commonpb.AnyValue{Value: &commonpb.AnyValue_IntValue{IntValue: 2}})}}}}, map[string]any{"x": int64(2)}},
		{"nil", nil, nil},
		{"unset", &commonpb.AnyValue{}, nil},
		{"NaN", &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.NaN()}}, "NaN"},
		{"positive infinity", &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.Inf(1)}}, "Infinity"},
		{"negative infinity", &commonpb.AnyValue{Value: &commonpb.AnyValue_DoubleValue{DoubleValue: math.Inf(-1)}}, "-Infinity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ToAny(tt.in); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestGunzipCap(t *testing.T) {
	compressed := gzipBytes(t, make([]byte, 1<<20))
	if _, err := Gunzip(bytes.NewReader(compressed), 512<<10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v, want ErrTooLarge", err)
	}
}

func TestDecodeLogsMergesGenAIRequestAndResponse(t *testing.T) {
	jsonBody, err := os.ReadFile("testdata/gemini_cli_logs.json")
	if err != nil {
		t.Fatal(err)
	}
	var request collectorlogspb.ExportLogsServiceRequest
	if err := protojson.Unmarshal(jsonBody, &request); err != nil {
		t.Fatal(err)
	}
	protoBody, err := proto.Marshal(&request)
	if err != nil {
		t.Fatal(err)
	}

	var first []RawSpan
	for _, input := range []struct {
		body      []byte
		mediaType string
	}{{jsonBody, ContentTypeJSON}, {protoBody, ContentTypeProto}, {jsonBody, ContentTypeJSON}} {
		spans, err := DecodeLogs(input.body, input.mediaType, t.Logf)
		if err != nil {
			t.Fatal(err)
		}
		if len(spans) != 1 {
			t.Fatalf("got %d spans, want 1", len(spans))
		}
		if first == nil {
			first = spans
		} else if spans[0].TraceID != first[0].TraceID || spans[0].SpanID != first[0].SpanID {
			t.Fatalf("IDs are not deterministic: got %s/%s, want %s/%s", spans[0].TraceID, spans[0].SpanID, first[0].TraceID, first[0].SpanID)
		}
	}

	span := first[0]
	if len(span.TraceID) != 32 || len(span.SpanID) != 16 {
		t.Fatalf("invalid IDs: %q/%q", span.TraceID, span.SpanID)
	}
	if span.StartNs != 1789539115130000000 || span.EndNs != 1789539117346000000 {
		t.Fatalf("times = %d..%d", span.StartNs, span.EndNs)
	}
	if span.Attrs["gen_ai.usage.input_tokens"] != int64(9097) || span.Attrs["gen_ai.input.messages"] == nil || span.Attrs["gen_ai.output.messages"] == nil {
		t.Fatalf("merged attrs missing: %#v", span.Attrs)
	}
	if span.Attrs["event.name"] != nil || span.Attrs["spanbox.source"] != "otlp-logs" {
		t.Fatalf("unexpected adapter attrs: %#v", span.Attrs)
	}
}
