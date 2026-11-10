package coinglass

import (
	"context"
	"net/url"
	"testing"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func TestEveryCompiledOperationHasExactHandler(t *testing.T) {
	driver := New()
	if got, want := len(driver.OperationIDs()), len(operationIDs); got != want {
		t.Fatalf("operation count = %d, want %d", got, want)
	}
	for _, operationID := range operationIDs {
		handler, exists := driver.Handler(operationID)
		if !exists || handler.OperationID() != operationID {
			t.Fatalf("missing exact handler for %q", operationID)
		}
	}
	if _, exists := driver.Handler("unknown-operation"); exists {
		t.Fatal("unknown operation used a fallback handler")
	}
}

func TestIntervalDialectsAreOperationSpecific(t *testing.T) {
	tests := []struct {
		operationID string
		input       string
		want        string
	}{
		{"futures-price-history", "m1", "1m"},
		{"futures-v2-taker-buy-sell-volume-history", "1m", "m1"},
	}
	for _, test := range tests {
		t.Run(test.operationID, func(t *testing.T) {
			handler, _ := New().Handler(test.operationID)
			call, warnings, driverErr := handler.Prepare(context.Background(), driverapi.Request{
				Query: url.Values{"symbol": {" btcusdt "}, "interval": {test.input}},
			}, driverapi.OperationView{OperationID: test.operationID, SideEffect: driverapi.SideEffectNone})
			if driverErr != nil {
				t.Fatal(driverErr)
			}
			if got := call.Query.Get("interval"); got != test.want {
				t.Fatalf("interval = %q, want %q", got, test.want)
			}
			if got := call.Query.Get("symbol"); got != "BTCUSDT" {
				t.Fatalf("symbol = %q", got)
			}
			if len(warnings) != 2 {
				t.Fatalf("warnings = %#v", warnings)
			}
		})
	}
}

func TestIntervalHandlerRejectsUnsupportedValue(t *testing.T) {
	handler, _ := New().Handler("futures-v2-taker-buy-sell-volume-history")
	_, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{
		Query: url.Values{"interval": {"2h"}},
	}, driverapi.OperationView{OperationID: handler.OperationID(), SideEffect: driverapi.SideEffectNone})
	if driverErr == nil || driverErr.Code != "invalid_interval" {
		t.Fatalf("error = %#v", driverErr)
	}
}
