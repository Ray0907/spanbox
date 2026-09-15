package web

import (
	"errors"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/normalize"
	"github.com/Ray0907/spanbox/internal/otlp"
	"github.com/Ray0907/spanbox/internal/store"
	collectortracepb "go.opentelemetry.io/proto/otlp/collector/trace/v1"
	statuspb "google.golang.org/genproto/googleapis/rpc/status"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

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
	rawSpans, err := otlp.Decode(body, mediaType)
	if err != nil {
		writeStatus(w, mediaType, http.StatusBadRequest, 3, err.Error())
		return
	}
	spans := make([]store.Span, 0, len(rawSpans))
	for _, raw := range rawSpans {
		span := normalize.Span(raw, deps.Logf)
		if (span.Kind == "llm" || span.Kind == "embedding") && span.CostUSD == nil && span.InputTokens != nil && span.OutputTokens != nil {
			if cost, ok := deps.Pricing.Cost(span.Provider, span.RequestModel, span.ResponseModel, span.InputTokens, span.OutputTokens, span.CacheReadTokens); ok {
				span.CostUSD = &cost
				span.CostSource = "pricing"
			}
		}
		spans = append(spans, span)
	}
	if err := deps.Store.InsertBatch(r.Context(), spans); err != nil {
		deps.Logf("insert trace batch: %v", err)
		writeStatus(w, mediaType, http.StatusInternalServerError, 13, "database error")
		return
	}
	w.Header().Set("Content-Type", mediaType)
	response := &collectortracepb.ExportTraceServiceResponse{}
	if mediaType == otlp.ContentTypeProto {
		body, _ = proto.Marshal(response)
	} else {
		body, _ = protojson.Marshal(response)
	}
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
