package output

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/mattn/go-runewidth"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/quant-store/quantstore-cli/core/internal/redact"
)

var validModes = map[string]struct{}{
	"auto": {}, "table": {}, "json": {}, "jsonl": {}, "yaml": {}, "raw": {},
}

type PayloadError struct{ Message string }

func (e *PayloadError) Error() string { return e.Message }

func ValidateMode(mode string) error {
	if _, ok := validModes[mode]; !ok {
		return fmt.Errorf("invalid output mode %q; expected auto, table, json, jsonl, yaml or raw", mode)
	}
	return nil
}

func Write(writer io.Writer, mode string, raw []byte) error {
	if err := ValidateMode(mode); err != nil {
		return err
	}
	if mode == "auto" {
		mode = "json"
		if file, ok := writer.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
			mode = "table"
		}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	if mode == "raw" {
		_, err := writer.Write(ensureNewline(raw))
		return err
	}
	value, err := decodeJSON(raw)
	if err != nil {
		return &PayloadError{Message: "server returned invalid JSON"}
	}
	switch mode {
	case "json":
		encoder := json.NewEncoder(writer)
		encoder.SetIndent("", "  ")
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	case "jsonl":
		encoder := json.NewEncoder(writer)
		encoder.SetEscapeHTML(false)
		if items, ok := value.([]any); ok {
			for _, item := range items {
				if err := encoder.Encode(item); err != nil {
					return err
				}
			}
			return nil
		}
		return encoder.Encode(value)
	case "yaml":
		encoder := yaml.NewEncoder(writer)
		encoder.SetIndent(2)
		defer encoder.Close()
		return encoder.Encode(value)
	case "table":
		return writeTable(writer, value)
	default:
		return fmt.Errorf("unsupported output mode %q", mode)
	}
}

func WriteValue(writer io.Writer, mode string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return Write(writer, mode, raw)
}

func decodeJSON(raw []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("trailing JSON value")
	}
	return value, nil
}

func writeTable(writer io.Writer, value any) error {
	if object, ok := value.(map[string]any); ok {
		if items, exists := object["items"].([]any); exists {
			if err := writeRows(writer, items); err != nil {
				return err
			}
			meta := make(map[string]any, len(object)-1)
			for key, val := range object {
				if key != "items" {
					meta[key] = val
				}
			}
			if len(meta) > 0 {
				return writeObject(writer, meta, "")
			}
			return nil
		}
		return writeObject(writer, object, "")
	}
	if items, ok := value.([]any); ok {
		return writeRows(writer, items)
	}
	_, err := fmt.Fprintln(writer, scalar(value))
	return err
}

func writeObject(writer io.Writer, object map[string]any, prefix string) error {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([][]string, 0, len(keys))
	for _, key := range keys {
		rows = append(rows, []string{redact.String(prefix + key), scalar(object[key])})
	}
	return writeAligned(writer, rows)
}

func writeRows(writer io.Writer, items []any) error {
	if len(items) == 0 {
		_, err := fmt.Fprintln(writer, "No rows.")
		return err
	}
	columnsSet := make(map[string]struct{})
	rows := make([]map[string]any, 0, len(items))
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			_, err := fmt.Fprintln(writer, scalar(item))
			if err != nil {
				return err
			}
			continue
		}
		rows = append(rows, object)
		for key := range object {
			columnsSet[key] = struct{}{}
		}
	}
	if len(rows) == 0 {
		return nil
	}
	columns := make([]string, 0, len(columnsSet))
	for column := range columnsSet {
		columns = append(columns, column)
	}
	sort.Strings(columns)
	alignedRows := make([][]string, 0, len(rows)+1)
	header := make([]string, 0, len(columns))
	for _, column := range columns {
		header = append(header, strings.ToUpper(redact.String(column)))
	}
	alignedRows = append(alignedRows, header)
	for _, row := range rows {
		cells := make([]string, 0, len(columns))
		for _, column := range columns {
			cells = append(cells, scalar(row[column]))
		}
		alignedRows = append(alignedRows, cells)
	}
	return writeAligned(writer, alignedRows)
}

func writeAligned(writer io.Writer, rows [][]string) error {
	widths := make([]int, 0)
	for _, row := range rows {
		for column, cell := range row {
			if column >= len(widths) {
				widths = append(widths, 0)
			}
			if width := runewidth.StringWidth(cell); width > widths[column] {
				widths[column] = width
			}
		}
	}
	for _, row := range rows {
		for column, cell := range row {
			if _, err := io.WriteString(writer, cell); err != nil {
				return err
			}
			if column < len(row)-1 {
				padding := widths[column] - runewidth.StringWidth(cell) + 2
				if _, err := io.WriteString(writer, strings.Repeat(" ", padding)); err != nil {
					return err
				}
			}
		}
		if _, err := io.WriteString(writer, "\n"); err != nil {
			return err
		}
	}
	return nil
}

func scalar(value any) string {
	if value == nil {
		return "-"
	}
	switch typed := value.(type) {
	case string:
		return redact.String(typed)
	case json.Number:
		return typed.String()
	case bool:
		if typed {
			return "true"
		}
		return "false"
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return "?"
		}
		return string(raw)
	}
}

func ensureNewline(value []byte) []byte {
	if len(value) > 0 && value[len(value)-1] == '\n' {
		return value
	}
	result := append([]byte(nil), value...)
	return append(result, '\n')
}
