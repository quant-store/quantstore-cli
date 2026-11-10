package productexec

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/quant-store/quantstore-cli/driverapi"
	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

const maxDeclarativeParameterBytes = 64 << 10

var ErrDeclarativeRejected = errors.New("declarative operation was rejected")

// PrepareDeclarative binds caller-supplied path and query values to one exact
// catalog operation. It deliberately supports only the small, side-effect-free
// REST subset whose behavior can be described completely by the catalog.
func PrepareDeclarative(
	catalog *developercatalog.Catalog,
	operationID string,
	pathValues, queryValues map[string][]string,
) (*developercatalog.Operation, string, url.Values, error) {
	if catalog == nil || catalog.Matcher() == nil {
		return nil, "", nil, declarativeError("catalog is unavailable")
	}

	operation := findOperation(catalog.Operations, operationID)
	if operation == nil {
		return nil, "", nil, declarativeError("operation is unavailable")
	}
	if err := validateDeclarativeOperation(operation); err != nil {
		return nil, "", nil, err
	}
	return bindExactRead(catalog, operation, pathValues, queryValues)
}

// PrepareDriverRead runs an existing compiled product handler and then binds
// its value-only result back through the exact catalog matcher. It keeps
// current driver-required GET/HEAD operations usable while newly classified
// declarative reads can arrive through R2 without a new executable.
func PrepareDriverRead(
	ctx context.Context,
	registry *Registry,
	catalog *developercatalog.Catalog,
	operationID string,
	pathValues, queryValues map[string][]string,
) (*developercatalog.Operation, string, url.Values, []driverapi.Warning, error) {
	if registry == nil || catalog == nil || catalog.Matcher() == nil {
		return nil, "", nil, nil, declarativeError("compiled product driver is unavailable")
	}
	operation := findOperation(catalog.Operations, operationID)
	if operation == nil {
		return nil, "", nil, nil, declarativeError("operation is unavailable")
	}
	if operation.CLIExecution != developercatalog.CLIExecutionDriverRequired {
		return nil, "", nil, nil, declarativeError("operation does not require a compiled driver")
	}
	if operation.Protocol != "rest" || (operation.Method != http.MethodGet && operation.Method != http.MethodHead) || operation.SideEffect != "none" || operation.Idempotent == nil || !*operation.Idempotent || len(operation.BodySchema) != 0 || operation.MCP != nil || operation.WSS != nil || operation.ProtocolOperation != "" {
		return nil, "", nil, nil, declarativeError("compiled operation is not a bodyless REST read")
	}
	path := make(map[string]string, len(pathValues))
	for name, values := range pathValues {
		if len(values) != 1 {
			return nil, "", nil, nil, declarativeError("each path parameter must be provided exactly once")
		}
		path[name] = values[0]
	}
	bound, driverErr := registry.Prepare(ctx, catalog.ProductSlug, operationID, driverapi.Request{
		PathParams: path,
		Query:      cloneValues(queryValues),
	}, driverapi.OperationView{
		OperationID: operationID,
		SideEffect:  driverapi.SideEffect(operation.SideEffect),
	})
	if driverErr != nil {
		return nil, "", nil, nil, driverErr
	}
	if len(bound.Call.Body) != 0 || bound.Call.IdempotencyKey != "" {
		return nil, "", nil, nil, declarativeError("compiled read prepared unsupported request behavior")
	}
	preparedPath := make(map[string][]string, len(bound.Call.PathParams))
	for name, value := range bound.Call.PathParams {
		preparedPath[name] = []string{value}
	}
	matched, escapedPath, query, err := bindExactRead(catalog, operation, preparedPath, bound.Call.Query)
	if err != nil {
		return nil, "", nil, nil, err
	}
	return matched, escapedPath, query, append([]driverapi.Warning(nil), bound.Warnings...), nil
}

