package developercatalog

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	paramRE   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	literalRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
)

type matchEntry struct {
	op            *Operation
	segments      []string
	query         map[string]struct{}
	requiredQuery map[string]struct{}
}

type Matcher struct{ entries []matchEntry }

func NewMatcher(operations []Operation) (*Matcher, error) {
	compiled := append([]Operation(nil), operations...)
	m := &Matcher{entries: make([]matchEntry, 0, len(compiled))}
	for i := range compiled {
		segments, err := templateSegments(compiled[i].PathTemplate)
		if err != nil {
			return nil, invalid("operation %q path: %v", compiled[i].OperationID, err)
		}
		query := map[string]struct{}{}
		requiredQuery := map[string]struct{}{}
		for _, p := range compiled[i].Params {
			if p.In == "query" {
				query[p.Name] = struct{}{}
				if p.Required != nil && *p.Required {
					requiredQuery[p.Name] = struct{}{}
				}
			}
		}
		entry := matchEntry{op: &compiled[i], segments: segments, query: query, requiredQuery: requiredQuery}
		for _, previous := range m.entries {
			if canOverlap(previous, entry) {
				return nil, invalid("ambiguous matcher between %q and %q", previous.op.OperationID, entry.op.OperationID)
			}
		}
		m.entries = append(m.entries, entry)
	}
	return m, nil
}

// Match returns the single operation authorized by protocol, method, escaped
// path, protocol operation and allowlisted query keys. protocolOperation is
// empty for REST and exact (for example tools/call:name) for MCP/WSS.
func (m *Matcher) Match(protocol, method, escapedPath, protocolOperation string, query url.Values) (*Operation, bool, error) {
	if m == nil {
		return nil, false, ErrInvalidCatalog
	}
	actual, err := requestSegments(escapedPath)
	if err != nil {
		return nil, false, err
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	var found *Operation
	for _, entry := range m.entries {
		if entry.op.Protocol != protocol || entry.op.Method != method || entry.op.ProtocolOperation != protocolOperation || len(entry.segments) != len(actual) {
			continue
		}
		matched := true
		for i := range actual {
			if isPlaceholder(entry.segments[i]) {
				continue
			}
			if entry.segments[i] != actual[i] {
				matched = false
				break
			}
		}
		if !matched {
			continue
		}
		for key := range query {
			if _, ok := entry.query[key]; !ok {
				return nil, false, fmt.Errorf("%w: unknown query parameter %q", ErrInvalidCatalog, key)
			}
		}
		for key := range entry.requiredQuery {
			if _, ok := query[key]; !ok {
				return nil, false, fmt.Errorf("%w: missing required query parameter %q", ErrInvalidCatalog, key)
			}
		}
		if found != nil {
			return nil, false, invalid("request matched multiple operations")
		}
		found = entry.op
	}
	if found == nil {
		return nil, false, nil
	}
	result := *found
	return &result, true, nil
}

func parseTemplate(path string) (map[string]struct{}, error) {
	segments, err := templateSegments(path)
	if err != nil {
		return nil, err
	}
	params := map[string]struct{}{}
	for _, segment := range segments {
		if isPlaceholder(segment) {
			name := segment[1 : len(segment)-1]
			if _, exists := params[name]; exists {
				return nil, fmt.Errorf("duplicate path parameter %q", name)
			}
			params[name] = struct{}{}
		}
	}
	return params, nil
}

func templateSegments(path string) ([]string, error) {
	if path == "/" {
		return []string{}, nil
	}
	if len(path) == 0 || len(path) > 512 || path[0] != '/' || strings.Contains(path, "//") || strings.ContainsAny(path, "?%\\") {
		return nil, errors.New("path_template must be a canonical relative absolute path")
	}
	parts := strings.Split(path[1:], "/")
	for _, part := range parts {
		if isPlaceholder(part) {
			if !paramRE.MatchString(part[1 : len(part)-1]) {
				return nil, fmt.Errorf("invalid placeholder %q", part)
			}
			continue
		}
		if !literalRE.MatchString(part) {
			return nil, fmt.Errorf("literal segment %q must be lower-kebab", part)
		}
	}
	return parts, nil
}

func requestSegments(path string) ([]string, error) {
	if path == "/" {
		return []string{}, nil
	}
	if path == "" || path[0] != '/' || strings.Contains(path, "//") || strings.ContainsAny(path, "?\\") {
		return nil, fmt.Errorf("%w: unsafe request path", ErrInvalidCatalog)
	}
	parts := strings.Split(path[1:], "/")
	for i, raw := range parts {
		if raw == "" {
			return nil, fmt.Errorf("%w: empty path segment", ErrInvalidCatalog)
		}
		for pos := 0; pos < len(raw); pos++ {
			if raw[pos] < 0x20 || raw[pos] == 0x7f {
				return nil, fmt.Errorf("%w: control byte in path", ErrInvalidCatalog)
			}
			if raw[pos] != '%' {
				continue
			}
			if pos+2 >= len(raw) || !upperHex(raw[pos+1]) || !upperHex(raw[pos+2]) {
				return nil, fmt.Errorf("%w: non-canonical percent encoding", ErrInvalidCatalog)
			}
			value := fromHex(raw[pos+1])<<4 | fromHex(raw[pos+2])
			if value == '/' || value == '\\' || value < 0x20 || value == 0x7f || isUnreserved(value) {
				return nil, fmt.Errorf("%w: forbidden percent encoding", ErrInvalidCatalog)
			}
			pos += 2
		}
		decoded, err := url.PathUnescape(raw)
		if err != nil || !utf8.ValidString(decoded) || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\\x00") {
			return nil, fmt.Errorf("%w: unsafe decoded path segment", ErrInvalidCatalog)
		}
		parts[i] = decoded
	}
	return parts, nil
}

func canOverlap(a, b matchEntry) bool {
	if a.op.Protocol != b.op.Protocol || a.op.Method != b.op.Method || a.op.ProtocolOperation != b.op.ProtocolOperation || len(a.segments) != len(b.segments) {
		return false
	}
	for i := range a.segments {
		if !isPlaceholder(a.segments[i]) && !isPlaceholder(b.segments[i]) && a.segments[i] != b.segments[i] {
			return false
		}
	}
	return true
}

func isPlaceholder(segment string) bool {
	return len(segment) >= 3 && segment[0] == '{' && segment[len(segment)-1] == '}'
}
func upperHex(value byte) bool { return value >= '0' && value <= '9' || value >= 'A' && value <= 'F' }
func fromHex(value byte) byte {
	if value <= '9' {
		return value - '0'
	}
	return value - 'A' + 10
}
func isUnreserved(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9' || value == '-' || value == '.' || value == '_' || value == '~'
}
