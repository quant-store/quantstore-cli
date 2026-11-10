package wirehash

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding"
	"encoding/base32"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

var base32Lower = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func Canonicalize(value any) ([]byte, error) {
	if err := validateGoValue(reflect.ValueOf(value), make(map[goVisit]struct{})); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return CanonicalizeJSON(raw)
}

func CanonicalizeJSON(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, errors.New("wirehash: empty JSON")
	}
	if !utf8.Valid(raw) {
		return nil, errors.New("wirehash: JSON is not valid UTF-8")
	}
	if err := validateSurrogates(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	value, err := decodeValue(decoder)
	if err != nil {
		return nil, err
	}
	if token, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("wirehash: trailing JSON token %v", token)
		}
		return nil, fmt.Errorf("wirehash: trailing JSON: %w", err)
	}
	var out bytes.Buffer
	if err := appendCanonical(&out, value); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func Digest(domain string, value any) ([32]byte, error) {
	canonical, err := Canonicalize(value)
	if err != nil {
		return [32]byte{}, err
	}
	return digestCanonical(domain, canonical)
}

func DigestJSON(domain string, raw []byte) ([32]byte, error) {
	canonical, err := CanonicalizeJSON(raw)
	if err != nil {
		return [32]byte{}, err
	}
	return digestCanonical(domain, canonical)
}

func MAC(key []byte, domain string, value any) ([32]byte, error) {
	canonical, err := Canonicalize(value)
	if err != nil {
		return [32]byte{}, err
	}
	framed, err := frame(domain, canonical)
	if err != nil {
		return [32]byte{}, err
	}
	h := hmac.New(sha256.New, key)
	_, _ = h.Write(framed)
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func SHA256Wire(digest [32]byte) string {
	return "sha256:" + hex.EncodeToString(digest[:])
}

func Base32Lower(digest [32]byte) string {
	return base32Lower.EncodeToString(digest[:])
}

func DerivedID(prefix string, digest [32]byte) string {
	return prefix + Base32Lower(digest)
}

func ParseSHA256Wire(value string) ([32]byte, error) {
	var out [32]byte
	if len(value) != 71 || !strings.HasPrefix(value, "sha256:") {
		return out, errors.New("wirehash: invalid sha256 wire value")
	}
	for i := 7; i < len(value); i++ {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f')) {
			return out, errors.New("wirehash: invalid sha256 wire value")
		}
	}
	raw, err := hex.DecodeString(value[7:])
	if err != nil || len(raw) != sha256.Size {
		return out, errors.New("wirehash: invalid sha256 wire value")
	}
	copy(out[:], raw)
	return out, nil
}

func digestCanonical(domain string, canonical []byte) ([32]byte, error) {
	framed, err := frame(domain, canonical)
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(framed), nil
}

func frame(domain string, canonical []byte) ([]byte, error) {
	domainBytes := []byte(domain)
	if len(domainBytes) == 0 || len(domainBytes) > math.MaxUint16 {
		return nil, errors.New("wirehash: domain must contain 1..65535 UTF-8 bytes")
	}
	if !utf8.Valid(domainBytes) {
		return nil, errors.New("wirehash: domain is not valid UTF-8")
	}
	if uint64(len(canonical)) > math.MaxUint64-10-uint64(len(domainBytes)) {
		return nil, errors.New("wirehash: canonical JSON length overflow")
	}
	out := make([]byte, 2+len(domainBytes)+8+len(canonical))
	binary.BigEndian.PutUint16(out[:2], uint16(len(domainBytes)))
	copy(out[2:], domainBytes)
	offset := 2 + len(domainBytes)
	binary.BigEndian.PutUint64(out[offset:offset+8], uint64(len(canonical)))
	copy(out[offset+8:], canonical)
	return out, nil
}

