package productexec

import (
	"context"
	"net/url"
	"reflect"
	"testing"

	"github.com/quant-store/quantstore-cli/driverapi"
	"github.com/quant-store/quantstore-cli/productdrivers/builtin"
)

func TestBuiltInDriversRegisterExplicitly(t *testing.T) {
	registry, err := NewRegistry(builtin.Drivers()...)
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	want := []string{"coinglass", "smart-money-monitor-pro", "twitter-monitor"}
	if !reflect.DeepEqual(registry.Slugs(), want) {
		t.Fatalf("Slugs() = %v, want %v", registry.Slugs(), want)
	}
}

func TestPrepareBindsProductAndOperationInCore(t *testing.T) {
	registry, err := NewRegistry(builtin.Drivers()...)
	if err != nil {
		t.Fatal(err)
	}
	bound, driverErr := registry.Prepare(context.Background(), "coinglass", "futures-price-history", driverapi.Request{
		Query: url.Values{"symbol": {" btcusdt "}, "interval": {"m1"}},
	}, driverapi.OperationView{OperationID: "futures-price-history", SideEffect: driverapi.SideEffectNone})
	if driverErr != nil {
		t.Fatalf("Prepare() error = %v", driverErr)
	}
	if bound.ProductSlug != "coinglass" || bound.OperationID != "futures-price-history" {
		t.Fatalf("bound identity = %q/%q", bound.ProductSlug, bound.OperationID)
	}
	if got := bound.Call.Query.Get("symbol"); got != "BTCUSDT" {
		t.Fatalf("symbol = %q", got)
	}
	if got := bound.Call.Query.Get("interval"); got != "1m" {
		t.Fatalf("interval = %q", got)
	}
}

func TestPrepareRejectsCatalogMismatchBeforeHandler(t *testing.T) {
	registry, err := NewRegistry(builtin.Drivers()...)
	if err != nil {
		t.Fatal(err)
	}
	_, driverErr := registry.Prepare(context.Background(), "twitter-monitor", "tweets.search", driverapi.Request{}, driverapi.OperationView{
		OperationID: "handles.resolve",
		SideEffect:  driverapi.SideEffectNone,
	})
	if driverErr == nil || driverErr.Code != "catalog_operation_mismatch" {
		t.Fatalf("error = %#v", driverErr)
	}
}

func TestStrictInputRejectsCorrection(t *testing.T) {
	registry, err := NewRegistry(builtin.Drivers()...)
	if err != nil {
		t.Fatal(err)
	}
	_, driverErr := registry.Prepare(context.Background(), "coinglass", "futures-price-history", driverapi.Request{
		Query:       url.Values{"interval": {"m1"}},
		StrictInput: true,
	}, driverapi.OperationView{OperationID: "futures-price-history", SideEffect: driverapi.SideEffectNone})
	if driverErr == nil || driverErr.Code != "input_requires_canonical_form" {
		t.Fatalf("error = %#v", driverErr)
	}
}

func TestPreparedCallCannotChooseDispatchTarget(t *testing.T) {
	typeOf := reflect.TypeOf(driverapi.PreparedCall{})
	for _, forbidden := range []string{"ProductSlug", "OperationID", "Endpoint", "URL"} {
		if _, exists := typeOf.FieldByName(forbidden); exists {
			t.Fatalf("PreparedCall unexpectedly exposes %s", forbidden)
		}
	}
}
