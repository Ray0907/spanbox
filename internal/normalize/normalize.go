package normalize

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/otlp"
	"github.com/Ray0907/spanbox/internal/store"
)

type candidate struct {
	key   string
	value any
}

func Span(raw otlp.RawSpan, logf func(format string, args ...any)) store.Span {
	kind := detectKind(raw.Attrs, logf)
	result := store.Span{
		TraceID: raw.TraceID, SpanID: raw.SpanID, ParentSpanID: raw.ParentSpanID, Name: raw.Name, Kind: kind,
		ServiceName: "unknown", StartNs: raw.StartNs, EndNs: raw.EndNs, DurationMs: float64(raw.EndNs-raw.StartNs) / 1e6,
		StatusCode: raw.StatusCode, StatusMessage: raw.StatusMessage, TraceState: raw.TraceState,
		Attributes: jsonText(raw.Attrs, "{}", logf), Events: jsonText(raw.Events, "[]", logf),
		Links: jsonText(raw.Links, "[]", logf), Resource: jsonText(raw.Resource, "{}", logf), Scope: jsonText(raw.Scope, "{}", logf),
	}
	result.ServiceName = firstString(raw.Resource, logf, "service.name")
	if result.ServiceName == "" {
		result.ServiceName = "unknown"
	}
	result.Provider = firstString(raw.Attrs, logf, "gen_ai.provider.name", "gen_ai.system", "llm.provider", "llm.system", "ai.model.provider")
	result.RequestModel = firstString(raw.Attrs, logf, "gen_ai.request.model", "llm.request.model_name", "llm.model_name", "langfuse.observation.model.name", "ai.model.id", "embedding.model_name")
	result.ResponseModel = firstString(raw.Attrs, logf, "gen_ai.response.model", "llm.response.model_name", "ai.response.model")

	result.InputTokens = firstToken(raw.Attrs, logf, "gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt", "ai.usage.promptTokens")
	if result.InputTokens == nil && kind == "embedding" {
		result.InputTokens = firstToken(raw.Attrs, logf, "ai.usage.tokens")
	}
	result.OutputTokens = firstToken(raw.Attrs, logf, "gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion", "ai.usage.completionTokens")
	result.CacheReadTokens = firstToken(raw.Attrs, logf, "gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens", "llm.token_count.prompt_details.cache_read")
	if details, ok := usageDetails(raw.Attrs["langfuse.observation.usage_details"], logf); ok {
		if result.InputTokens == nil {
			result.InputTokens = summedTokens(details, "input", "input_", logf)
		}
		if result.OutputTokens == nil {
			result.OutputTokens = summedTokens(details, "output", "output_", logf)
		}
		if result.CacheReadTokens == nil {
			result.CacheReadTokens = firstToken(details, logf, "input_cached_tokens", "cache_read_input_tokens")
		}
	}

	if cost, ok := explicitCost(raw.Attrs, logf); ok {
		result.CostUSD = &cost
		result.CostSource = "explicit"
	}
	result.InputContent = inputContent(raw.Attrs, logf)
	result.OutputContent = outputContent(raw.Attrs, logf)
	toolKeys := []string{"gen_ai.tool.name", "tool.name", "ai.toolCall.name"}
	if kind == "tool" {
		toolKeys = append(toolKeys, "traceloop.entity.name")
	}
	result.ToolName = firstString(raw.Attrs, logf, toolKeys...)
	result.ToolCallID = firstString(raw.Attrs, logf, "gen_ai.tool.call.id", "tool.id", "ai.toolCall.id")
	result.FinishReason = finishReason(raw.Attrs, logf)
	result.SessionID = firstString(raw.Attrs, logf, "gen_ai.conversation.id", "session.id", "langfuse.session.id", "traceloop.correlation.id")
	result.UserID = firstString(raw.Attrs, logf, "user.id", "langfuse.user.id", "enduser.id", "gen_ai.user", "llm.user")
	return result
}

func firstString(values map[string]any, logf func(string, ...any), keys ...string) string {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			warn(logf, "%s must be a string, got %T", key, value)
			continue
		}
		if text != "" {
			return text
		}
	}
	return ""
}

func firstToken(values map[string]any, logf func(string, ...any), keys ...string) *int64 {
	for _, key := range keys {
		value, exists := values[key]
		if !exists {
			continue
		}
		if token, ok := tokenValue(value); ok {
			return &token
		}
		warn(logf, "%s has invalid token value %v", key, value)
	}
	return nil
}

func tokenValue(value any) (int64, bool) {
	var number float64
	switch value := value.(type) {
	case int64:
		if value < 0 || value > config.MaxTokens {
			return 0, false
		}
		return value, true
	case float64:
		number = value
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	case json.Number:
		parsed, err := strconv.ParseFloat(string(value), 64)
		if err != nil {
			return 0, false
		}
		number = parsed
	default:
		return 0, false
	}
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > float64(config.MaxTokens) || math.Trunc(number) != number {
		return 0, false
	}
	return int64(number), true
}

func usageDetails(value any, logf func(string, ...any)) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	if object, ok := value.(map[string]any); ok {
		return object, true
	}
	text, ok := value.(string)
	if !ok {
		warn(logf, "langfuse.observation.usage_details must be JSON, got %T", value)
		return nil, false
	}
	var object map[string]any
	if err := json.Unmarshal([]byte(text), &object); err != nil {
		warn(logf, "invalid langfuse.observation.usage_details: %v", err)
		return nil, false
	}
	return object, true
}

