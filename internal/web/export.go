package web

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

func (deps Deps) exportSpans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	filter, _, err := traceFilter(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="spanbox-export.ndjson"`)
	encoder := json.NewEncoder(w)
	written := false
	if err := deps.Store.ExportSpans(r.Context(), filter, func(span store.Span) error {
		if err := encoder.Encode(span); err != nil {
			return err
		}
		written = true
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return nil
	}); err != nil {
		if !written {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		} else {
			deps.Logf("export failed after partial write: %v", err)
		}
	}
}

func (deps Deps) importSpans(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/x-ndjson" {
		http.Error(w, "unsupported media type", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, config.MaxBodyBytes)
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 64<<10), 10<<20)
	batch := make([]store.Span, 0, 500)
	count := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := deps.Store.InsertBatch(r.Context(), batch); err != nil {
			return err
		}
		count += len(batch)
		batch = batch[:0]
		return nil
	}
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		var span store.Span
		if err := decoder.Decode(&span); err != nil {
			http.Error(w, fmt.Sprintf("line %d: %v", lineNum, err), http.StatusBadRequest)
			return
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			if err == nil {
				err = errors.New("extra JSON value")
			}
			http.Error(w, fmt.Sprintf("line %d: %v", lineNum, err), http.StatusBadRequest)
			return
		}
		if span.TraceID == "" || span.SpanID == "" || span.StartNs == 0 || span.EndNs == 0 {
			http.Error(w, "missing span identity or timestamps", http.StatusBadRequest)
			return
		}
		batch = append(batch, span)
		if len(batch) == 500 {
			if err := flush(); err != nil {
				http.Error(w, fmt.Sprintf("import failed: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		} else if errors.Is(err, bufio.ErrTooLong) {
			http.Error(w, "line too long", http.StatusBadRequest)
		} else {
			http.Error(w, "invalid import body", http.StatusBadRequest)
		}
		return
	}
	if err := flush(); err != nil {
		http.Error(w, fmt.Sprintf("import failed: %v", err), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, "{\"imported\":%d}\n", count)
}
