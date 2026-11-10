package twittermonitor

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func tweetsSearchHandler() driverapi.Handler {
	return tweetsSearchHandlerWithID("tweets.search")
}

func tweetsSearchHandlerWithID(operationID string) driverapi.Handler {
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			object, driverErr := decodeBody(request.Body)
			if driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			warnings := make([]driverapi.Warning, 0, 3)
			if driverErr := normalizeStringField(object, "handle", normalizeHandle, &warnings); driverErr != nil {
				return driverapi.PreparedCall{}, nil, driverErr
			}
			if raw, exists := object["handle_norms"]; exists {
				var handles []string
				if err := json.Unmarshal(raw, &handles); err != nil {
					return driverapi.PreparedCall{}, nil, driverapi.NewError("invalid_handle_norms", "handle_norms must be an array of strings")
				}
				canonical := normalizeHandles(handles)
				if !equalStrings(handles, canonical) {
					warnings = append(warnings, driverapi.Warning{Code: "input_canonicalized", Field: "body.handle_norms", From: strings.Join(handles, ","), To: strings.Join(canonical, ",")})
				}
				object["handle_norms"], _ = json.Marshal(canonical)
			}
			if raw, exists := object["cursor"]; exists && string(raw) != "null" {
				var cursor string
				if err := json.Unmarshal(raw, &cursor); err != nil {
					return driverapi.PreparedCall{}, nil, driverapi.NewError("invalid_cursor", "cursor must be an opaque signed continuation string")
				}
				canonical := strings.TrimSpace(cursor)
				if canonical == "" && cursor != "" {
					return driverapi.PreparedCall{}, nil, driverapi.NewError("invalid_cursor", "cursor must be empty or an opaque signed continuation")
				}
				if canonical != cursor {
					warnings = append(warnings, driverapi.Warning{Code: "input_canonicalized", Field: "body.cursor", From: cursor, To: canonical})
					object["cursor"], _ = json.Marshal(canonical)
				}
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

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
