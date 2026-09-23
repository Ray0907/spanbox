package proxy

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Ray0907/spanbox/internal/otlp"
)

type Exchange struct {
	Vendor          string
	Path            string
	ServerAddress   string
	RequestHeaders  http.Header
	RequestBody     []byte
	ResponseHeaders http.Header
	ResponseBody    []byte
	StatusCode      int
	StartNs         int64
	EndNs           int64
	TTFBMs          float64
}

type parsedResponse struct {
	id, model, serviceTier string
	finishReasons          []string
	input, output          int64
	cacheRead              int64
	cacheCreation          int64
	reasoning              int64
	outputMessages         any
	hasUsage               bool
	scanErr                error
}

var storedRequestHeaders = map[string]bool{
	"user-agent":          true,
	"anthropic-version":   true,
	"anthropic-beta":      true,
	"x-app":               true,
	"x-goog-api-client":   true,
	"x-stainless-lang":    true,
	"openai-organization": true,
	"x-spanbox-session":   true,
}

func Parse(exchange Exchange, version string) (otlp.RawSpan, error) {
	request := decodeObject(exchange.RequestBody)
	attrs := map[string]any{
		"spanbox.source":            "proxy",
		"spanbox.proxy.ttfb_ms":     exchange.TTFBMs,
		"http.response.status_code": int64(exchange.StatusCode),
		"url.path":                  exchange.Path,
	}
	provider, operation := providerOperation(exchange.Vendor)
	attrs["gen_ai.provider.name"] = provider
	attrs["gen_ai.operation.name"] = operation
	stream := requestBool(request, "stream") || strings.Contains(exchange.Path, ":streamGenerateContent")
	attrs["spanbox.proxy.stream"] = stream
	serverAddress := exchange.ServerAddress
	if serverAddress == "" {
		serverAddress = defaultServerAddress(exchange.Vendor)
	}
	attrs["server.address"] = serverAddress

	for name, values := range exchange.RequestHeaders {
		lower := strings.ToLower(name)
		if storedRequestHeaders[lower] {
			attrs["http.request.header."+lower] = strings.Join(values, ",")
		}
	}
	if session := exchange.RequestHeaders.Get("X-Spanbox-Session"); session != "" {
		attrs["spanbox.session"] = session
	}

	requestModel, sessionID, userID := requestAttributes(exchange, request, attrs)
	response := parseResponse(exchange.Vendor, stream, exchange.ResponseBody)
	if response.id == "" {
		response.id = exchange.ResponseHeaders.Get("request-id")
	}
	if response.model == "" {
		response.model = requestModel
	}
	if response.id != "" {
		attrs["gen_ai.response.id"] = response.id
	}
	if response.model != "" {
		attrs["gen_ai.response.model"] = response.model
	}
	if len(response.finishReasons) > 0 {
		items := make([]any, len(response.finishReasons))
		for i, reason := range response.finishReasons {
			items[i] = reason
		}
		attrs["gen_ai.response.finish_reasons"] = items
	}
	if response.hasUsage {
		attrs["gen_ai.usage.input_tokens"] = response.input
		attrs["gen_ai.usage.output_tokens"] = response.output
		if response.cacheRead > 0 || exchange.Vendor == "anthropic" {
			attrs["gen_ai.usage.cache_read.input_tokens"] = response.cacheRead
		}
		if response.cacheCreation > 0 || exchange.Vendor == "anthropic" {
			attrs["gen_ai.usage.cache_creation.input_tokens"] = response.cacheCreation
		}
		if response.reasoning > 0 {
			attrs["gen_ai.usage.reasoning.output_tokens"] = response.reasoning
		}
	}
	if response.outputMessages != nil {
		attrs["gen_ai.output.messages"] = jsonString(response.outputMessages)
	}
	if response.serviceTier != "" {
		attrs["service_tier"] = response.serviceTier
	}

	serviceName, serviceVersion := service(exchange.RequestHeaders.Get("User-Agent"))
	resource := map[string]any{"service.name": serviceName}
	if serviceVersion != "" {
		resource["service.version"] = serviceVersion
	}
	spanSeed := response.id
	if spanSeed == "" {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			return otlp.RawSpan{}, fmt.Errorf("random span id: %w", err)
		}
		spanSeed = hex.EncodeToString(random[:])
	}
	spanID := hash("spanbox-proxy-span"+spanSeed, 8)
	traceSeed := spanID
	if sessionID != "" {
		traceSeed = sessionID
	}
	statusCode := int32(0)
	statusMessage := ""
	if exchange.StatusCode < 200 || exchange.StatusCode >= 300 {
		statusCode = 2
		statusMessage = string(exchange.ResponseBody[:min(len(exchange.ResponseBody), 2048)])
	}
	if userID != "" {
		attrs["user.id"] = userID
	}
	if sessionID != "" {
		attrs["gen_ai.conversation.id"] = sessionID
	}
	return otlp.RawSpan{
		TraceID: hash("spanbox-proxy-trace"+traceSeed, 16), SpanID: spanID,
		Name: operation + " " + requestModel, StartNs: exchange.StartNs, EndNs: exchange.EndNs,
		StatusCode: statusCode, StatusMessage: statusMessage, Attrs: attrs,
		Events: []map[string]any{}, Links: []map[string]any{}, Resource: resource,
		Scope: map[string]any{"name": "spanbox/proxy", "version": version, "attributes": map[string]any{}},
	}, response.scanErr
}