func bindExactRead(
	catalog *developercatalog.Catalog,
	operation *developercatalog.Operation,
	pathValues, queryValues map[string][]string,
) (*developercatalog.Operation, string, url.Values, error) {

	pathParameters, queryParameters := parameterIndexes(operation.Params)
	if err := rejectUnknownOrRepeated(pathValues, pathParameters); err != nil {
		return nil, "", nil, err
	}
	if err := rejectUnknownOrRepeated(queryValues, queryParameters); err != nil {
		return nil, "", nil, err
	}
	if err := requireDeclaredValues(pathValues, pathParameters); err != nil {
		return nil, "", nil, err
	}
	if err := requireDeclaredValues(queryValues, queryParameters); err != nil {
		return nil, "", nil, err
	}

	escapedPath := operation.PathTemplate
	for name, parameter := range pathParameters {
		value := pathValues[name][0]
		if err := validateScalarValue(value, parameter.Schema); err != nil {
			return nil, "", nil, declarativeError("path parameter is invalid")
		}
		escapedPath = strings.ReplaceAll(escapedPath, "{"+name+"}", url.PathEscape(value))
	}

	query := make(url.Values, len(queryValues))
	for name, values := range queryValues {
		if err := validateScalarValue(values[0], queryParameters[name].Schema); err != nil {
			return nil, "", nil, declarativeError("query parameter is invalid")
		}
		query[name] = []string{values[0]}
	}

	matched, ok, err := catalog.Matcher().Match("rest", operation.Method, escapedPath, "", query)
	if err != nil || !ok || matched == nil || matched.OperationID != operation.OperationID {
		return nil, "", nil, declarativeError("bound request does not match the exact operation")
	}
	if matched.CLIExecution != operation.CLIExecution {
		return nil, "", nil, declarativeError("matched operation execution class changed")
	}
	return matched, escapedPath, query, nil
}

func cloneValues(values map[string][]string) url.Values {
	cloned := make(url.Values, len(values))
	for name, items := range values {
		cloned[name] = append([]string(nil), items...)
	}
	return cloned
}

func findOperation(operations []developercatalog.Operation, operationID string) *developercatalog.Operation {
	for index := range operations {
		if operations[index].OperationID == operationID {
			return &operations[index]
		}
	}
	return nil
}

func validateDeclarativeOperation(operation *developercatalog.Operation) error {
	if operation == nil || operation.CLIExecution != "declarative" {
		return declarativeError("operation is not declarative")
	}
	if len(operation.RequiredCLICapabilities) != 1 || operation.RequiredCLICapabilities[0] != "rest.read.v1" {
		return declarativeError("operation requires an unsupported CLI capability")
	}
	if operation.Protocol != "rest" || (operation.Method != http.MethodGet && operation.Method != http.MethodHead) {
		return declarativeError("operation transport is not declarative REST")
	}
	if operation.SideEffect != "none" || operation.Idempotent == nil || !*operation.Idempotent {
		return declarativeError("operation is not side-effect-free and idempotent")
	}
	if len(operation.BodySchema) != 0 || operation.MCP != nil || operation.WSS != nil || operation.ProtocolOperation != "" {
		return declarativeError("operation requires unsupported request behavior")
	}
	return nil
}

func parameterIndexes(parameters []developercatalog.Parameter) (map[string]developercatalog.Parameter, map[string]developercatalog.Parameter) {
	path := make(map[string]developercatalog.Parameter)
	query := make(map[string]developercatalog.Parameter)
	for _, parameter := range parameters {
		switch parameter.In {
		case "path":
			path[parameter.Name] = parameter
		case "query":
			query[parameter.Name] = parameter
		}
	}
	return path, query
}

func rejectUnknownOrRepeated(values map[string][]string, declared map[string]developercatalog.Parameter) error {
	for name, provided := range values {
		if _, ok := declared[name]; !ok {
			return declarativeError("request contains an unknown parameter")
		}
		if len(provided) != 1 {
			return declarativeError("each parameter must be provided exactly once")
		}
	}
	return nil
}

func requireDeclaredValues(values map[string][]string, declared map[string]developercatalog.Parameter) error {
	for name, parameter := range declared {
		if parameter.Required != nil && *parameter.Required {
			if provided, ok := values[name]; !ok || len(provided) != 1 {
				return declarativeError("a required parameter is missing")
			}
		}
	}
	return nil
}

