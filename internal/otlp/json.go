package otlp

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// OTLP/JSON encodes trace and span ids as hex, unlike the standard protobuf
// JSON mapping that protojson implements, which expects base64. Hex ids are
// rewritten to base64 before decoding; base64 ids are still accepted.
// Lengths cannot collide: 16 and 8 bytes are 32 and 16 hex characters but 24
// and 12 base64 characters.
var otlpIDHexLengths = map[string]int{
	"traceId":        32,
	"trace_id":       32,
	"spanId":         16,
	"span_id":        16,
	"parentSpanId":   16,
	"parent_span_id": 16,
}

func unmarshalOTLPJSON(body []byte, message proto.Message) error {
	return protojson.Unmarshal(hexIDsToBase64(body), message)
}

// hexIDsToBase64 returns body unchanged when it holds no hex ids or is not a
// single valid JSON value, leaving error reporting to protojson.
func hexIDsToBase64(body []byte) []byte {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var document any
	if err := decoder.Decode(&document); err != nil {
		return body
	}
	if _, err := decoder.Token(); err != io.EOF {
		return body
	}
	if !rewriteHexIDs(document) {
		return body
	}
	rewritten, err := json.Marshal(document)
	if err != nil {
		return body
	}
	return rewritten
}

func rewriteHexIDs(value any) bool {
	changed := false
	switch value := value.(type) {
	case map[string]any:
		for key, item := range value {
			if id, ok := item.(string); ok {
				if size, isID := otlpIDHexLengths[key]; isID && len(id) == size {
					if decoded, err := hex.DecodeString(id); err == nil {
						value[key] = base64.StdEncoding.EncodeToString(decoded)
						changed = true
					}
				}
				continue
			}
			if rewriteHexIDs(item) {
				changed = true
			}
		}
	case []any:
		for _, item := range value {
			if rewriteHexIDs(item) {
				changed = true
			}
		}
	}
	return changed
}
