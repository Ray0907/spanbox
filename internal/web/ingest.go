package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/normalize"
	"github.com/Ray0907/spanbox/internal/otlp"
	"github.com/Ray0907/spanbox/internal/store"
	collectorlogspb "go.opentelemetry.io/proto/otlp/collector/logs/v1"
	collectormetricspb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

const (
	signalTraces  = "traces"
	signalLogs    = "logs"
	signalMetrics = "metrics"
)

var (
	metricsReceived atomic.Uint64
	metricsLogOnce  sync.Once
)

func (deps Deps) limitIngest(w http.ResponseWriter, r *http.Request) {
	select {
	case deps.ingestSlots <- struct{}{}:
		defer func() { <-deps.ingestSlots }()
		deps.ingest(w, r)
	default:
		w.Header().Set("Retry-After", "1")
		writeStatus(w, responseMediaType(r), http.StatusServiceUnavailable, 14, "ingest capacity exhausted; retry later")
	}
}

func (deps Deps) ingest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeStatus(w, responseMediaType(r), http.StatusMethodNotAllowed, 3, "POST required")
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != otlp.ContentTypeJSON && mediaType != otlp.ContentTypeProto {
		writeStatus(w, otlp.ContentTypeJSON, http.StatusUnsupportedMediaType, 3, "unsupported content type")
		return
	}
	encoding := strings.ToLower(strings.TrimSpace(r.Header.Get("Content-Encoding")))
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		writeStatus(w, mediaType, http.StatusUnsupportedMediaType, 3, "unsupported content encoding")
		return
	}
	limited := http.MaxBytesReader(w, r.Body, config.MaxBodyBytes)
	var body []byte
	if encoding == "gzip" {
		body, err = otlp.Gunzip(limited, config.MaxBodyBytes)
	} else {
		body, err = io.ReadAll(limited)
	}
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.Is(err, otlp.ErrTooLarge) || errors.As(err, &maxBytesError) {
			writeStatus(w, mediaType, http.StatusRequestEntityTooLarge, 8, "payload too large")
			return
		}
		writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
		return
	}
	signal, err := ingestSignal(r.URL.Path, mediaType, body)
	if err != nil {
		writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
		return
	}
	if signal == signalMetrics {
		metricsReceived.Add(1)
		metricsLogOnce.Do(func() { deps.Logf("metrics received and discarded") })
		writeExportResponse(w, mediaType, &collectormetricspb.ExportMetricsServiceResponse{})
		return
	}
	var rawSpans []otlp.RawSpan
	if signal == signalLogs {
		rawSpans, err = deps.Logs.Decode(body, mediaType, deps.Logf)
	} else {
		rawSpans, err = otlp.Decode(body, mediaType)
	}
	if err != nil {
		writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
		return
	}
	spans := make([]store.Span, 0, len(rawSpans))
	for _, raw := range rawSpans {
		span, err := normalize.Span(raw, deps.Logf)
		if err != nil {
			if errors.Is(err, normalize.ErrIndexedPath) || errors.Is(err, store.ErrCostOutOfRange) {
				writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
			} else {
				deps.Logf("normalize span: %v", err)
				writeStatus(w, mediaType, http.StatusInternalServerError, 13, "normalization error")
			}
			return
		}
		if (span.Kind == "llm" || span.Kind == "embedding") && span.CostUSD == nil && span.InputTokens != nil && span.OutputTokens != nil {
			if cost, ok := deps.Pricing.Cost(span.Provider, span.RequestModel, span.ResponseModel, span.InputTokens, span.OutputTokens, span.CacheReadTokens, nil); ok {
				span.CostUSD = &cost
				span.CostSource = "pricing"
			}
		}
		spans = append(spans, span)
	}
	if err := deps.Store.InsertBatch(r.Context(), spans); err != nil {
		if errors.Is(err, store.ErrCostOutOfRange) {
			writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
		} else {
			deps.Logf("insert trace batch: %v", err)
			writeStatus(w, mediaType, http.StatusInternalServerError, 13, "database error")
		}
		return
	}
	if signal == signalLogs {
		writeExportResponse(w, mediaType, &collectorlogspb.ExportLogsServiceResponse{})
	} else {
		writeExportResponse(w, mediaType, &collectortracepb.ExportTraceServiceResponse{})
	}
}

func ingestSignal(path, mediaType string, body []byte) (string, error) {
	switch path {
	case "/v1/traces", "/api/public/otel/v1/traces":
		return signalTraces, nil
	case "/v1/logs":
		return signalLogs, nil
	case "/v1/metrics":
		return signalMetrics, nil
	}
	if path != "/" {
		return "", fmt.Errorf("unknown ingest path")
	}
	if mediaType == otlp.ContentTypeProto {
		return signalTraces, nil
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", fmt.Errorf("decode JSON envelope: %w", err)
	}
	for key, signal := range map[string]string{"resourceSpans": signalTraces, "resourceLogs": signalLogs, "resourceMetrics": signalMetrics} {
		if _, ok := envelope[key]; ok {
			return signal, nil
		}
	}
	return "", fmt.Errorf("unknown OTLP signal")
}

func writeExportResponse(w http.ResponseWriter, mediaType string, response proto.Message) {
	var body []byte
	if mediaType == otlp.ContentTypeProto {
		body, _ = proto.Marshal(response)
	} else {
		body, _ = protojson.Marshal(response)
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func responseMediaType(r *http.Request) string {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err == nil && mediaType == otlp.ContentTypeProto {
		return otlp.ContentTypeProto
	}
	return otlp.ContentTypeJSON
}

func writeStatus(w http.ResponseWriter, mediaType string, httpCode int, rpcCode int32, message string) {
	status := &statuspb.Status{Code: rpcCode, Message: message}
	var body []byte
	if mediaType == otlp.ContentTypeProto {
		body, _ = proto.Marshal(status)
	} else {
		mediaType = otlp.ContentTypeJSON
		body, _ = protojson.Marshal(status)
	}
	w.Header().Set("Content-Type", mediaType)
	w.WriteHeader(httpCode)
	_, _ = w.Write(body)
}
