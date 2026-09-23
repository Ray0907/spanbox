package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Ray0907/spanbox/internal/normalize"
	"github.com/Ray0907/spanbox/internal/pricing"
	"github.com/Ray0907/spanbox/internal/store"
	"github.com/klauspost/compress/zstd"
)

const (
	maxBodyBytes       = 32 << 20
	defaultIdleTimeout = 120 * time.Second
)

var defaultUpstreams = map[string]string{
	"anthropic": "https://api.anthropic.com",
	"openai":    "https://api.openai.com",
	"chatgpt":   "https://chatgpt.com/backend-api",
	"gemini":    "https://generativelanguage.googleapis.com",
}

type Config struct {
	AnthropicUpstream     string
	OpenAIUpstream        string
	OpenAICompatUpstreams string
	ChatGPTUpstream       string
	GeminiUpstream        string
	Logf                  func(string, ...any)
	IdleTimeout           time.Duration
	Store                 *store.Store
	Pricing               *pricing.Table
	Version               string
}

type endpoint struct {
	base   *url.URL
	client *http.Client
}

type Handler struct {
	endpoints    map[string]endpoint
	logf         func(string, ...any)
	idleTimeout  time.Duration
	nonInference atomic.Uint64
	store        *store.Store
	pricing      *pricing.Table
	version      string
	wg           sync.WaitGroup
	captureSlots chan struct{}
}

func New(cfg Config) (*Handler, error) {
	if cfg.Logf == nil {
		cfg.Logf = log.Printf
	}
	if cfg.IdleTimeout <= 0 {
		cfg.IdleTimeout = defaultIdleTimeout
	}
	values := map[string]string{
		"anthropic": cfg.AnthropicUpstream,
		"openai":    cfg.OpenAIUpstream,
		"chatgpt":   cfg.ChatGPTUpstream,
		"gemini":    cfg.GeminiUpstream,
	}
	compat, err := parseOpenAICompatUpstreams(cfg.OpenAICompatUpstreams)
	if err != nil {
		return nil, err
	}
	for name, value := range compat {
		values["openai-compat/"+name] = value
	}
	h := &Handler{endpoints: make(map[string]endpoint, len(values)), logf: cfg.Logf, idleTimeout: cfg.IdleTimeout, store: cfg.Store, pricing: cfg.Pricing, version: cfg.Version, captureSlots: make(chan struct{}, 8)}
	for route, value := range values {
		if value == "" {
			value = defaultUpstreams[route]
		}
		endpoint, err := newEndpoint(value)
		if err != nil {
			return nil, fmt.Errorf("%s upstream: %w", route, err)
		}
		h.endpoints[route] = endpoint
	}
	return h, nil
}

func newEndpoint(value string) (endpoint, error) {
	base, err := url.Parse(value)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return endpoint{}, fmt.Errorf("invalid URL %q", value)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 120 * time.Second
	transport.IdleConnTimeout = 90 * time.Second
	transport.MaxIdleConnsPerHost = 16
	transport.DisableCompression = true
	return endpoint{base: base, client: &http.Client{Transport: transport}}, nil
}

var openAICompatName = regexp.MustCompile(`^[a-z0-9-]+$`)

func parseOpenAICompatUpstreams(value string) (map[string]string, error) {
	result := map[string]string{}
	if value == "" {
		return result, nil
	}
	for _, item := range strings.Split(value, ",") {
		name, upstream, ok := strings.Cut(item, "=")
		if !ok || !openAICompatName.MatchString(name) {
			return nil, fmt.Errorf("OPENAI_COMPAT_UPSTREAMS: invalid entry %q", item)
		}
		result[name] = upstream
	}
	return result, nil
}

func proxyRoute(path string) (vendor, route, rest string, ok bool) {
	vendor, rest, ok = strings.Cut(strings.TrimPrefix(path, "/proxy/"), "/")
	route = vendor
	if !ok || vendor != "openai-compat" {
		return
	}
	name, remaining, found := strings.Cut(rest, "/")
	if !found {
		return vendor, route, rest, false
	}
	return "openai", vendor + "/" + name, remaining, true
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	vendor, route, rest, ok := proxyRoute(r.URL.Path)
	endpoint, exists := h.endpoints[route]
	if !ok || !exists || rest == "" {
		writeError(w, http.StatusNotFound, "unknown proxy route")
		return
	}
	body, ok := readProxyBody(w, r)
	if !ok {
		return
	}
	h.relay(w, r, vendor, route, "/"+rest, endpoint, body, start)
}

func readProxyBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err == nil {
		return body, true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
	} else {
		writeError(w, http.StatusBadRequest, "read request body")
	}
	return nil, false
}

func newUpstreamRequest(r *http.Request, endpoint endpoint, rest string, body []byte) (*http.Request, error) {
	target := *endpoint.base
	target.Path = strings.TrimRight(endpoint.base.Path, "/") + rest
	target.RawPath = ""
	target.RawQuery = r.URL.RawQuery
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	headers := r.Header.Clone()
	headers.Del("X-Spanbox-Token")
	headers.Del("X-Spanbox-Session")
	copyHeaders(upstream.Header, headers)
	upstream.Host = target.Host
	return upstream, nil
}

func (h *Handler) relay(w http.ResponseWriter, r *http.Request, vendor, route, rest string, endpoint endpoint, body []byte, start time.Time) {
	upstream, err := newUpstreamRequest(r, endpoint, rest, body)
	if err != nil {
		writeError(w, http.StatusBadGateway, "create upstream request")
		return
	}
	inference, captureBody := h.classifyRequest(vendor, route, rest, r, body)
	response, err := endpoint.client.Do(upstream)
	if err != nil {
		h.writeUpstreamError(w, r, vendor, rest, endpoint, captureBody, start, inference, err)
		return
	}
	defer response.Body.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	streamed := h.stream(w, response, start, inference)
	if inference {
		h.captureAsync(Exchange{Vendor: vendor, Path: rest, ServerAddress: endpoint.base.Hostname(), RequestHeaders: captureHeaders(r.Header), RequestBody: captureBody, ResponseHeaders: response.Header, ResponseBody: streamed.body, StatusCode: streamed.status, StartNs: start.UnixNano(), EndNs: streamed.endNs, TTFBMs: streamed.ttfbMs})
	}
}

func (h *Handler) classifyRequest(vendor, route, path string, r *http.Request, body []byte) (bool, []byte) {
	inferencePath := path
	if strings.HasPrefix(route, "openai-compat/") && !strings.HasPrefix(path, "/v1/") {
		inferencePath = "/v1" + path
	}
	if !isInference(vendor, r.Method, inferencePath) {
		count := h.nonInference.Add(1)
		h.logf("metric: non-inference proxy requests=%d vendor=%s path=%s", count, vendor, path)
		return false, body
	}
	decoded, err := decodeRequestBody(r.Header, body)
	if err != nil {
		h.logf("decode proxy request: %v", err)
		return true, body
	}
	return true, decoded
}

func (h *Handler) writeUpstreamError(w http.ResponseWriter, r *http.Request, vendor, path string, endpoint endpoint, body []byte, start time.Time, inference bool, err error) {
	status := http.StatusBadGateway
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		status = http.StatusGatewayTimeout
	}
	responseBody := errorBody(http.StatusText(status))
	writeJSON(w, status, responseBody)
	if inference {
		h.captureAsync(Exchange{Vendor: vendor, Path: path, ServerAddress: endpoint.base.Hostname(), RequestHeaders: captureHeaders(r.Header), RequestBody: body, ResponseBody: responseBody, StatusCode: status, StartNs: start.UnixNano(), EndNs: time.Now().UnixNano()})
	}
}