func providerOperation(vendor string) (string, string) {
	switch vendor {
	case "anthropic":
		return "anthropic", "chat"
	case "gemini":
		return "gcp.gen_ai", "generate_content"
	case "chatgpt":
		return "chatgpt", "chat"
	default:
		return "openai", "chat"
	}
}

func defaultServerAddress(vendor string) string {
	switch vendor {
	case "anthropic":
		return "api.anthropic.com"
	case "gemini":
		return "generativelanguage.googleapis.com"
	case "chatgpt":
		return "chatgpt.com"
	default:
		return "api.openai.com"
	}
}

func requestAttributes(exchange Exchange, request map[string]any, attrs map[string]any) (model, sessionID, userID string) {
	model = text(request["model"])
	switch exchange.Vendor {
	case "anthropic":
		setJSONAttr(attrs, "gen_ai.system_instructions", request["system"])
		setJSONAttr(attrs, "gen_ai.input.messages", request["messages"])
		if metadata, ok := request["metadata"].(map[string]any); ok {
			var identity map[string]any
			_ = json.Unmarshal([]byte(text(metadata["user_id"])), &identity)
			sessionID, userID = text(identity["session_id"]), text(identity["account_uuid"])
		}
		setNumberAttr(attrs, "gen_ai.request.max_tokens", request["max_tokens"])
		setNumberAttr(attrs, "gen_ai.request.temperature", request["temperature"])
		if tier := text(request["service_tier"]); tier != "" {
			attrs["service_tier"] = tier
		}
	case "gemini":
		model = geminiModel(exchange.Path)
		setJSONAttr(attrs, "gen_ai.system_instructions", request["systemInstruction"])
		setJSONAttr(attrs, "gen_ai.input.messages", request["contents"])
		userID = exchange.RequestHeaders.Get("x-gemini-api-privileged-user-id")
		if generation, ok := request["generationConfig"].(map[string]any); ok {
			setNumberAttr(attrs, "gen_ai.request.max_tokens", generation["maxOutputTokens"])
			setNumberAttr(attrs, "gen_ai.request.temperature", generation["temperature"])
		}
	case "openai", "chatgpt":
		if strings.HasSuffix(exchange.Path, "/responses") {
			setJSONAttr(attrs, "gen_ai.system_instructions", request["instructions"])
			setJSONAttr(attrs, "gen_ai.input.messages", request["input"])
		} else {
			setJSONAttr(attrs, "gen_ai.input.messages", request["messages"])
		}
		userID = text(request["user"])
		value := request["max_tokens"]
		if value == nil {
			value = request["max_completion_tokens"]
		}
		setNumberAttr(attrs, "gen_ai.request.max_tokens", value)
		setNumberAttr(attrs, "gen_ai.request.temperature", request["temperature"])
	}
	if model != "" {
		attrs["gen_ai.request.model"] = model
	}
	if names := toolNames(exchange.Vendor, request["tools"]); len(names) > 0 {
		attrs["gen_ai.tool.definitions"] = jsonString(map[string]any{"count": len(names), "names": names})
	}
	return model, sessionID, userID
}

func parseResponse(vendor string, stream bool, body []byte) parsedResponse {
	if stream {
		return parseStream(vendor, body)
	}
	object := decodeObject(body)
	switch vendor {
	case "anthropic":
		return parseAnthropicJSON(object)
	case "gemini":
		return parseGeminiChunks([]map[string]any{object})
	default:
		return parseOpenAIJSON(object)
	}
}

