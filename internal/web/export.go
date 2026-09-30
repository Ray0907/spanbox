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
	"reflect"
	"unicode/utf8"

	"github.com/Ray0907/spanbox/internal/config"
	"github.com/Ray0907/spanbox/internal/store"
)

// JSON strings cannot represent arbitrary bytes. Keep ordinary exports compatible
// and carry base64 overrides only for strings containing invalid UTF-8.
type exportedSpan struct {
	store.Span
	InvalidUTF8 map[string][]byte `json:",omitempty"`
}

func exportRecord(span store.Span) exportedSpan {
	record := exportedSpan{Span: span}
	fields := reflect.ValueOf(span)
	for i := 0; i < fields.NumField(); i++ {
		field := fields.Field(i)
		if field.Kind() == reflect.String && !utf8.ValidString(field.String()) {
			if record.InvalidUTF8 == nil {
				record.InvalidUTF8 = make(map[string][]byte)
			}
			record.InvalidUTF8[fields.Type().Field(i).Name] = []byte(field.String())
		}
	}
	return record
}

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
		if err := encoder.Encode(exportRecord(span)); err != nil {
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
	// A file may contain arbitrarily many spans. Bound each line (including
	// JSON/base64 expansion of a 32 MiB OTLP payload), not the entire stream.
	defer deps.Store.PauseRetention()()
	scanner := bufio.NewScanner(r.Body)
	scanner.Buffer(make([]byte, 64<<10), 256<<20)
	batch := make([]store.Span, 0, 500)
	batchBytes := 0
	count := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := deps.Store.InsertBatch(r.Context(), batch); err != nil {
			return err
		}
		count += len(batch)
		clear(batch)
		batch = batch[:0]
		batchBytes = 0
		return nil
	}
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		if !utf8.Valid(line) {
			http.Error(w, fmt.Sprintf("line %d: invalid UTF-8; use InvalidUTF8 base64 overrides", lineNum), http.StatusBadRequest)
			return
		}
		decoder := json.NewDecoder(bytes.NewReader(line))
		var record exportedSpan
		if err := decoder.Decode(&record); err != nil {
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
		fields := reflect.ValueOf(&record.Span).Elem()
		for name, raw := range record.InvalidUTF8 {
			field := fields.FieldByName(name)
			if !field.IsValid() || field.Kind() != reflect.String {
				http.Error(w, fmt.Sprintf("line %d: invalid InvalidUTF8 field %q", lineNum, name), http.StatusBadRequest)
				return
			}
			field.SetString(string(raw))
		}
		span := record.Span
		if span.TraceID == "" || span.SpanID == "" || span.StartNs == 0 || span.EndNs == 0 {
			http.Error(w, fmt.Sprintf("line %d: missing span identity or timestamps", lineNum), http.StatusBadRequest)
			return
		}
		batch = append(batch, span)
		batchBytes += len(line)
		if len(batch) == 500 || batchBytes >= config.MaxBodyBytes {
			if err := flush(); err != nil {
				http.Error(w, fmt.Sprintf("import failed: %v", err), http.StatusInternalServerError)
				return
			}
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			http.Error(w, fmt.Sprintf("line %d: exceeds 256 MiB limit", lineNum+1), http.StatusBadRequest)
		} else {
			http.Error(w, fmt.Sprintf("line %d: invalid import body: %v", lineNum+1, err), http.StatusBadRequest)
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
