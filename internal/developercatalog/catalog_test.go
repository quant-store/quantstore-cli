package developercatalog

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
)

func validFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/valid-product.json")
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParseCanonicalDigestStable(t *testing.T) {
	a, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	b, err := Parse(a.CanonicalBytes())
	if err != nil {
		t.Fatal(err)
	}
	if a.Digest() != b.Digest() {
		t.Fatalf("digest drift: %s != %s", a.Digest(), b.Digest())
	}
	if len(a.Digest()) != 71 || !strings.HasPrefix(a.Digest(), "sha256:") {
		t.Fatalf("bad digest %q", a.Digest())
	}
	copyBytes := a.CanonicalBytes()
	copyBytes[0] = '['
	if bytes.Equal(copyBytes, a.CanonicalBytes()) {
		t.Fatal("CanonicalBytes exposed mutable backing storage")
	}
}

func TestParseDefaultsMissingCLIExecutionToDriverRequired(t *testing.T) {
	catalog, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, operation := range catalog.Operations {
		if operation.CLIExecution != CLIExecutionDriverRequired {
			t.Fatalf("operation %q cli_execution = %q, want %q", operation.OperationID, operation.CLIExecution, CLIExecutionDriverRequired)
		}
		if operation.RequiredCLICapabilities != nil {
			t.Fatalf("operation %q capabilities = %#v, want nil", operation.OperationID, operation.RequiredCLICapabilities)
		}
	}
}

func TestParseAcceptsExplicitDeclarativeMetadataWithoutClassifyingExecution(t *testing.T) {
	raw := bytes.Replace(validFixture(t), []byte(`"method": "GET"`), []byte(`"method": "POST"`), 1)
	raw = bytes.Replace(raw, []byte(`"side_effect": "none",`), []byte(`
      "side_effect": "mutation",
      "cli_execution": "declarative",
      "required_cli_capabilities": ["http.rest:v1", "schema.basic_v1"],`), 1)
	catalog, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	operation := catalog.Operations[0]
	if operation.Method != "POST" || operation.SideEffect != "mutation" || operation.CLIExecution != CLIExecutionDeclarative {
		t.Fatalf("unexpected declarative operation: %+v", operation)
	}
	if got := operation.RequiredCLICapabilities; len(got) != 2 || got[0] != "http.rest:v1" || got[1] != "schema.basic_v1" {
		t.Fatalf("required_cli_capabilities = %#v", got)
	}
}

func TestParseDeveloperAccessExactContract(t *testing.T) {
	valid := []byte(`{
		"interfaces": [
			{"id":"data-rest","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/coinglass/proxy"},
			{"id":"mcp","kind":"mcp_http","runtime_family":"proxy","catalog_id":"product/coinglass/proxy"}
		]
	}`)
	access, err := ParseDeveloperAccess(valid, "coinglass")
	if err != nil {
		t.Fatal(err)
	}
	if len(access.Interfaces) != 2 || access.Interfaces[1].ID != "mcp" {
		t.Fatalf("unexpected developer access: %+v", access)
	}

	tests := map[string][]byte{
		"duplicate id with different row": []byte(`{"interfaces":[
			{"id":"data","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/coinglass/proxy"},
			{"id":"data","kind":"proxy_wss","runtime_family":"proxy","catalog_id":"product/coinglass/proxy"}
		]}`),
		"mcp product control": []byte(`{"interfaces":[
			{"id":"mcp","kind":"mcp_http","runtime_family":"product_control","catalog_id":"product/coinglass/control"}
		]}`),
		"kind family mismatch": []byte(`{"interfaces":[
			{"id":"data","kind":"proxy_rest","runtime_family":"gateway","catalog_id":"product/coinglass/gateway"}
		]}`),
		"catalog family mismatch": []byte(`{"interfaces":[
			{"id":"data","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/coinglass/gateway"}
		]}`),
		"catalog product mismatch": []byte(`{"interfaces":[
			{"id":"data","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/nansen/proxy"}
		]}`),
		"unknown field": []byte(`{"interfaces":[
			{"id":"data","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/coinglass/proxy","origin":"https://evil.invalid"}
		]}`),
		"duplicate JSON field": []byte(`{"interfaces":[
			{"id":"data","id":"other","kind":"proxy_rest","runtime_family":"proxy","catalog_id":"product/coinglass/proxy"}
		]}`),
		"trailing value": append(append([]byte(nil), valid...), []byte(` {}`)...),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParseDeveloperAccess(raw, "coinglass")
			if !errors.Is(err, ErrInvalidDeveloperAccess) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := ParseDeveloperAccess(make([]byte, MaxDeveloperAccessBytes+1), "coinglass"); !errors.Is(err, ErrInvalidDeveloperAccess) {
		t.Fatalf("oversize got %v", err)
	}
}

func TestParseRejectsContractAndWireAttacks(t *testing.T) {
	tests := map[string][]byte{
		"duplicate key":            bytes.Replace(validFixture(t), []byte(`"revision": 1,`), []byte(`"revision": 1, "revision": 2,`), 1),
		"unknown root":             bytes.Replace(validFixture(t), []byte(`"mode": "native",`), []byte(`"mode": "native", "origin": "https://evil.invalid",`), 1),
		"trailing":                 append(validFixture(t), []byte(` {}`)...),
		"remote ref":               bytes.Replace(validFixture(t), []byte(`{"type": "string", "minLength": 1}`), []byte(`{"$ref":"https://evil.invalid/schema"}`), 1),
		"unknown schema keyword":   bytes.Replace(validFixture(t), []byte(`{"type": "string", "minLength": 1}`), []byte(`{"type":"string","evil":true}`), 1),
		"invalid schema type":      bytes.Replace(validFixture(t), []byte(`{"type": "string", "minLength": 1}`), []byte(`{"type":{"evil":true}}`), 1),
		"missing side effect":      bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), nil, 1),
		"missing params":           bytes.Replace(validFixture(t), []byte(`"params": [],`), nil, 1),
		"invalid MCP tool":         bytes.Replace(validFixture(t), []byte(`"tool_name": "get_tweets"`), []byte(`"tool_name": "get tweets"`), 1),
		"undeclared scope":         bytes.Replace(validFixture(t), []byte(`"required_scope": "tweets.read"`), []byte(`"required_scope": "admin.all"`), 1),
		"unknown execution field":  bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"cli_executor": "declarative", "side_effect": "none",`), 1),
		"unknown execution class":  bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"cli_execution": "generic", "side_effect": "none",`), 1),
		"null execution class":     bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"cli_execution": null, "side_effect": "none",`), 1),
		"null capabilities":        bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"required_cli_capabilities": null, "side_effect": "none",`), 1),
		"non-string capability":    bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"required_cli_capabilities": [1], "side_effect": "none",`), 1),
		"invalid capability token": bytes.Replace(validFixture(t), []byte(`"side_effect": "none",`), []byte(`"required_cli_capabilities": ["HTTP REST"], "side_effect": "none",`), 1),
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(raw)
			if !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestParseRejectsResourceCeilings(t *testing.T) {
	if _, err := Parse(make([]byte, MaxCatalogBytes+1)); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("got %v", err)
	}
	nested := strings.Repeat(`{"not":`, MaxJSONDepth+2) + `{}` + strings.Repeat(`}`, MaxJSONDepth+2)
	raw := bytes.Replace(validFixture(t), []byte(`{"type": "string", "minLength": 1}`), []byte(nested), 1)
	if _, err := Parse(raw); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("got %v", err)
	}
}
