package twittermonitor

import (
	"context"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func handleBodyHandler(operationID string) driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			object, driverErr := decodeBody(request.Body)
			if driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			warnings := make([]driverapi.Warning, 0, 1)
			if driverErr := normalizeStringField(object, "handle", normalizeHandle, &warnings); driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			rawHandle, exists := object["handle"]
			if !exists || string(rawHandle) == `""` {
				return driverapi.PreparedCall{}, nil, driverapi.NewError("missing_handle", "handle is required")
			}
			body, driverErr := encodeBody(object)
			if driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			cloned := driverapi.CloneRequest(request)
			return driverapi.PreparedCall{PathParams: cloned.PathParams, Query: cloned.Query, Body: body}, warnings, nil
		},
	}
}
