package proxy

import (
	"context"
	"strings"
	"time"

	"github.com/Ray0907/spanbox/internal/normalize"
	"github.com/Ray0907/spanbox/internal/otlp"
	"github.com/Ray0907/spanbox/internal/store"
)

// turnAnchor returns the last real user message, ignoring tool-only continuations.
func turnAnchor(exchange Exchange, request map[string]any) (any, int) {
	key := "messages"
	if exchange.Vendor == "gemini" {
		key = "contents"
	} else if strings.HasSuffix(exchange.Path, "/responses") {
		key = "input"
	}
	if input, ok := request[key].(string); ok && key == "input" {
		return input, 0
	}
	items := array(request[key])
	for i := len(items) - 1; i >= 0; i-- {
		message := objectOf(items[i])
		if text(message["role"]) != "user" {
			continue
		}
		switch exchange.Vendor {
		case "anthropic":
			if _, ok := message["content"].(string); ok {
				return message, i
			}
			for _, block := range array(message["content"]) {
				if text(objectOf(block)["type"]) != "tool_result" {
					return message, i
				}
			}
		case "gemini":
			for _, part := range array(message["parts"]) {
				p := objectOf(part)
				if _, ok := p["text"].(string); ok && p["functionResponse"] == nil {
					return message, i
				}
			}
		default:
			if key != "input" || message["type"] == nil || text(message["type"]) == "message" {
				return message, i
			}
		}
	}
	return nil, -1
}

// Extract only visible text, never tool arguments, tool results or reasoning.
func turnText(value any) string {
	switch v := value.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if s := turnText(item); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if typ := text(v["type"]); typ != "" && typ != "message" && typ != "text" && typ != "input_text" && typ != "output_text" {
			return ""
		}
		if s, ok := v["text"].(string); ok {
			return s
		}
		if content, ok := v["content"]; ok {
			return turnText(content)
		}
		return turnText(v["parts"])
	}
	return ""
}

func (h *Handler) proxyTurn(exchange Exchange, raw otlp.RawSpan, request map[string]any, response parsedResponse) (*store.ProxyTurn, error) {
	session := text(raw.Attrs["spanbox.session"])
	if session == "" {
		session = text(raw.Attrs["gen_ai.conversation.id"])
	}
	if session == "" {
		return nil, nil
	}
	anchor, index := turnAnchor(exchange, request)
	var turn store.ProxyTurn
	if index < 0 {
		previous, found, err := h.store.GetProxyTurn(context.Background(), "", exchange.route(), session)
		if err != nil {
			return nil, err
		}
		if found {
			turn = previous
		} else {
			anchor = raw.SpanID
		}
	}
	if turn.Key == "" {
		turn.Key = jsonString([]any{exchange.route(), session, index, hash(jsonString(anchor), 16)})
		turn.Route, turn.ScopeSession = exchange.route(), session
	}
	attrs := map[string]any{"gen_ai.operation.name": "invoke_agent", "spanbox.source": "proxy"}
	for _, key := range []string{"spanbox.session", "gen_ai.conversation.id", "user.id"} {
		if value, ok := raw.Attrs[key]; ok {
			attrs[key] = value
		}
	}
	root, err := normalize.Span(otlp.RawSpan{
		TraceID: hash("spanbox-proxy-turn-trace"+turn.Key, 16), SpanID: hash("spanbox-proxy-turn-root"+turn.Key, 8),
		Name: "invoke_agent " + text(raw.Resource["service.name"]), StartNs: raw.StartNs, EndNs: raw.EndNs,
		StatusCode: raw.StatusCode, StatusMessage: raw.StatusMessage, Attrs: attrs, Resource: raw.Resource, Scope: raw.Scope,
		Events: []map[string]any{}, Links: []map[string]any{},
	}, h.logf)
	if err != nil {
		return nil, err
	}
	root.InputContent = turnText(anchor)
	if index < 0 {
		root.InputContent = turn.Root.InputContent
	}
	root.OutputContent = turnText(response.outputMessages)
	turn.Root = root
	turn.CreatedNs = time.Now().UnixNano()
	return &turn, nil
}
