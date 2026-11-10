package coinglass

import (
	"context"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func defaultHandler(operationID string) driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			return preparedFromRequest(request), nil, nil
		},
	}
}

func preparedFromRequest(request driverapi.Request) driverapi.PreparedCall {
	cloned := driverapi.CloneRequest(request)
	return driverapi.PreparedCall{
		PathParams: cloned.PathParams,
		Query:      cloned.Query,
		Body:       cloned.Body,
	}
}
