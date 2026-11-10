package productexec

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/quant-store/quantstore-cli/driverapi"
)

const maxPreparedBodyBytes = 1 << 20

var (
	slugPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	operationPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
	fieldPattern     = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)
	warningPattern   = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)
)

type Registry struct {
	drivers map[string]driverapi.Driver
}

type BoundCall struct {
	ProductSlug string
	OperationID string
	Call        driverapi.PreparedCall
	Warnings    []driverapi.Warning
}

func NewRegistry(drivers ...driverapi.Driver) (*Registry, error) {
	registry := &Registry{drivers: make(map[string]driverapi.Driver, len(drivers))}
	for _, driver := range drivers {
		if driver == nil {
			return nil, fmt.Errorf("nil product driver")
		}
		slug := driver.Slug()
		if !slugPattern.MatchString(slug) {
			return nil, fmt.Errorf("invalid product driver slug %q", slug)
		}
		if _, exists := registry.drivers[slug]; exists {
			return nil, fmt.Errorf("duplicate product driver %q", slug)
		}
		if err := validateDriver(driver); err != nil {
			return nil, fmt.Errorf("product driver %q: %w", slug, err)
		}
		registry.drivers[slug] = driver
	}
	return registry, nil
}

func (registry *Registry) Slugs() []string {
	result := make([]string, 0, len(registry.drivers))
	for slug := range registry.drivers {
		result = append(result, slug)
	}
	sort.Strings(result)
	return result
}

func (registry *Registry) Prepare(ctx context.Context, productSlug, operationID string, request driverapi.Request, view driverapi.OperationView) (BoundCall, *driverapi.DriverError) {
	driver, exists := registry.drivers[productSlug]
	if !exists {
		return BoundCall{}, driverapi.NewError("cli_driver_unavailable", "product driver is not available in this CLI version")
	}
	handler, exists := driver.Handler(operationID)
	if !exists {
		return BoundCall{}, driverapi.NewError("cli_operation_unavailable", "operation is not registered by the product driver")
	}
	if view.OperationID != operationID {
		return BoundCall{}, driverapi.NewError("catalog_operation_mismatch", "catalog operation does not match the dispatched handler")
	}
	if view.SideEffect != handler.SideEffect() {
		return BoundCall{}, driverapi.NewError("catalog_side_effect_mismatch", "catalog side effect does not match the product driver")
	}
	call, warnings, driverErr := handler.Prepare(ctx, driverapi.CloneRequest(request), view)
	if driverErr != nil {
		return BoundCall{}, driverErr
	}
	if request.StrictInput && len(warnings) > 0 {
		return BoundCall{}, driverapi.NewError("input_requires_canonical_form", "input requires canonicalization while strict input mode is enabled")
	}
	if driverErr := validatePrepared(call, warnings); driverErr != nil {
		return BoundCall{}, driverErr
	}
	return BoundCall{
		ProductSlug: productSlug,
		OperationID: operationID,
		Call:        driverapi.ClonePreparedCall(call),
		Warnings:    append([]driverapi.Warning(nil), warnings...),
	}, nil
}

func validateDriver(driver driverapi.Driver) error {
	seen := make(map[string]struct{})
	for _, operationID := range driver.OperationIDs() {
		if !operationPattern.MatchString(operationID) {
			return fmt.Errorf("invalid operation id %q", operationID)
		}
		if _, exists := seen[operationID]; exists {
			return fmt.Errorf("duplicate operation id %q", operationID)
		}
		seen[operationID] = struct{}{}
		handler, exists := driver.Handler(operationID)
		if !exists || handler == nil {
			return fmt.Errorf("operation %q has no handler", operationID)
		}
		if handler.OperationID() != operationID {
			return fmt.Errorf("operation %q handler reports %q", operationID, handler.OperationID())
		}
		if handler.SideEffect() != driverapi.SideEffectNone && handler.SideEffect() != driverapi.SideEffectMutation {
			return fmt.Errorf("operation %q has invalid side effect", operationID)
		}
	}
	if len(seen) == 0 {
		return fmt.Errorf("driver has no registered operations")
	}
	return nil
}

func validatePrepared(call driverapi.PreparedCall, warnings []driverapi.Warning) *driverapi.DriverError {
	if len(call.Body) > maxPreparedBodyBytes {
		return driverapi.NewError("input_too_large", "prepared request body exceeds 1 MiB")
	}
	if len(call.Body) > 0 && !json.Valid(call.Body) {
		return driverapi.NewError("invalid_request_body", "prepared request body is not valid JSON")
	}
	for key, value := range call.PathParams {
		if !fieldPattern.MatchString(key) || strings.ContainsAny(value, "\x00\r\n") {
			return driverapi.NewError("invalid_path_parameter", "prepared path parameter is invalid")
		}
	}
	for key, values := range call.Query {
		if !fieldPattern.MatchString(key) {
			return driverapi.NewError("invalid_query_parameter", "prepared query parameter name is invalid")
		}
		for _, value := range values {
			if len(value) > 64*1024 || strings.ContainsAny(value, "\x00\r\n") {
				return driverapi.NewError("invalid_query_parameter", "prepared query parameter value is invalid")
			}
		}
	}
	for _, warning := range warnings {
		if !warningPattern.MatchString(warning.Code) || (warning.Field != "" && !fieldPattern.MatchString(warning.Field)) {
			return driverapi.NewError("invalid_driver_warning", "product driver emitted an invalid warning")
		}
		if len(warning.From) > 4096 || len(warning.To) > 4096 {
			return driverapi.NewError("invalid_driver_warning", "product driver warning is too large")
		}
	}
	return nil
}
