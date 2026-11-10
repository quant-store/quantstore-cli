package productexec

import (
	"errors"
	"net/http"
	"testing"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

func TestPrepareDeclarativeBindsAndRechecksExactOperation(t *testing.T) {
	catalog := declarativeCatalog(t)
	operation, path, query, err := PrepareDeclarative(
		catalog,
		"quotes.latest",
		map[string][]string{"symbol": {"BTC USD"}},
		map[string][]string{"limit": {"10"}, "format": {"compact"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if operation.OperationID != "quotes.latest" || operation.Method != http.MethodGet {
		t.Fatalf("operation = %#v", operation)
	}
	if path != "/api/quotes/BTC%20USD" {
		t.Fatalf("path = %q", path)
	}
	if query.Encode() != "format=compact&limit=10" {
		t.Fatalf("query = %q", query.Encode())
	}

	// The returned values are defensive; mutating them cannot alter the input.
	query.Set("limit", "11")
	if got := query["limit"][0]; got != "11" {
		t.Fatalf("query mutation = %q", got)
	}
}

func TestPrepareDeclarativeRejectsUnsafeClassBeforeBinding(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*developercatalog.Operation)
	}{
		{name: "driver required", mutate: func(op *developercatalog.Operation) { op.CLIExecution = "driver_required" }},
		{name: "missing capability", mutate: func(op *developercatalog.Operation) { op.RequiredCLICapabilities = nil }},
		{name: "unknown capability", mutate: func(op *developercatalog.Operation) { op.RequiredCLICapabilities = []string{"rest.read.v2"} }},
		{name: "extra capability", mutate: func(op *developercatalog.Operation) {
			op.RequiredCLICapabilities = []string{"rest.read.v1", "other.v1"}
		}},
		{name: "post", mutate: func(op *developercatalog.Operation) { op.Method = http.MethodPost }},
		{name: "mcp", mutate: func(op *developercatalog.Operation) { op.Protocol = "mcp" }},
		{name: "mutation", mutate: func(op *developercatalog.Operation) { op.SideEffect = "mutation" }},
		{name: "not idempotent", mutate: func(op *developercatalog.Operation) { value := false; op.Idempotent = &value }},
		{name: "body", mutate: func(op *developercatalog.Operation) { op.BodySchema = []byte(`{"type":"object"}`) }},
		{name: "protocol operation", mutate: func(op *developercatalog.Operation) { op.ProtocolOperation = "other" }},
		{name: "mcp metadata", mutate: func(op *developercatalog.Operation) { op.MCP = &developercatalog.MCPTool{} }},
		{name: "wss metadata", mutate: func(op *developercatalog.Operation) { op.WSS = &developercatalog.WSS{} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := declarativeCatalog(t)
			test.mutate(&catalog.Operations[0])
			_, _, _, err := PrepareDeclarative(catalog, "quotes.latest", map[string][]string{"symbol": {"BTC"}}, nil)
			if !errors.Is(err, ErrDeclarativeRejected) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPrepareDeclarativeRejectsBadBindings(t *testing.T) {
	tests := []struct {
		name  string
		opID  string
		path  map[string][]string
		query map[string][]string
	}{
		{name: "unknown operation", opID: "missing", path: map[string][]string{"symbol": {"BTC"}}},
		{name: "missing required path", opID: "quotes.latest"},
		{name: "unknown path", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}, "other": {"x"}}},
		{name: "repeated path", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC", "ETH"}}},
		{name: "unknown query", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}}, query: map[string][]string{"other": {"1"}}},
		{name: "repeated query", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}}, query: map[string][]string{"limit": {"1", "2"}}},
		{name: "wrong integer type", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}}, query: map[string][]string{"limit": {"ten"}}},
		{name: "number below minimum", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}}, query: map[string][]string{"limit": {"0"}}},
		{name: "enum miss", opID: "quotes.latest", path: map[string][]string{"symbol": {"BTC"}}, query: map[string][]string{"format": {"wide"}}},
		{name: "unsafe path segment", opID: "quotes.latest", path: map[string][]string{"symbol": {"../secret"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog := declarativeCatalog(t)
			_, _, _, err := PrepareDeclarative(catalog, test.opID, test.path, test.query)
			if !errors.Is(err, ErrDeclarativeRejected) {
				t.Fatalf("error = %v", err)
			}
		})
	}
	if _, _, _, err := PrepareDeclarative(nil, "quotes.latest", nil, nil); !errors.Is(err, ErrDeclarativeRejected) {
		t.Fatalf("nil catalog error = %v", err)
	}
}

func declarativeCatalog(t *testing.T) *developercatalog.Catalog {
	t.Helper()
	raw := []byte(`{
  "catalog_id":"product/demo/proxy",
  "product_slug":"demo",
  "schema_version":2,
  "revision":1,
  "min_cli_version":"0.1.0",
  "mode":"passthrough",
  "scopes":[{"code":"data.read","description":"Read data."}],
  "operations":[{
    "operation_id":"quotes.latest",
    "surface_id":"data-rest",
    "protocol":"rest",
    "method":"GET",
    "path_template":"/api/quotes/{symbol}",
    "params":[
      {"name":"symbol","in":"path","required":true,"schema":{"type":"string","minLength":1,"maxLength":32}},
      {"name":"limit","in":"query","required":false,"schema":{"type":"integer","minimum":1,"maximum":100}},
      {"name":"format","in":"query","required":false,"schema":{"type":"string","enum":["compact","full"]}}
    ],
    "side_effect":"none",
    "idempotent":true,
    "required_scope":"data.read",
    "cli_execution":"declarative",
    "required_cli_capabilities":["rest.read.v1"]
  }]
}`)
	catalog, err := developercatalog.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
