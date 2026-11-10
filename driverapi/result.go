package driverapi

import "encoding/json"

type Warning struct {
	Code  string `json:"code"`
	Field string `json:"field,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
}

type DriverResult struct {
	OperationID string          `json:"operation_id"`
	StatusCode  int             `json:"status_code"`
	Body        json.RawMessage `json:"body,omitempty"`
	Code        string          `json:"code,omitempty"`
	Message     string          `json:"message,omitempty"`
	RequestID   string          `json:"request_id,omitempty"`
	RetryAfter  int             `json:"retry_after_seconds,omitempty"`
}

type DriverError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
}

func NewError(code, message string) *DriverError {
	return &DriverError{Code: code, Message: message}
}

func (err *DriverError) Error() string {
	if err == nil {
		return ""
	}
	return err.Message
}
