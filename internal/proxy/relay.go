package proxy

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"
)

const (
	maxBodyBytes       = 32 << 20
	defaultIdleTimeout = 120 * time.Second
)

var defaultUpstreams = map[string]string{
	"anthropic": "https://api.anthropic.com",
	"openai":    "https://api.openai.com",
	"gemini":    "https://generativelanguage.googleapis.com",
}

type Config struct {
	AnthropicUpstream string
	OpenAIUpstream    string
	GeminiUpstream    string
	Logf              func(string, ...any)
	IdleTimeout       time.Duration
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
		"gemini":    cfg.GeminiUpstream,
	}
	h := &Handler{endpoints: make(map[string]endpoint, len(values)), logf: cfg.Logf, idleTimeout: cfg.IdleTimeout}
	for vendor, value := range values {
		if value == "" {
			value = defaultUpstreams[vendor]
		}
		base, err := url.Parse(value)
		if err != nil || base.Scheme == "" || base.Host == "" {
			return nil, fmt.Errorf("%s upstream: invalid URL %q", vendor, value)
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.ResponseHeaderTimeout = 120 * time.Second
		transport.IdleConnTimeout = 90 * time.Second
		transport.MaxIdleConnsPerHost = 16
		transport.DisableCompression = true
		h.endpoints[vendor] = endpoint{base: base, client: &http.Client{Transport: transport}}
	}
	return h, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	vendor, rest, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/proxy/"), "/")
	endpoint, exists := h.endpoints[vendor]
	if !ok || !exists || rest == "" {
		writeError(w, http.StatusNotFound, "unknown proxy route")
		return
	}
	rest = "/" + rest
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "payload too large")
		} else {
			writeError(w, http.StatusBadRequest, "read request body")
		}
		return
	}

	target := *endpoint.base
	target.Path = strings.TrimRight(endpoint.base.Path, "/") + rest
	target.RawPath = ""
	target.RawQuery = r.URL.RawQuery
	upstream, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusBadGateway, "create upstream request")
		return
	}
	copyHeaders(upstream.Header, r.Header)
	upstream.Host = target.Host

	if !isInference(vendor, r.Method, rest) {
		count := h.nonInference.Add(1)
		h.logf("metric: non-inference proxy requests=%d vendor=%s path=%s", count, vendor, rest)
	}
	response, err := endpoint.client.Do(upstream)
	if err != nil {
		status := http.StatusBadGateway
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() {
			status = http.StatusGatewayTimeout
		}
		writeError(w, status, http.StatusText(status))
		return
	}
	defer response.Body.Close()
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})
	h.stream(w, response)
}

type readResult struct {
	n   int
	err error
}

func (h *Handler) stream(w http.ResponseWriter, response *http.Response) {
	buffer := make([]byte, 32<<10)
	headersSent := false
	for {
		result := make(chan readResult, 1)
		go func() {
			n, err := response.Body.Read(buffer)
			result <- readResult{n: n, err: err}
		}()
		var read readResult
		select {
		case read = <-result:
		case <-time.After(h.idleTimeout):
			_ = response.Body.Close()
			<-result
			if !headersSent {
				writeError(w, http.StatusBadGateway, "upstream idle timeout")
			}
			return
		}
		if !headersSent {
			copyHeaders(w.Header(), response.Header)
			w.Header().Del("Content-Length")
			w.WriteHeader(response.StatusCode)
			headersSent = true
		}
		if read.n > 0 {
			if _, err := w.Write(buffer[:read.n]); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if read.err != nil {
			if read.err != io.EOF {
				h.logf("proxy response read: %v", read.err)
			}
			return
		}
	}
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
