package command

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	clientcatalog "github.com/quant-store/quantstore-cli/core/internal/catalog"
	"github.com/quant-store/quantstore-cli/core/internal/productexec"
	"github.com/quant-store/quantstore-cli/driverapi"
	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

type fixedCatalogResolver struct {
	resolved clientcatalog.Resolved
	err      error
}

func (resolver fixedCatalogResolver) Resolve(context.Context, string) (clientcatalog.Resolved, error) {
	return resolver.resolved, resolver.err
}

type captureProductTransport struct {
	mu       sync.Mutex
	requests []*http.Request
	body     string
}

func (transport *captureProductTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.mu.Unlock()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(transport.body)),
		Request:    request,
	}, nil
}

func TestProductRequestRunsExactDeclarativeOperation(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("R", 43)
	catalog := mustProductCatalog(t, "declarative")
	transport := &captureProductTransport{body: `{"symbol":"BTC","price":100}`}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "product", "request", "demo", "quotes.latest", "--path", "symbol=BTC", "--query", "limit=10"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, "https://api.quant-store.com"),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  &http.Client{Transport: transport},
		Catalogs: fixedCatalogResolver{resolved: clientcatalog.Resolved{
			Reference: clientcatalog.Reference{
				SchemaVersion: 1,
				ProductSlug:   "demo",
				CatalogID:     "product/demo/proxy",
				CatalogDigest: catalog.Digest(),
				HTTPSOrigin:   "https://demo.proxy.quant-store.com",
			},
			Catalog: catalog,
		}},
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 1 {
		t.Fatalf("product requests=%d", len(transport.requests))
	}
	request := transport.requests[0]
	if request.Method != http.MethodGet || request.URL.String() != "https://demo.proxy.quant-store.com/api/quotes/BTC?limit=10" {
		t.Fatalf("wire=%s %s", request.Method, request.URL.String())
	}
	if request.Header.Get("Authorization") != "Bearer "+key {
		t.Fatalf("Authorization=%q", request.Header.Get("Authorization"))
	}
	if request.Header.Get("X-QS-Catalog-Digest") != catalog.Digest() {
		t.Fatalf("catalog digest=%q", request.Header.Get("X-QS-Catalog-Digest"))
	}
	if request.Header.Get("X-API-Key") != "" || request.Header.Get("Cookie") != "" {
		t.Fatal("unexpected credential header")
	}
	if !strings.Contains(stdout.String(), `"price": 100`) || strings.Contains(stdout.String()+stderr.String(), key) {
		t.Fatalf("output=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestProductRequestRejectsDriverRequiredBeforeNetwork(t *testing.T) {
	clearAPIKeyEnvironment(t)
	catalog := mustProductCatalog(t, "driver_required")
	transport := &captureProductTransport{body: `{}`}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"product", "request", "demo", "quotes.latest", "--path", "symbol=BTC"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, "https://api.quant-store.com"),
		SecretStore: &memorySecrets{value: "qs_live_" + strings.Repeat("D", 43)},
		HTTPClient:  &http.Client{Transport: transport},
		Catalogs: fixedCatalogResolver{resolved: clientcatalog.Resolved{
			Reference: clientcatalog.Reference{SchemaVersion: 1, ProductSlug: "demo", CatalogID: "product/demo/proxy", CatalogDigest: catalog.Digest(), HTTPSOrigin: "https://demo.proxy.quant-store.com"},
			Catalog:   catalog,
		}},
	})
	if code == 0 || !strings.Contains(stderr.String(), "compiled product driver is unavailable") {
		t.Fatalf("exit=%d stderr=%q", code, stderr.String())
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 0 {
		t.Fatalf("unexpected product requests=%d", len(transport.requests))
	}
}

type demoDriver struct{}

func (demoDriver) Slug() string           { return "demo" }
func (demoDriver) OperationIDs() []string { return []string{"quotes.latest"} }
func (demoDriver) Handler(operationID string) (driverapi.Handler, bool) {
	if operationID != "quotes.latest" {
		return nil, false
	}
	return driverapi.StaticHandler{
		ID:     operationID,
		Effect: driverapi.SideEffectNone,
		PrepareCall: func(_ context.Context, request driverapi.Request, _ driverapi.OperationView) (driverapi.PreparedCall, []driverapi.Warning, *driverapi.DriverError) {
			request.PathParams["symbol"] = strings.ToUpper(request.PathParams["symbol"])
			return driverapi.PreparedCall{PathParams: request.PathParams, Query: request.Query}, []driverapi.Warning{{Code: "symbol_normalized", Field: "symbol"}}, nil
		},
	}, true
}

func TestProductRequestUsesCompiledDriverForExistingOperation(t *testing.T) {
	clearAPIKeyEnvironment(t)
	catalog := mustProductCatalog(t, "driver_required")
	registry, err := productexec.NewRegistry(demoDriver{})
	if err != nil {
		t.Fatal(err)
	}
	transport := &captureProductTransport{body: `{"ok":true}`}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "product", "request", "demo", "quotes.latest", "--path", "symbol=btc"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, "https://api.quant-store.com"),
		SecretStore: &memorySecrets{value: "qs_live_" + strings.Repeat("E", 43)},
		HTTPClient:  &http.Client{Transport: transport},
		Products:    registry,
		Catalogs: fixedCatalogResolver{resolved: clientcatalog.Resolved{
			Reference: clientcatalog.Reference{SchemaVersion: 1, ProductSlug: "demo", CatalogID: "product/demo/proxy", CatalogDigest: catalog.Digest(), HTTPSOrigin: "https://demo.proxy.quant-store.com"},
			Catalog:   catalog,
		}},
	})
	if code != 0 {
		t.Fatalf("exit=%d stderr=%s", code, stderr.String())
	}
	transport.mu.Lock()
	defer transport.mu.Unlock()
	if len(transport.requests) != 1 || transport.requests[0].URL.Path != "/api/quotes/BTC" {
		t.Fatalf("requests=%d path=%v", len(transport.requests), requestPath(transport.requests))
	}
	if !strings.Contains(stderr.String(), "symbol_normalized (symbol)") {
		t.Fatalf("warning=%q", stderr.String())
	}
}

func requestPath(requests []*http.Request) string {
	if len(requests) == 0 {
		return ""
	}
	return requests[0].URL.Path
}

func mustProductCatalog(t *testing.T, execution string) *developercatalog.Catalog {
	t.Helper()
	raw := `{
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
      {"name":"symbol","in":"path","required":true,"schema":{"type":"string"}},
      {"name":"limit","in":"query","required":false,"schema":{"type":"integer","minimum":1,"maximum":100}}
    ],
    "side_effect":"none",
    "idempotent":true,
    "required_scope":"data.read",
    "cli_execution":"` + execution + `",
    "required_cli_capabilities":["rest.read.v1"]
  }]
}`
	catalog, err := developercatalog.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}