type scalarParameterSchema struct {
	Type             string            `json:"type"`
	Enum             []json.RawMessage `json:"enum,omitempty"`
	MinLength        *int              `json:"minLength,omitempty"`
	MaxLength        *int              `json:"maxLength,omitempty"`
	Minimum          *json.Number      `json:"minimum,omitempty"`
	Maximum          *json.Number      `json:"maximum,omitempty"`
	ExclusiveMinimum *json.Number      `json:"exclusiveMinimum,omitempty"`
	ExclusiveMaximum *json.Number      `json:"exclusiveMaximum,omitempty"`
	Pattern          string            `json:"pattern,omitempty"`
}

func validateScalarValue(value string, rawSchema json.RawMessage) error {
	if len(value) > maxDeclarativeParameterBytes || !utf8.ValidString(value) || strings.ContainsRune(value, '\x00') {
		return errors.New("invalid scalar value")
	}
	decoder := json.NewDecoder(bytes.NewReader(rawSchema))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var schema scalarParameterSchema
	if err := decoder.Decode(&schema); err != nil {
		return errors.New("unsupported scalar schema")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unsupported scalar schema")
	}

	var comparable any
	switch schema.Type {
	case "string":
		length := utf8.RuneCountInString(value)
		if schema.MinLength != nil && length < *schema.MinLength || schema.MaxLength != nil && length > *schema.MaxLength {
			return errors.New("string length is outside schema bounds")
		}
		if schema.Pattern != "" {
			pattern, err := regexp.Compile(schema.Pattern)
			if err != nil || !pattern.MatchString(value) {
				return errors.New("string does not match schema pattern")
			}
		}
		comparable = value
	case "integer":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return errors.New("value is not an integer")
		}
		if !withinNumericBounds(float64(parsed), schema) {
			return errors.New("integer is outside schema bounds")
		}
		comparable = parsed
	case "number":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsInf(parsed, 0) || math.IsNaN(parsed) {
			return errors.New("value is not a finite number")
		}
		if !withinNumericBounds(parsed, schema) {
			return errors.New("number is outside schema bounds")
		}
		comparable = parsed
	case "boolean":
		if value != "true" && value != "false" {
			return errors.New("value is not a boolean")
		}
		comparable = value == "true"
	default:
		return errors.New("unsupported scalar schema type")
	}

	if len(schema.Enum) > 0 && !enumContains(schema.Type, comparable, schema.Enum) {
		return errors.New("value is outside schema enum")
	}
	return nil
}

func withinNumericBounds(value float64, schema scalarParameterSchema) bool {
	if bound, ok := schemaNumber(schema.Minimum); ok && value < bound {
		return false
	}
	if bound, ok := schemaNumber(schema.Maximum); ok && value > bound {
		return false
	}
	if bound, ok := schemaNumber(schema.ExclusiveMinimum); ok && value <= bound {
		return false
	}
	if bound, ok := schemaNumber(schema.ExclusiveMaximum); ok && value >= bound {
		return false
	}
	return true
}

func schemaNumber(value *json.Number) (float64, bool) {
	if value == nil {
		return 0, false
	}
	parsed, err := strconv.ParseFloat(value.String(), 64)
	return parsed, err == nil && !math.IsInf(parsed, 0) && !math.IsNaN(parsed)
}

func enumContains(schemaType string, value any, candidates []json.RawMessage) bool {
	for _, candidate := range candidates {
		decoder := json.NewDecoder(bytes.NewReader(candidate))
		decoder.UseNumber()
		var decoded any
		if decoder.Decode(&decoded) != nil {
			continue
		}
		switch schemaType {
		case "string", "boolean":
			if decoded == value {
				return true
			}
		case "integer":
			number, ok := decoded.(json.Number)
			parsed, err := strconv.ParseInt(number.String(), 10, 64)
			if ok && err == nil && parsed == value.(int64) {
				return true
			}
		case "number":
			number, ok := decoded.(json.Number)
			parsed, err := strconv.ParseFloat(number.String(), 64)
			if ok && err == nil && parsed == value.(float64) {
				return true
			}
		}
	}
	return false
}

func declarativeError(message string) error {
	return fmt.Errorf("%w: %s", ErrDeclarativeRejected, message)
}
