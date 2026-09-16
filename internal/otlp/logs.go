package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const genAIEventName = "gen_ai.client.inference.operation.details"

type logRecord struct {
	record   *logspb.LogRecord
	attrs    map[string]any
	resource map[string]any
	scope    map[string]any
	time     int64
}

func DecodeLogs(body []byte, mediaType string, logf func(string, ...any)) ([]RawSpan, error) {
	var request collectorlogspb.ExportLogsServiceRequest
	switch mediaType {
	case ContentTypeProto:
		if err := proto.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode logs protobuf: %w", err)
		}
	case ContentTypeJSON:
		if err := protojson.Unmarshal(body, &request); err != nil {
			return nil, fmt.Errorf("decode logs JSON: %w", err)
		}
	default:
		return nil, ErrUnsupportedMediaType
	}

	var result []RawSpan
	ignored, unpaired := 0, 0
	for _, resourceLogs := range request.ResourceLogs {
		resource := attributes(resourceLogs.GetResource().GetAttributes())
		pending := make(map[string][]logRecord)
		for _, scopeLogs := range resourceLogs.ScopeLogs {
			scope := map[string]any{
				"name":       scopeLogs.GetScope().GetName(),
				"version":    scopeLogs.GetScope().GetVersion(),
				"attributes": attributes(scopeLogs.GetScope().GetAttributes()),
			}
			for _, record := range scopeLogs.LogRecords {
				attrs := attributes(record.Attributes)
				if attrs["event.name"] != genAIEventName {
					ignored++
					continue
				}
				timestamp, err := logTimestamp(record)
				if err != nil {
					return nil, err
				}
				current := logRecord{record: record, attrs: attrs, resource: resource, scope: scope, time: timestamp}
				model, _ := attrs["gen_ai.request.model"].(string)
				if !isGenAIResponse(attrs) {
					pending[model] = append(pending[model], current)
					continue
				}
				var requestRecord *logRecord
				for i, candidate := range pending[model] {
					if candidate.time <= current.time {
						requestRecord = &candidate
						pending[model] = append(pending[model][:i], pending[model][i+1:]...)
						break
					}
				}
				result = append(result, rawLogSpan(current, requestRecord))
			}
		}
		for _, records := range pending {
			unpaired += len(records)
		}
	}
	if logf != nil && ignored > 0 {
		logf("debug: ignored %d non-GenAI OTLP log records", ignored)
	}
	if logf != nil && unpaired > 0 {
		logf("debug: dropped %d unpaired GenAI request log records", unpaired)
	}
	return result, nil
}

func logTimestamp(record *logspb.LogRecord) (int64, error) {
	timestamp := record.ObservedTimeUnixNano
	if timestamp == 0 {
		timestamp = record.TimeUnixNano
	}
	if timestamp > math.MaxInt64 {
		return 0, fmt.Errorf("log timestamp exceeds int64")
	}
	return int64(timestamp), nil
}

func isGenAIResponse(attrs map[string]any) bool {
	for key := range attrs {
		if strings.HasPrefix(key, "gen_ai.usage.") || key == "gen_ai.output.messages" || strings.HasPrefix(key, "gen_ai.response.") {
			return true
		}
	}
	return false
}

func rawLogSpan(response logRecord, request *logRecord) RawSpan {
	attrs := make(map[string]any, len(response.attrs)+1)
	start := response.time
	if request != nil {
		for key, value := range request.attrs {
			attrs[key] = value
		}
		start = request.time
	}
	for key, value := range response.attrs {
		attrs[key] = value
	}
	delete(attrs, "event.name")
	attrs["spanbox.source"] = "otlp-logs"
	end := response.time
	if start == 0 {
		start = end
	}
	if end == 0 {
		end = start
	}
	if end < start {
		start, end = end, start
	}

	spanID := validLogID(response.record.SpanId, 8)
	if spanID == "" {
		spanID = hashID("spanbox-log-span"+stringValue(response.resource, "session.id")+stringValue(attrs, "gen_ai.response.id")+strconv.FormatInt(end, 10), 8)
	}
	traceID := validLogID(response.record.TraceId, 16)
	if traceID == "" {
		sessionID := stringValue(response.resource, "session.id")
		if sessionID == "" {
			sessionID = stringValue(attrs, "gen_ai.conversation.id")
		}
		if sessionID == "" {
			sessionID = stringValue(attrs, "session.id")
		}
		seed := sessionID
		if seed == "" {
			seed = spanID
		}
		traceID = hashID("spanbox-log-trace"+seed, 16)
	}

	statusCode := int32(0)
	if _, exists := attrs["error.type"]; exists {
		statusCode = 2
	}
	return RawSpan{
		TraceID: traceID, SpanID: spanID,
		Name:    strings.TrimSpace(stringValue(attrs, "gen_ai.operation.name") + " " + stringValue(attrs, "gen_ai.request.model")),
		StartNs: start, EndNs: end, StatusCode: statusCode,
		Attrs: attrs, Events: []map[string]any{}, Links: []map[string]any{}, Resource: response.resource, Scope: response.scope,
	}
}

func validLogID(id []byte, size int) string {
	if validID("id", id, size, false) != nil {
		return ""
	}
	return hex.EncodeToString(id)
}

func hashID(seed string, size int) string {
	hash := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(hash[:size])
}

func stringValue(values map[string]any, key string) string {
	value, _ := values[key].(string)
	return value
}
