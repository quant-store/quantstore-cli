package twittermonitor

import (
	"encoding/json"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func decodeBody(body json.RawMessage) (map[string]json.RawMessage, *driverapi.DriverError) {
	if len(body) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(body, &object); err != nil || object == nil {
		return nil, driverapi.NewError("invalid_request_body", "request body must be a JSON object")
	}
	return object, nil
}

func encodeBody(object map[string]json.RawMessage) (json.RawMessage, *driverapi.DriverError) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, driverapi.NewError("invalid_request_body", "request body could not be encoded")
	}
	return body, nil
}

func normalizeStringField(object map[string]json.RawMessage, field string, normalize func(string) string, warnings *[]driverapi.Warning) *driverapi.DriverError {
	raw, exists := object[field]
	if !exists {
		return nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return driverapi.NewError("invalid_"+field, field+" must be a string")
	}
	canonical := normalize(value)
	if canonical != value {
		*warnings = append(*warnings, driverapi.Warning{Code: "input_canonicalized", Field: "body." + field, From: value, To: canonical})
	}
	encoded, _ := json.Marshal(canonical)
	object[field] = encoded
	return nil
}
