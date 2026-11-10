package driverapi

import "context"

type SideEffect string

const (
	SideEffectNone     SideEffect = "none"
	SideEffectMutation SideEffect = "mutation"
)

type Driver interface {
	Slug() string
	OperationIDs() []string
	Handler(operationID string) (Handler, bool)
}

type Handler interface {
	OperationID() string
	SideEffect() SideEffect
	Prepare(context.Context, Request, OperationView) (PreparedCall, []Warning, *DriverError)
}

type PrepareFunc func(context.Context, Request, OperationView) (PreparedCall, []Warning, *DriverError)

type StaticHandler struct {
	ID          string
	Effect      SideEffect
	PrepareCall PrepareFunc
}

func (handler StaticHandler) OperationID() string {
	return handler.ID
}

func (handler StaticHandler) SideEffect() SideEffect {
	return handler.Effect
}

func (handler StaticHandler) Prepare(ctx context.Context, request Request, view OperationView) (PreparedCall, []Warning, *DriverError) {
	if handler.PrepareCall == nil {
		return PreparedCall{}, nil, NewError("driver_handler_unavailable", "operation handler is unavailable")
	}
	return handler.PrepareCall(ctx, request, view)
}
