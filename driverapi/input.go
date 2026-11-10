package driverapi

import (
	"encoding/json"
	"net/url"
)

type Request struct {
	PathParams  map[string]string
	Query       url.Values
	Body        json.RawMessage
	StrictInput bool
}

type OperationView struct {
	OperationID string
	SideEffect  SideEffect
}

type PreparedCall struct {
	PathParams     map[string]string
	Query          url.Values
	Body           json.RawMessage
	IdempotencyKey string
}

func CloneRequest(request Request) Request {
	return Request{
		PathParams:  cloneStrings(request.PathParams),
		Query:       cloneValues(request.Query),
		Body:        append(json.RawMessage(nil), request.Body...),
		StrictInput: request.StrictInput,
	}
}

func ClonePreparedCall(call PreparedCall) PreparedCall {
	return PreparedCall{
		PathParams:     cloneStrings(call.PathParams),
		Query:          cloneValues(call.Query),
		Body:           append(json.RawMessage(nil), call.Body...),
		IdempotencyKey: call.IdempotencyKey,
	}
}

func cloneStrings(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	result := make(map[string]string, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}

func cloneValues(input url.Values) url.Values {
	if input == nil {
		return nil
	}
	result := make(url.Values, len(input))
	for key, values := range input {
		result[key] = append([]string(nil), values...)
	}
	return result
}
