package output

import (
	"bytes"
	"strings"
	"testing"

	"github.com/mattn/go-runewidth"
)

func TestWriteJSONPreservesWireFieldNames(t *testing.T) {
	var result bytes.Buffer
	if err := Write(&result, "json", []byte(`{"available_cents":12,"username":"alice"}`)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.String(), `"available_cents": 12`) || !strings.HasSuffix(result.String(), "\n") {
		t.Fatalf("JSON output = %q", result.String())
	}
}

func TestWriteTableIncludesItemsAndEnvelopeMetadata(t *testing.T) {
	var result bytes.Buffer
	raw := []byte(`{"items":[{"order_id":"ord_1","status":"paid"}],"next_cursor":"cursor_2","partial":false}`)
	if err := Write(&result, "table", raw); err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"ORDER_ID", "STATUS", "ord_1", "paid", "next_cursor", "cursor_2", "partial"} {
		if !strings.Contains(result.String(), expected) {
			t.Fatalf("table output missing %q: %s", expected, result.String())
		}
	}
}

func TestWriteJSONLExpandsTopLevelArrayOnly(t *testing.T) {
	var result bytes.Buffer
	if err := Write(&result, "jsonl", []byte(`[{"id":1},{"id":2}]`)); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(result.String()), "\n")
	if len(lines) != 2 || lines[0] != `{"id":1}` || lines[1] != `{"id":2}` {
		t.Fatalf("JSONL output = %q", result.String())
	}
}

func TestWriteRejectsInvalidJSONInsteadOfFallingBackToRaw(t *testing.T) {
	var result bytes.Buffer
	if err := Write(&result, "json", []byte(`not-json`)); err == nil {
		t.Fatal("expected invalid JSON error")
	}
	if result.Len() != 0 {
		t.Fatalf("invalid response reached stdout: %q", result.String())
	}
}

func TestTableEscapesTerminalControlCharacters(t *testing.T) {
	var result bytes.Buffer
	if err := Write(&result, "table", []byte(`{"name\u001b]52;field\u0007":"safe\u001b]52;clipboard\u0007"}`)); err != nil {
		t.Fatal(err)
	}
	if strings.ContainsRune(result.String(), '\x1b') || strings.ContainsRune(result.String(), '\x07') {
		t.Fatalf("terminal control reached table output: %q", result.String())
	}
}

func TestTableAlignsCJKDisplayWidth(t *testing.T) {
	var result bytes.Buffer
	raw := []byte(`{"items":[{"name":"中文","status":"ok"},{"name":"a","status":"ok"}]}`)
	if err := Write(&result, "table", raw); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(result.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("lines = %#v", lines)
	}
	var statusOffsets []int
	for _, line := range lines {
		index := strings.LastIndex(line, "ok")
		if index >= 0 {
			statusOffsets = append(statusOffsets, runewidth.StringWidth(line[:index]))
		}
	}
	if len(statusOffsets) != 2 || statusOffsets[0] != statusOffsets[1] {
		t.Fatalf("CJK rows are not display-aligned: %v; output=%q", statusOffsets, result.String())
	}
}