func decodeValue(decoder *json.Decoder) (any, error) {
	token, err := decoder.Token()
	if err != nil {
		return nil, fmt.Errorf("wirehash: decode JSON: %w", err)
	}
	delim, ok := token.(json.Delim)
	if !ok {
		switch value := token.(type) {
		case nil, bool, string, json.Number:
			return value, nil
		default:
			return nil, fmt.Errorf("wirehash: unsupported JSON token %T", token)
		}
	}
	switch delim {
	case '{':
		object := make(map[string]any)
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, fmt.Errorf("wirehash: decode object key: %w", err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("wirehash: object key is not a string")
			}
			if _, exists := object[key]; exists {
				return nil, fmt.Errorf("wirehash: duplicate object key %q", key)
			}
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return nil, errors.New("wirehash: unterminated object")
		}
		return object, nil
	case '[':
		array := make([]any, 0)
		for decoder.More() {
			value, err := decodeValue(decoder)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return nil, errors.New("wirehash: unterminated array")
		}
		return array, nil
	default:
		return nil, fmt.Errorf("wirehash: unexpected delimiter %q", delim)
	}
}

func appendCanonical(out *bytes.Buffer, value any) error {
	switch value := value.(type) {
	case nil:
		out.WriteString("null")
	case bool:
		if value {
			out.WriteString("true")
		} else {
			out.WriteString("false")
		}
	case string:
		return appendJSONString(out, value)
	case json.Number:
		number, err := canonicalNumber(value.String())
		if err != nil {
			return err
		}
		out.WriteString(number)
	case []any:
		out.WriteByte('[')
		for i := range value {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendCanonical(out, value[i]); err != nil {
				return err
			}
		}
		out.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool { return utf16Less(keys[i], keys[j]) })
		out.WriteByte('{')
		for i, key := range keys {
			if i > 0 {
				out.WriteByte(',')
			}
			if err := appendJSONString(out, key); err != nil {
				return err
			}
			out.WriteByte(':')
			if err := appendCanonical(out, value[key]); err != nil {
				return err
			}
		}
		out.WriteByte('}')
	default:
		return fmt.Errorf("wirehash: unsupported decoded value %T", value)
	}
	return nil
}

func appendJSONString(out *bytes.Buffer, value string) error {
	if err := validateUnicodeString(value); err != nil {
		return err
	}
	out.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"', '\\':
			out.WriteByte('\\')
			out.WriteRune(r)
		case '\b':
			out.WriteString("\\b")
		case '\t':
			out.WriteString("\\t")
		case '\n':
			out.WriteString("\\n")
		case '\f':
			out.WriteString("\\f")
		case '\r':
			out.WriteString("\\r")
		default:
			if r < 0x20 {
				fmt.Fprintf(out, "\\u%04x", r)
			} else {
				out.WriteRune(r)
			}
		}
	}
	out.WriteByte('"')
	return nil
}

func isUnicodeNoncharacter(r rune) bool {
	return r >= 0xfdd0 && r <= 0xfdef || r&0xffff == 0xfffe || r&0xffff == 0xffff
}

func validateUnicodeString(value string) error {
	if !utf8.ValidString(value) {
		return errors.New("wirehash: string is not valid UTF-8")
	}
	for _, r := range value {
		if isUnicodeNoncharacter(r) {
			return errors.New("wirehash: string contains a Unicode noncharacter")
		}
	}
	return nil
}

var (
	jsonMarshalerType = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshalerType = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

type goVisit struct {
	typ reflect.Type
	ptr uintptr
}

func validateGoValue(value reflect.Value, active map[goVisit]struct{}) error {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		return validateGoValue(value.Elem(), active)
	}
	if implementsCustomMarshaler(value.Type()) {
		return fmt.Errorf("wirehash: custom JSON or text marshaler %s is not supported; pass its JSON bytes to CanonicalizeJSON", value.Type())
	}
	var visit goVisit
	switch value.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice:
		if value.IsNil() {
			return nil
		}
		visit = goVisit{typ: value.Type(), ptr: value.Pointer()}
		if _, exists := active[visit]; exists {
			return nil
		}
		active[visit] = struct{}{}
		defer delete(active, visit)
	}
	switch value.Kind() {
	case reflect.String:
		if !utf8.ValidString(value.String()) {
			return errors.New("wirehash: Go string is not valid UTF-8")
		}
	case reflect.Pointer:
		return validateGoValue(value.Elem(), active)
	case reflect.Struct:
		typeOfValue := value.Type()
		for i := 0; i < value.NumField(); i++ {
			field := typeOfValue.Field(i)
			fieldType := field.Type
			if fieldType.Kind() == reflect.Pointer {
				fieldType = fieldType.Elem()
			}
			if field.Tag.Get("json") == "-" ||
				(field.PkgPath != "" && (!field.Anonymous || fieldType.Kind() != reflect.Struct)) {
				continue
			}
			if err := validateGoValue(value.Field(i), active); err != nil {
				return err
			}
		}
	case reflect.Map:
		iterator := value.MapRange()
		for iterator.Next() {
			if err := validateGoValue(iterator.Key(), active); err != nil {
				return err
			}
			if err := validateGoValue(iterator.Value(), active); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		for i := 0; i < value.Len(); i++ {
			if err := validateGoValue(value.Index(i), active); err != nil {
				return err
			}
		}
	}
	return nil
}

