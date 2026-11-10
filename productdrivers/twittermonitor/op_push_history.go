package twittermonitor

import (
	"context"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func pushHistoryHandler() driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     "push-history.list",
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			cloned := driverapi.CloneRequest(request)
			call := driverapi.PreparedCall{PathParams: cloned.PathParams, Query: cloned.Query, Body: cloned.Body}
			return call, nil, nil
		},
	}
}