func parseStream(vendor string, body []byte) parsedResponse {
	var chunks []map[string]any
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 64<<10), maxBodyBytes)
	for scanner.Scan() {
		line, ok := bytes.CutPrefix(scanner.Bytes(), []byte("data:"))
		if !ok {
			continue
		}
		data := bytes.TrimSpace(line)
		if bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if object := decodeObject(data); len(object) > 0 {
			chunks = append(chunks, object)
		}
	}
	var result parsedResponse
	switch vendor {
	case "anthropic":
		result = parseAnthropicChunks(chunks)
	case "gemini":
		result = parseGeminiChunks(chunks)
	default:
		completed := false
		for _, chunk := range chunks {
			if text(chunk["type"]) == "response.completed" {
				result = parseResponsesCompleted(chunk)
				completed = true
				break
			}
		}
		if !completed {
			result = parseOpenAIChunks(chunks)
		}
	}
	if err := scanner.Err(); err != nil {
		result.scanErr = fmt.Errorf("scan proxy stream: %w", err)
	}
	return result
}

func parseAnthropicChunks(chunks []map[string]any) parsedResponse {
	result := parsedResponse{}
	blocks := map[int]map[string]any{}
	for _, chunk := range chunks {
		switch text(chunk["type"]) {
		case "message_start":
			message, _ := chunk["message"].(map[string]any)
			result.id, result.model = text(message["id"]), text(message["model"])
			applyAnthropicUsage(&result, object(message["usage"]))
		case "content_block_start":
			index, _ := number(chunk["index"])
			block := cloneObject(object(chunk["content_block"]))
			switch text(block["type"]) {
			case "thinking":
				block = map[string]any{"type": "thinking", "chars": int64(len(text(block["thinking"])))}
			case "text":
				delete(block, "signature")
			}
			blocks[int(index)] = block
		case "content_block_delta":
			index, _ := number(chunk["index"])
			block := blocks[int(index)]
			if block == nil {
				block = map[string]any{}
				blocks[int(index)] = block
			}
			delta := object(chunk["delta"])
			switch text(delta["type"]) {
			case "text_delta":
				block["text"] = text(block["text"]) + text(delta["text"])
			case "thinking_delta":
				chars, _ := number(block["chars"])
				block["chars"] = chars + int64(len(text(delta["thinking"])))
			case "input_json_delta":
				block["_partial_json"] = text(block["_partial_json"]) + text(delta["partial_json"])
			}
		case "message_delta":
			delta := object(chunk["delta"])
			addFinish(&result.finishReasons, text(delta["stop_reason"]))
			applyAnthropicUsage(&result, object(chunk["usage"]))
		}
	}
	content := orderedBlocks(blocks)
	if len(content) > 0 {
		result.outputMessages = []any{map[string]any{"role": "assistant", "content": content}}
	}
	return result
}