func implementsCustomMarshaler(valueType reflect.Type) bool {
	if valueType.Implements(jsonMarshalerType) || valueType.Implements(textMarshalerType) {
		return true
	}
	return valueType.Kind() != reflect.Pointer &&
		(reflect.PointerTo(valueType).Implements(jsonMarshalerType) ||
			reflect.PointerTo(valueType).Implements(textMarshalerType))
}

func utf16Less(a, b string) bool {
	aa := utf16.Encode([]rune(a))
	bb := utf16.Encode([]rune(b))
	for i := 0; i < len(aa) && i < len(bb); i++ {
		if aa[i] != bb[i] {
			return aa[i] < bb[i]
		}
	}
	return len(aa) < len(bb)
}

func canonicalNumber(text string) (string, error) {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return "", fmt.Errorf("wirehash: invalid RFC 8785 number %q", text)
	}
	if value == 0 {
		return "0", nil
	}
	formatted := strconv.FormatFloat(value, 'g', -1, 64)
	e := strings.IndexByte(formatted, 'e')
	if e < 0 {
		return formatted, nil
	}
	mantissa := formatted[:e]
	exponent, err := strconv.Atoi(formatted[e+1:])
	if err != nil {
		return "", fmt.Errorf("wirehash: invalid exponent %q", formatted)
	}
	sign := ""
	if strings.HasPrefix(mantissa, "-") {
		sign = "-"
		mantissa = mantissa[1:]
	}
	dot := strings.IndexByte(mantissa, '.')
	decimalPosition := len(mantissa)
	if dot >= 0 {
		decimalPosition = dot
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	newPosition := decimalPosition + exponent
	if newPosition > 0 && newPosition <= 21 {
		if newPosition >= len(mantissa) {
			return sign + mantissa + strings.Repeat("0", newPosition-len(mantissa)), nil
		}
		return sign + mantissa[:newPosition] + "." + mantissa[newPosition:], nil
	}
	if newPosition <= 0 && newPosition >= -5 {
		return sign + "0." + strings.Repeat("0", -newPosition) + mantissa, nil
	}
	normalizedExponent := strconv.Itoa(exponent)
	if exponent >= 0 {
		normalizedExponent = "+" + normalizedExponent
	}
	return formatted[:e+1] + normalizedExponent, nil
}

func validateSurrogates(raw []byte) error {
	inString := false
	for i := 0; i < len(raw); i++ {
		if !inString {
			if raw[i] == '"' {
				inString = true
			}
			continue
		}
		switch raw[i] {
		case '"':
			inString = false
		case '\\':
			if i+1 >= len(raw) {
				return errors.New("wirehash: truncated string escape")
			}
			if raw[i+1] != 'u' {
				i++
				continue
			}
			if i+6 > len(raw) {
				return errors.New("wirehash: truncated unicode escape")
			}
			first, err := strconv.ParseUint(string(raw[i+2:i+6]), 16, 16)
			if err != nil {
				return errors.New("wirehash: invalid unicode escape")
			}
			i += 5
			if first >= 0xd800 && first <= 0xdbff {
				if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
					return errors.New("wirehash: unpaired high surrogate")
				}
				second, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
				if err != nil || second < 0xdc00 || second > 0xdfff {
					return errors.New("wirehash: unpaired high surrogate")
				}
				i += 6
			} else if first >= 0xdc00 && first <= 0xdfff {
				return errors.New("wirehash: unpaired low surrogate")
			}
		}
	}
	if inString {
		return errors.New("wirehash: unterminated string")
	}
	return nil
}
