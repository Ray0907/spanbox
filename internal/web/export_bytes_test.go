package web

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Ray0907/spanbox/internal/store"
)

func TestExportImportInvalidUTF8(t *testing.T) {
	h, db := newTestHandler(t, "")
	other, target := newTestHandler(t, "")
	span := store.Span{TraceID: strings.Repeat("a", 32), SpanID: strings.Repeat("b", 16), StartNs: 1, EndNs: 2, Kind: "other"}
	fields := reflect.ValueOf(&span).Elem()
	for i := 0; i < fields.NumField(); i++ {
		name := fields.Type().Field(i).Name
		if fields.Field(i).Kind() == reflect.String && name != "TraceID" && name != "SpanID" && name != "ParentSpanID" && name != "Kind" {
			fields.Field(i).SetString("世界\x00\xff\xfe" + name)
		}
	}
	if err := db.InsertBatch(context.Background(), []store.Span{span}); err != nil {
		t.Fatal(err)
	}
	exported := exportRequest(h, "GET", "/export", "", nil)
	imported := exportRequest(other, "POST", "/import", "application/x-ndjson", exported.Body.Bytes())
	if imported.Code != 200 {
		t.Fatalf("import: %d %s", imported.Code, imported.Body.String())
	}
	got, err := target.GetSpan(context.Background(), span.TraceID, span.SpanID)
	if err != nil || !reflect.DeepEqual(got, span) {
		t.Fatalf("invalid UTF-8 bytes changed: err=%v", err)
	}
}