func orderedBlocks(blocks map[int]map[string]any) []any {
	indexes := make([]int, 0, len(blocks))
	for index := range blocks {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	result := make([]any, 0, len(indexes))
	for _, index := range indexes {
		block := blocks[index]
		if partial := text(block["_partial_json"]); partial != "" {
			var input any
			if json.Unmarshal([]byte(partial), &input) == nil {
				block["input"] = input
			} else {
				block["input"] = partial
			}
			delete(block, "_partial_json")
		}
		result = append(result, block)
	}
	return result
}

func parseAnthropicJSON(object map[string]any) parsedResponse {
	result := parsedResponse{id: text(object["id"]), model: text(object["model"])}
	addFinish(&result.finishReasons, text(object["stop_reason"]))
	applyAnthropicUsage(&result, objectOf(object["usage"]))
	var blocks []any
	for _, item := range array(object["content"]) {
		block := cloneObject(objectOf(item))
		if text(block["type"]) == "thinking" {
			block = map[string]any{"type": "thinking", "chars": int64(len(text(block["thinking"])))}
		}
		delete(block, "signature")
		blocks = append(blocks, block)
	}
	if len(blocks) > 0 {
		result.outputMessages = []any{map[string]any{"role": "assistant", "content": blocks}}
	}
	return result
}

func applyAnthropicUsage(result *parsedResponse, usage map[string]any) {
	if len(usage) == 0 {
		return
	}
	result.hasUsage = true
	input, _ := number(usage["input_tokens"])
	result.cacheRead, _ = number(usage["cache_read_input_tokens"])
	result.cacheCreation, _ = number(usage["cache_creation_input_tokens"])
	result.input = input + result.cacheRead + result.cacheCreation
	if output, ok := number(usage["output_tokens"]); ok {
		result.output = output
	}
	if details, ok := usage["output_tokens_details"].(map[string]any); ok {
		result.reasoning, _ = number(details["thinking_tokens"])
	}
	if tier := text(usage["service_tier"]); tier != "" {
		result.serviceTier = tier
	}
}

func parseGeminiChunks(chunks []map[string]any) parsedResponse {
	result := parsedResponse{}
	var parts []any
	for _, chunk := range chunks {
		if model := text(chunk["modelVersion"]); model != "" {
			result.model = model
		}
		if id := text(chunk["responseId"]); id != "" {
			result.id = id
		}
		usage := object(chunk["usageMetadata"])
		if len(usage) > 0 {
			result.hasUsage = true
			result.input, _ = number(usage["promptTokenCount"])
			result.cacheRead, _ = number(usage["cachedContentTokenCount"])
			candidates, _ := number(usage["candidatesTokenCount"])
			result.reasoning, _ = number(usage["thoughtsTokenCount"])
			result.output = candidates + result.reasoning
			if tier := text(usage["serviceTier"]); tier != "" {
				result.serviceTier = tier
			}
		}
		for _, candidateValue := range array(chunk["candidates"]) {
			candidate := objectOf(candidateValue)
			addFinish(&result.finishReasons, text(candidate["finishReason"]))
			content := object(candidate["content"])
			for _, part := range array(content["parts"]) {
				partObject := cloneObject(objectOf(part))
				delete(partObject, "thoughtSignature")
				if len(partObject) > 0 && !(len(partObject) == 1 && text(partObject["text"]) == "") {
					parts = append(parts, partObject)
				}
			}
		}
	}
	if len(parts) > 0 {
		result.outputMessages = []any{map[string]any{"role": "assistant", "parts": mergeGeminiParts(parts)}}
	}
	return result
}

func mergeGeminiParts(parts []any) []any {
	var result []any
	for _, value := range parts {
		part := objectOf(value)
		if len(result) > 0 {
			previous := objectOf(result[len(result)-1])
			if currentText, ok := part["text"].(string); ok {
				if previousText, ok := previous["text"].(string); ok {
					previous["text"] = previousText + currentText
					continue
				}
			}
		}
		result = append(result, part)
	}
	return result
}

func parseOpenAIChunks(chunks []map[string]any) parsedResponse {
	result := parsedResponse{}
	message := map[string]any{"role": "assistant"}
	var content strings.Builder
	tools := map[int]map[string]any{}
	sawChoice := false
	for _, chunk := range chunks {
		if id := text(chunk["id"]); id != "" {
			result.id = id
		}
		if model := text(chunk["model"]); model != "" {
			result.model = model
		}
		applyOpenAIUsage(&result, object(chunk["usage"]))
		for _, choiceValue := range array(chunk["choices"]) {
			sawChoice = true
			choice := objectOf(choiceValue)
			addFinish(&result.finishReasons, text(choice["finish_reason"]))
			delta := object(choice["delta"])
			content.WriteString(text(delta["content"]))
			for _, toolValue := range array(delta["tool_calls"]) {
				tool := objectOf(toolValue)
				index, _ := number(tool["index"])
				current := tools[int(index)]
				if current == nil {
					current = map[string]any{"type": text(tool["type"]), "function": map[string]any{}}
					tools[int(index)] = current
				}
				if id := text(tool["id"]); id != "" {
					current["id"] = id
				}
				function := object(current["function"])
				incoming := object(tool["function"])
				if name := text(incoming["name"]); name != "" {
					function["name"] = name
				}
				function["arguments"] = text(function["arguments"]) + text(incoming["arguments"])
			}
		}
	}
	message["content"] = content.String()
	if len(tools) > 0 {
		indexes := make([]int, 0, len(tools))
		for index := range tools {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		values := make([]any, 0, len(indexes))
		for _, index := range indexes {
			values = append(values, tools[index])
		}
		message["tool_calls"] = values
	}
	if sawChoice {
		result.outputMessages = []any{message}
	}
	return result
}

func parseOpenAIJSON(object map[string]any) parsedResponse {
	result := parsedResponse{id: text(object["id"]), model: text(object["model"])}
	applyOpenAIUsage(&result, objectOf(object["usage"]))
	var messages []any
	for _, choiceValue := range array(object["choices"]) {
		choice := objectOf(choiceValue)
		addFinish(&result.finishReasons, text(choice["finish_reason"]))
		if message, ok := choice["message"].(map[string]any); ok {
			messages = append(messages, message)
		}
	}
	if output := object["output"]; output != nil {
		result.outputMessages = output
		addFinish(&result.finishReasons, text(object["status"]))
	} else if len(messages) > 0 {
		result.outputMessages = messages
	}
	return result
}

func parseResponsesCompleted(event map[string]any) parsedResponse {
	response := object(event["response"])
	result := parsedResponse{id: text(response["id"]), model: text(response["model"]), outputMessages: response["output"]}
	addFinish(&result.finishReasons, text(response["status"]))
	applyResponsesUsage(&result, object(response["usage"]))
	return result
}

func applyOpenAIUsage(result *parsedResponse, usage map[string]any) {
	if len(usage) == 0 {
		return
	}
	result.hasUsage = true
	result.input, _ = number(usage["prompt_tokens"])
	result.output, _ = number(usage["completion_tokens"])
	result.cacheRead, _ = number(object(usage["prompt_tokens_details"])["cached_tokens"])
	result.reasoning, _ = number(object(usage["completion_tokens_details"])["reasoning_tokens"])
	if result.input == 0 {
		applyResponsesUsage(result, usage)
	}
}

func applyResponsesUsage(result *parsedResponse, usage map[string]any) {
	if len(usage) == 0 {
		return
	}
	result.hasUsage = true
	result.input, _ = number(usage["input_tokens"])
	result.output, _ = number(usage["output_tokens"])
	result.cacheRead, _ = number(object(usage["input_tokens_details"])["cached_tokens"])
	result.reasoning, _ = number(object(usage["output_tokens_details"])["reasoning_tokens"])
}

func toolNames(vendor string, value any) []string {
	var names []string
	for _, toolValue := range array(value) {
		tool := objectOf(toolValue)
		if vendor == "gemini" {
			for _, functionValue := range array(tool["functionDeclarations"]) {
				if name := text(objectOf(functionValue)["name"]); name != "" {
					names = append(names, name)
				}
			}
			continue
		}
		name := text(tool["name"])
		if function, ok := tool["function"].(map[string]any); ok {
			name = text(function["name"])
		}
		if name != "" {
			names = append(names, name)
		}
	}
	return names
}

func geminiModel(path string) string {
	_, rest, ok := strings.Cut(path, "/models/")
	if !ok {
		return ""
	}
	model, _, _ := strings.Cut(rest, ":")
	return model
}

func service(userAgent string) (string, string) {
	token := strings.Fields(userAgent)
	if len(token) == 0 {
		return "proxy", ""
	}
	name, version, found := strings.Cut(token[0], "/")
	if !found {
		return name, ""
	}
	return name, version
}

func decodeObject(body []byte) map[string]any {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var result map[string]any
	if decoder.Decode(&result) != nil {
		return map[string]any{}
	}
	return result
}

func object(value any) map[string]any {
	result, _ := value.(map[string]any)
	if result == nil {
		return map[string]any{}
	}
	return result
}

func objectOf(value any) map[string]any { return object(value) }
func array(value any) []any             { result, _ := value.([]any); return result }
func text(value any) string             { result, _ := value.(string); return result }
func requestBool(values map[string]any, key string) bool {
	result, _ := values[key].(bool)
	return result
}

func number(value any) (int64, bool) {
	switch value := value.(type) {
	case json.Number:
		result, err := value.Int64()
		return result, err == nil
	case float64:
		return int64(value), value == float64(int64(value))
	case int64:
		return value, true
	default:
		return 0, false
	}
}

func setNumberAttr(attrs map[string]any, key string, value any) {
	if integer, ok := number(value); ok {
		attrs[key] = integer
		return
	}
	if value, ok := value.(json.Number); ok {
		if decimal, err := value.Float64(); err == nil {
			attrs[key] = decimal
		}
	}
}

func setJSONAttr(attrs map[string]any, key string, value any) {
	if value != nil {
		attrs[key] = jsonString(value)
	}
}

func jsonString(value any) string { body, _ := json.Marshal(value); return string(body) }
func cloneObject(value map[string]any) map[string]any {
	result := make(map[string]any, len(value))
	for key, item := range value {
		result[key] = item
	}
	return result
}
func addFinish(values *[]string, value string) {
	if value == "" {
		return
	}
	for _, existing := range *values {
		if existing == value {
			return
		}
	}
	*values = append(*values, value)
}
func hash(seed string, size int) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:size])
}