func decodeRequestBody(headers http.Header, body []byte) ([]byte, error) {
	if headers.Get("Content-Encoding") != "zstd" {
		return body, nil
	}
	decoder, err := zstd.NewReader(bytes.NewReader(body), zstd.WithDecoderMaxMemory(maxBodyBytes))
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	decoded, err := io.ReadAll(io.LimitReader(decoder, maxBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(decoded) > maxBodyBytes {
		return nil, fmt.Errorf("decoded body exceeds %d bytes", maxBodyBytes)
	}
	return decoded, nil
}

type readResult struct {
	n   int
	err error
}

type streamResult struct {
	body   []byte
	status int
	endNs  int64
	ttfbMs float64
}

func (h *Handler) stream(w http.ResponseWriter, response *http.Response, start time.Time, inference bool) streamResult {
	buffer := make([]byte, 32<<10)
	result := streamResult{status: response.StatusCode, endNs: start.UnixNano()}
	headersSent := false
	captureLimitLogged := false
	for {
		readCh := make(chan readResult, 1)
		go func() {
			n, err := response.Body.Read(buffer)
			readCh <- readResult{n: n, err: err}
		}()
		var read readResult
		select {
		case read = <-readCh:
		case <-time.After(h.idleTimeout):
			_ = response.Body.Close()
			<-readCh
			if !headersSent {
				result.status = http.StatusBadGateway
				result.body = errorBody("upstream idle timeout")
				writeJSON(w, result.status, result.body)
			}
			result.endNs = time.Now().UnixNano()
			return result
		}
		if !headersSent {
			result.ttfbMs = float64(time.Since(start).Microseconds()) / 1000
			copyHeaders(w.Header(), response.Header)
			w.Header().Del("Content-Length")
			w.WriteHeader(response.StatusCode)
			headersSent = true
		}
		if read.n > 0 {
			if inference && len(result.body) < maxBodyBytes {
				result.body = append(result.body, buffer[:min(read.n, maxBodyBytes-len(result.body))]...)
			}
			if inference && len(result.body) == maxBodyBytes && !captureLimitLogged {
				h.logf("proxy response capture truncated at %d bytes", maxBodyBytes)
				captureLimitLogged = true
			}
			if _, err := w.Write(buffer[:read.n]); err != nil {
				result.endNs = time.Now().UnixNano()
				return result
			}
			result.endNs = time.Now().UnixNano()
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if read.err != nil {
			if read.err != io.EOF {
				h.logf("proxy response read: %v", read.err)
			}
			return result
		}
	}
}

func (h *Handler) captureAsync(exchange Exchange) {
	h.captureSlots <- struct{}{}
	h.wg.Add(1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				h.logf("proxy capture panic: %v", recovered)
			}
			<-h.captureSlots
			h.wg.Done()
		}()
		h.capture(exchange)
	}()
}

func (h *Handler) Wait() { h.wg.Wait() }

func (h *Handler) capture(exchange Exchange) {
	if h.store == nil {
		return
	}
	raw, err := Parse(exchange, h.version)
	if err != nil {
		h.logf("parse proxy span: %v", err)
		return
	}
	span, err := normalize.Span(raw, h.logf)
	if err != nil {
		h.logf("normalize proxy span: %v", err)
		return
	}
	if h.pricing != nil && span.CostUSD == nil && span.InputTokens != nil && span.OutputTokens != nil {
		var cacheCreation *int64
		if value, ok := raw.Attrs["gen_ai.usage.cache_creation.input_tokens"].(int64); ok {
			cacheCreation = &value
		}
		if cost, ok := h.pricing.Cost(span.Provider, span.RequestModel, span.ResponseModel, span.InputTokens, span.OutputTokens, span.CacheReadTokens, cacheCreation); ok {
			span.CostUSD, span.CostSource = &cost, "pricing"
		}
	}
	if err := h.store.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		h.logf("insert proxy span: %v", err)
	}
}

func captureHeaders(headers http.Header) http.Header {
	result := make(http.Header)
	for name, values := range headers {
		lower := strings.ToLower(name)
		if storedRequestHeaders[lower] || lower == "x-gemini-api-privileged-user-id" {
			result[name] = append([]string(nil), values...)
		}
	}
	return result
}

func isInference(vendor, method, path string) bool {
	if method != http.MethodPost {
		return false
	}
	switch vendor {
	case "anthropic":
		return path == "/v1/messages"
	case "openai":
		return path == "/v1/chat/completions" || path == "/v1/responses"
	case "chatgpt":
		return path == "/codex/responses"
	case "gemini":
		return strings.HasPrefix(path, "/v1beta/models/") && (strings.HasSuffix(path, ":streamGenerateContent") || strings.HasSuffix(path, ":generateContent"))
	default:
		return false
	}
}

func copyHeaders(dst, src http.Header) {
	connectionHeaders := make(map[string]bool)
	for _, value := range src.Values("Connection") {
		for _, name := range strings.Split(value, ",") {
			connectionHeaders[http.CanonicalHeaderKey(strings.TrimSpace(name))] = true
		}
	}
	for name, values := range src {
		canonical := http.CanonicalHeaderKey(name)
		if hopByHop(canonical) || connectionHeaders[canonical] {
			continue
		}
		for _, value := range values {
			dst.Add(name, value)
		}
	}
}

func hopByHop(name string) bool {
	return name == "Connection" || name == "Keep-Alive" || strings.HasPrefix(name, "Proxy-") || name == "Te" || name == "Trailer" || name == "Transfer-Encoding" || name == "Upgrade" || name == "Host" || name == "Accept-Encoding"
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, errorBody(message))
}

func errorBody(message string) []byte {
	body, _ := json.Marshal(map[string]string{"error": message})
	return append(body, '\n')
}

func writeJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