func summedTokens(values map[string]any, exact, prefix string, logf func(string, ...any)) *int64 {
	var total int64
	found := false
	for key, value := range values {
		if key != exact && !strings.HasPrefix(key, prefix) {
			continue
		}
		token, ok := tokenValue(value)
		if !ok {
			warn(logf, "usage detail %s has invalid token value %v", key, value)
			continue
		}
		total += token
		found = true
	}
	if !found || total > config.MaxTokens {
		return nil
	}
	return &total
}

func explicitCost(attrs map[string]any, logf func(string, ...any)) (float64, bool) {
	if value, exists := attrs["langfuse.observation.cost_details"]; exists {
		var details map[string]any
		switch value := value.(type) {
		case string:
			if err := json.Unmarshal([]byte(value), &details); err != nil {
				warn(logf, "invalid langfuse.observation.cost_details: %v", err)
			}
		case map[string]any:
			details = value
		default:
			warn(logf, "langfuse.observation.cost_details must be JSON, got %T", value)
		}
		if details != nil {
			if total, ok := costNumber(details["total"]); ok {
				return total, true
			}
			var total float64
			found := false
			for key, value := range details {
				if key == "total" {
					continue
				}
				if amount, ok := costNumber(value); ok {
					total += amount
					found = true
				}
			}
			if found {
				return total, true
			}
		}
	}
	if value, exists := attrs["llm.cost.total"]; exists {
		if cost, ok := costNumber(value); ok {
			return cost, true
		}
		warn(logf, "llm.cost.total has invalid cost value %v", value)
	}
	return 0, false
}

func costNumber(value any) (float64, bool) {
	var result float64
	switch value := value.(type) {
	case float64:
		result = value
	case int64:
		result = float64(value)
	case json.Number:
		parsed, err := value.Float64()
		if err != nil {
			return 0, false
		}
		result = parsed
	case string:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
		result = parsed
	default:
		return 0, false
	}
	return result, result >= 0 && !math.IsNaN(result) && !math.IsInf(result, 0)
}

func inputContent(attrs map[string]any, logf func(string, ...any)) string {
	special := make(map[string]any, 2)
	for key, name := range map[string]string{"gen_ai.system_instructions": "system_instructions", "gen_ai.input.messages": "messages"} {
		if value, exists := attrs[key]; exists {
			special[name] = value
		}
	}
	if len(special) > 0 {
		return jsonText(special, "", logf)
	}
	if value := firstContent(attrs, logf, "langfuse.observation.input", "input.value", "traceloop.entity.input", "ai.prompt.messages", "ai.prompt", "ai.value", "ai.values", "gen_ai.prompt"); value != "" {
		return value
	}
	for _, prefix := range []string{"gen_ai.prompt", "llm.input_messages", "llm.prompts"} {
		if value, ok := RebuildIndexed(attrs, prefix); ok {
			return jsonText(value, "", logf)
		}
	}
	return firstContent(attrs, logf, "ai.toolCall.args", "gen_ai.tool.call.arguments")
}

func outputContent(attrs map[string]any, logf func(string, ...any)) string {
	if value := firstContent(attrs, logf, "gen_ai.output.messages", "langfuse.observation.output", "output.value", "traceloop.entity.output", "ai.response.text", "ai.response.toolCalls", "ai.response.object", "ai.embedding", "ai.embeddings", "gen_ai.completion"); value != "" {
		return value
	}
	for _, prefix := range []string{"gen_ai.completion", "llm.output_messages", "llm.choices"} {
		if value, ok := RebuildIndexed(attrs, prefix); ok {
			return jsonText(value, "", logf)
		}
	}
	return firstContent(attrs, logf, "ai.toolCall.result", "gen_ai.tool.call.result")
}

func firstContent(attrs map[string]any, logf func(string, ...any), keys ...string) string {
	for _, key := range keys {
		value, exists := attrs[key]
		if !exists {
			continue
		}
		switch value := value.(type) {
		case string:
			if value != "" {
				return value
			}
		case []any, map[string]any:
			return jsonText(value, "", logf)
		default:
			warn(logf, "%s must be a string, array, or object, got %T", key, value)
		}
	}
	return ""
}

func finishReason(attrs map[string]any, logf func(string, ...any)) string {
	if value, exists := attrs["gen_ai.response.finish_reasons"]; exists {
		items, ok := value.([]any)
		if ok {
			parts := make([]string, 0, len(items))
			for _, item := range items {
				if text, ok := item.(string); ok && text != "" {
					parts = append(parts, text)
				} else {
					warn(logf, "gen_ai.response.finish_reasons item must be a string, got %T", item)
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, ",")
			}
		} else {
			warn(logf, "gen_ai.response.finish_reasons must be an array, got %T", value)
		}
	}
	return firstString(attrs, logf, "gen_ai.response.finish_reason", "llm.finish_reason", "ai.response.finishReason")
}

func jsonText(value any, fallback string, logf func(string, ...any)) string {
	body, err := json.Marshal(value)
	if err != nil {
		warn(logf, "cannot encode JSON: %v", err)
		return fallback
	}
	return string(body)
}

func warn(logf func(string, ...any), format string, args ...any) {
	if logf != nil {
		logf("warning: "+format, args...)
	}
}
