package twittermonitor

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func TestNormalizeHandleMatchesGatewayRules(t *testing.T) {
	if got := normalizeHandle("  @OpenAI  "); got != "openai" {
		t.Fatalf("normalizeHandle() = %q", got)
	}
	want := []string{"openai", "quantstore"}
	got := normalizeHandles([]string{" @OpenAI ", "", "openai", " @QuantStore"})
	if !equalStrings(got, want) {
		t.Fatalf("normalizeHandles() = %v, want %v", got, want)
	}
}

func TestTweetsSearchNormalizesBodyAndCursor(t *testing.T) {
	handler, _ := New().Handler("tweets.search")
	call, warnings, driverErr := handler.Prepare(context.Background(), driverapi.Request{
		Body: json.RawMessage(`{"handle":" @OpenAI ","handle_norms":[" @OpenAI ","openai"," @QuantStore "],"cursor":" 12 "}`),
	}, driverapi.OperationView{OperationID: "tweets.search", SideEffect: driverapi.SideEffectNone})
	if driverErr != nil {
		t.Fatal(driverErr)
	}
	var body struct {
		Handle      string   `json:"handle"`
		HandleNorms []string `json:"handle_norms"`
		Cursor      string   `json:"cursor"`
	}
	if err := json.Unmarshal(call.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body.Handle != "openai" || body.Cursor != "12" || !equalStrings(body.HandleNorms, []string{"openai", "quantstore"}) {
		t.Fatalf("body = %#v", body)
	}
	if len(warnings) != 3 {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestTweetsSearchRejectsInvalidCursor(t *testing.T) {
	handler, _ := New().Handler("tweets.search")
	_, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{Body: json.RawMessage(`{"cursor":42}`)}, driverapi.OperationView{})
	if driverErr == nil || driverErr.Code != "invalid_cursor" {
		t.Fatalf("error = %#v", driverErr)
	}
}

func TestTweetsSearchAcceptsOpaqueSignedCursor(t *testing.T) {
	handler, _ := New().Handler("tweets.search")
	call, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{Body: json.RawMessage(`{"cursor":"twqcur_exact"}`)}, driverapi.OperationView{})
	if driverErr != nil || string(call.Body) != `{"cursor":"twqcur_exact"}` {
		t.Fatalf("call=%s error=%#v", call.Body, driverErr)
	}
}

func TestOperationIDsMatchTwitterDeveloperCatalog(t *testing.T) {
	want := []string{
		"handles.profile", "handles.resolve", "mcp.get-handle-profile", "mcp.get-points",
		"mcp.get-push-history", "mcp.get-push-overview", "mcp.get-rules", "mcp.get-seats",
		"mcp.get-stats", "mcp.get-tweets", "mcp.resolve-handle", "points.get",
		"push-history.list", "push-overview.get", "rules.list", "seats.list", "stats.get",
		"stream.events", "tweets.search",
	}
	if got := New().OperationIDs(); !equalStrings(got, want) {
		t.Fatalf("operation IDs=%v want=%v", got, want)
	}
}

func TestHandleOperationsRequireHandle(t *testing.T) {
	for _, operationID := range []string{"handles.resolve", "handles.profile"} {
		handler, _ := New().Handler(operationID)
		_, _, driverErr := handler.Prepare(context.Background(), driverapi.Request{Body: json.RawMessage(`{"handle":" @ "}`)}, driverapi.OperationView{})
		if driverErr == nil || driverErr.Code != "missing_handle" {
			t.Fatalf("%s error = %#v", operationID, driverErr)
		}
	}
}
