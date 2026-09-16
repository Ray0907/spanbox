package otlp

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	logspb "go.opentelemetry.io/proto/otlp/logs/v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	genAIEventName        = "gen_ai.client.inference.operation.details"
	maxPendingLogRequests = 1000
	pendingLogTTL         = 10 * time.Minute
)

type logRecord struct {
	record   *logspb.LogRecord
	attrs    map[string]any
	resource map[string]any
	scope    map[string]any
	time     int64
}

type pendingLogKey struct {
	resourceID string
	model      string
}

type pendingLogRecord struct {
	attrs      map[string]any
	resource   map[string]any
	scope      map[string]any
	time       int64
	insertedAt time.Time
	order      uint64
}

type LogsAdapter struct {
	mu           sync.Mutex
	pending      map[pendingLogKey][]pendingLogRecord
	pendingCount int
	nextOrder    uint64
	now          func() time.Time
}

func NewLogsAdapter() *LogsAdapter {
	return &LogsAdapter{pending: make(map[pendingLogKey][]pendingLogRecord), now: time.Now}
}

func (a *LogsAdapter) Decode(body []byte, mediaType string, logf func(string, ...any)) ([]RawSpan, error) {
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

	a.mu.Lock()
	defer a.mu.Unlock()
	if a.pending == nil {
		a.pending = make(map[pendingLogKey][]pendingLogRecord)
	}
	if a.now == nil {
		a.now = time.Now
	}

	var result []RawSpan
	ignored, expired, capacityDropped := 0, 0, 0
	for _, resourceLogs := range request.ResourceLogs {
		resource := attributes(resourceLogs.GetResource().GetAttributes())
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
				key := pendingKey(resource, attrs)
				now := a.now()
				expired += a.expire(now)
				if !isGenAIResponse(attrs) {
					capacityDropped += a.addPending(key, current, now)
					continue
				}
				result = append(result, rawLogSpan(current, a.popPending(key, current.time)))
			}
		}
	}
	if logf != nil && ignored > 0 {
		logf("debug: ignored %d non-GenAI OTLP log records", ignored)
	}
	if logf != nil && expired > 0 {
		logf("debug: dropped %d expired GenAI request log records", expired)
	}
	if logf != nil && capacityDropped > 0 {
		logf("debug: dropped %d GenAI request log records at capacity", capacityDropped)
	}
	return result, nil
}

func pendingKey(resource, attrs map[string]any) pendingLogKey {
	resourceID := stringValue(resource, "session.id")
	if resourceID == "" {
		resourceID = stringValue(resource, "service.instance.id")
	}
	if resourceID == "" {
		resourceID = stringValue(resource, "service.name")
	}
	return pendingLogKey{resourceID: resourceID, model: stringValue(attrs, "gen_ai.request.model")}
}

func (a *LogsAdapter) addPending(key pendingLogKey, record logRecord, now time.Time) int {
	dropped := 0
	if a.pendingCount >= maxPendingLogRequests {
		a.dropOldest()
		dropped = 1
	}
	a.nextOrder++
	a.pending[key] = append(a.pending[key], pendingLogRecord{
		attrs: record.attrs, resource: record.resource, scope: record.scope, time: record.time,
		insertedAt: now, order: a.nextOrder,
	})
	a.pendingCount++
	return dropped
}

func (a *LogsAdapter) popPending(key pendingLogKey, responseTime int64) *pendingLogRecord {
	records := a.pending[key]
	for i := range records {
		if records[i].time > responseTime {
			continue
		}
		match := records[i]
		records = append(records[:i], records[i+1:]...)
		if len(records) == 0 {
			delete(a.pending, key)
		} else {
			a.pending[key] = records
		}
		a.pendingCount--
		return &match
	}
	return nil
}

func (a *LogsAdapter) expire(now time.Time) int {
	cutoff := now.Add(-pendingLogTTL)
	dropped := 0
	for key, records := range a.pending {
		first := 0
		for first < len(records) && records[first].insertedAt.Before(cutoff) {
			first++
		}
		if first == 0 {
			continue
		}
		dropped += first
		if first == len(records) {
			delete(a.pending, key)
		} else {
			a.pending[key] = records[first:]
		}
	}
	a.pendingCount -= dropped
	return dropped
}

func (a *LogsAdapter) dropOldest() {
	var oldestKey pendingLogKey
	var oldest pendingLogRecord
	found := false
	for key, records := range a.pending {
		if len(records) == 0 {
			continue
		}
		candidate := records[0]
		if !found || candidate.insertedAt.Before(oldest.insertedAt) || candidate.insertedAt.Equal(oldest.insertedAt) && candidate.order < oldest.order {
			oldestKey, oldest, found = key, candidate, true
		}
	}
	if !found {
		return
	}
	// ponytail: the queue is capped at 1000; use a heap only if this scan is measured hot.
	records := a.pending[oldestKey][1:]
	if len(records) == 0 {
		delete(a.pending, oldestKey)
	} else {
		a.pending[oldestKey] = records
	}
	a.pendingCount--
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

func rawLogSpan(response logRecord, request *pendingLogRecord) RawSpan {
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
