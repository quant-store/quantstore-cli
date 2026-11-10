package coinglass

import (
	"context"
	"strings"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func futuresPriceHistoryHandler() driverapi.Handler {
	return intervalHandler("futures-price-history", standardInterval)
}

func intervalHandler(operationID string, normalize func(string) (string, bool)) driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(ctx context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			call, warnings, driverErr := defaultHandler(operationID).Prepare(ctx, request, driverapi.OperationView{OperationID: operationID, SideEffect: driverapi.SideEffectNone})
			if driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			canonicalizeQuery(call.Query, "symbol", func(value string) string {
				return strings.ToUpper(strings.TrimSpace(value))
			}, &warnings)
			canonicalizeQuery(call.Query, "exchange", strings.TrimSpace, &warnings)
			values, exists := call.Query["interval"]
			if !exists || len(values) == 0 || values[0] == "" {
				return driverapi.PreparedCall{}, nil, driverapi.NewError("missing_interval", "interval is required")
			}
			for index, value := range values {
				canonical, valid := normalize(value)
				if !valid {
					return driverapi.PreparedCall{}, nil, driverapi.NewError("invalid_interval", "interval is not supported by this Coinglass operation")
				}
				if canonical != value {
					values[index] = canonical
					warnings = append(warnings, driverapi.Warning{Code: "input_canonicalized", Field: "query.interval", From: value, To: canonical})
				}
			}
			return call, warnings, nil
		},
	}
}
