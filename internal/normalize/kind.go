package normalize

import "strings"

var kindLayers = []struct {
	key    string
	values map[string]string
}{
	{"openinference.span.kind", map[string]string{
		"LLM": "llm", "EMBEDDING": "embedding", "TOOL": "tool", "RETRIEVER": "retrieval", "RERANKER": "retrieval",
		"AGENT": "agent", "CHAIN": "other", "GUARDRAIL": "other", "EVALUATOR": "other", "PROMPT": "other",
	}},
	{"langfuse.observation.type", map[string]string{
		"generation": "llm", "embedding": "embedding", "tool": "tool", "retriever": "retrieval", "agent": "agent",
		"span": "other", "chain": "other", "evaluator": "other", "guardrail": "other", "event": "other",
	}},
	{"traceloop.span.kind", map[string]string{"tool": "tool", "agent": "agent", "workflow": "other", "task": "other"}},
	{"llm.request.type", map[string]string{"chat": "llm", "completion": "llm", "embedding": "embedding", "rerank": "retrieval"}},
	{"gen_ai.operation.name", map[string]string{
		"chat": "llm", "generate_content": "llm", "text_completion": "llm", "llm_request": "llm", "embeddings": "embedding",
		"execute_tool": "tool", "retrieval": "retrieval", "search_memory": "retrieval", "vector_db_retrieve": "retrieval",
		"rerank": "retrieval", "invoke_agent": "agent", "create_agent": "agent", "invoke_workflow": "agent", "plan": "agent", "agent_step": "agent",
	}},
}

func DetectKind(attrs map[string]any) string { return detectKind(attrs, nil) }

func detectKind(attrs map[string]any, logf func(string, ...any)) string {
	for i, layer := range kindLayers {
		value, exists := attrs[layer.key]
		if !exists {
			continue
		}
		text, ok := value.(string)
		if !ok {
			warn(logf, "%s must be a string, got %T", layer.key, value)
			continue
		}
		if i > 0 {
			text = strings.ToLower(text)
		} else {
			text = strings.ToUpper(text)
		}
		if kind, ok := layer.values[text]; ok {
			return kind
		}
	}
	if value, exists := attrs["ai.operationId"]; exists {
		operation, ok := value.(string)
		if !ok {
			warn(logf, "ai.operationId must be a string, got %T", value)
		} else {
			switch {
			case strings.HasSuffix(operation, ".doGenerate"), strings.HasSuffix(operation, ".doStream"):
				return "llm"
			case operation == "ai.toolCall":
				return "tool"
			case strings.HasPrefix(operation, "ai.embed"):
				return "embedding"
			case operation == "ai.generateText", operation == "ai.streamText", operation == "ai.generateObject", operation == "ai.streamObject":
				return "agent"
			}
		}
	}
	if hasValidString(attrs, modelKeys...) && hasTokenCandidate(attrs) {
		return "llm"
	}
	return "other"
}

var modelKeys = []string{
	"gen_ai.request.model", "llm.request.model_name", "llm.model_name", "langfuse.observation.model.name", "ai.model.id", "embedding.model_name",
	"gen_ai.response.model", "llm.response.model_name", "ai.response.model",
}

func hasValidString(attrs map[string]any, keys ...string) bool {
	for _, key := range keys {
		if value, ok := attrs[key].(string); ok && value != "" {
			return true
		}
	}
	return false
}

func hasTokenCandidate(attrs map[string]any) bool {
	keys := []string{
		"gen_ai.usage.input_tokens", "gen_ai.usage.prompt_tokens", "llm.token_count.prompt", "ai.usage.promptTokens", "ai.usage.tokens",
		"gen_ai.usage.output_tokens", "gen_ai.usage.completion_tokens", "llm.token_count.completion", "ai.usage.completionTokens",
		"gen_ai.usage.cache_read.input_tokens", "gen_ai.usage.cache_read_input_tokens", "llm.token_count.prompt_details.cache_read",
	}
	for _, key := range keys {
		if value, exists := attrs[key]; exists {
			if _, ok := tokenValue(value); ok {
				return true
			}
		}
	}
	if value, ok := attrs["langfuse.observation.usage_details"]; ok {
		_, parsed := usageDetails(value, nil)
		return parsed
	}
	return false
}
