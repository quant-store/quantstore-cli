// Command import-apis-v1 performs a one-time, review-required migration from a
// legacy product apis.json into a CLI-safe developer catalog. Its output is a
// checked-in authoring source; this command is not a runtime fallback.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"unicode"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

type endpointDocument struct {
	Endpoints []endpoint `json:"endpoints"`
}

type endpoint struct {
	Slug        string      `json:"endpoint_slug"`
	Method      string      `json:"method"`
	Path        string      `json:"upstream_path_template"`
	Label       string      `json:"label"`
	Description string      `json:"description"`
	Params      []parameter `json:"params"`
}

type parameter struct {
	Name        string `json:"name"`
	In          string `json:"in"`
	Required    bool   `json:"required"`
	Type        string `json:"type"`
	Description string `json:"description"`
}

type repeatedFlag []string

func (values *repeatedFlag) String() string { return strings.Join(*values, ",") }
func (values *repeatedFlag) Set(value string) error {
	*values = append(*values, value)
	return nil
}

func main() {
	var input, output, catalogID, productSlug, mode, surfaceID, scope, scopeDescription string
	var sideEffect string
	var idempotent bool
	var wssPath, wssOperation, wssSurface string
	var revision int64
	var mcpMappings repeatedFlag
	flag.StringVar(&input, "input", "", "legacy apis.json path")
	flag.StringVar(&output, "output", "", "reviewed developer catalog output path")
	flag.StringVar(&catalogID, "catalog-id", "", "catalog identity")
	flag.StringVar(&productSlug, "product-slug", "", "product slug")
	flag.StringVar(&mode, "mode", "passthrough", "native or passthrough")
	flag.StringVar(&surfaceID, "surface-id", "data-rest", "REST surface identity")
	flag.StringVar(&scope, "scope", "data.read", "opaque required scope")
	flag.StringVar(&scopeDescription, "scope-description", "Call this product's subscribed data surface.", "human-readable scope description")
	flag.StringVar(&sideEffect, "side-effect", "none", "operation side effect: none or mutation")
	flag.BoolVar(&idempotent, "idempotent", true, "whether operation execution is idempotent")
	flag.Int64Var(&revision, "revision", 1, "positive catalog revision")
	flag.Var(&mcpMappings, "mcp", "repeatable tool_name=endpoint_slug mapping")
	flag.StringVar(&wssPath, "wss-path", "", "optional public WSS path template")
	flag.StringVar(&wssOperation, "wss-operation", "session", "WSS protocol operation")
	flag.StringVar(&wssSurface, "wss-surface", "realtime", "WSS surface identity")
	flag.Parse()
	if input == "" || output == "" || catalogID == "" || productSlug == "" {
		fail("-input, -output, -catalog-id and -product-slug are required")
	}
	if sideEffect != "none" && sideEffect != "mutation" {
		fail("-side-effect must be none or mutation")
	}

	raw, err := os.ReadFile(input)
	if err != nil {
		fail("read input: %v", err)
	}
	var doc endpointDocument
	if err := json.Unmarshal(raw, &doc); err != nil {
		fail("parse legacy API document: %v", err)
	}

	truth := true
	idempotentValue := idempotent
	falseValue := false
	catalog := developercatalog.Catalog{
		CatalogID: catalogID, ProductSlug: productSlug, SchemaVersion: 2,
		Revision: revision, MinCLIVersion: "0.1.0", Mode: mode,
		Scopes: []developercatalog.Scope{{Code: scope, Description: scopeDescription}},
	}
	byLegacySlug := make(map[string]endpoint, len(doc.Endpoints))
	seenIDs := map[string]string{}
	for _, legacy := range doc.Endpoints {
		byLegacySlug[legacy.Slug] = legacy
		op, err := restOperation(legacy, surfaceID, scope, sideEffect, &idempotentValue, &truth)
		if err != nil {
			fail("endpoint %q: %v", legacy.Slug, err)
		}
		if prior := seenIDs[op.OperationID]; prior != "" {
			fail("normalized operation id collision: %q and %q", prior, legacy.Slug)
		}
		seenIDs[op.OperationID] = legacy.Slug
		catalog.Operations = append(catalog.Operations, op)
	}
	for _, mapping := range mcpMappings {
		parts := strings.SplitN(mapping, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			fail("invalid -mcp mapping %q", mapping)
		}
		legacy, ok := byLegacySlug[parts[1]]
		if !ok {
			fail("MCP mapping %q references unknown endpoint", mapping)
		}
		params, required, err := normalizedParams(legacy.Params, &truth)
		if err != nil {
			fail("MCP mapping %q: %v", mapping, err)
		}
		properties := map[string]any{}
		for _, param := range params {
			var schema any
			if err := json.Unmarshal(param.Schema, &schema); err != nil {
				fail("MCP schema: %v", err)
			}
			properties[param.Name] = schema
		}
		inputSchema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
		if len(required) > 0 {
			inputSchema["required"] = required
		}
		inputRaw, _ := json.Marshal(inputSchema)
		toolName := parts[0]
		opID := "mcp." + lowerKebab(toolName)
		catalog.Operations = append(catalog.Operations, developercatalog.Operation{
			OperationID: opID, SurfaceID: "mcp", Protocol: "mcp", Method: "POST", PathTemplate: "/mcp",
			ProtocolOperation: "tools/call:" + toolName, Params: []developercatalog.Parameter{}, SideEffect: sideEffect,
			Idempotent: &idempotentValue, RequiredScope: scope,
			MCP: &developercatalog.MCPTool{ToolName: toolName, Title: legacy.Label, Description: description(legacy), InputSchema: inputRaw},
		})
	}
	if wssPath != "" {
		catalog.Operations = append(catalog.Operations, developercatalog.Operation{
			OperationID: "stream.session", SurfaceID: wssSurface, Protocol: "wss", Method: "GET",
			PathTemplate: canonicalPath(wssPath), ProtocolOperation: wssOperation,
			Params:     []developercatalog.Parameter{{Name: "stream", In: "path", Required: &truth, Schema: json.RawMessage(`{"type":"string","minLength":1}`)}},
			SideEffect: "none", Idempotent: &truth, RequiredScope: scope,
			WSS: &developercatalog.WSS{Delivery: "at_most_once", Resume: &falseValue, ReplayWindowSeconds: intPtr(0)},
		})
	}
	sort.Slice(catalog.Operations, func(i, j int) bool { return catalog.Operations[i].OperationID < catalog.Operations[j].OperationID })
	out, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		fail("marshal catalog: %v", err)
	}
	out = append(out, '\n')
	if _, err := developercatalog.Parse(out); err != nil {
		fail("generated catalog rejected: %v", err)
	}
	if err := os.WriteFile(output, out, 0o644); err != nil {
		fail("write output: %v", err)
	}
	fmt.Printf("wrote %s with %d operations\n", output, len(catalog.Operations))
}

func restOperation(legacy endpoint, surfaceID, scope, sideEffect string, idempotent, truth *bool) (developercatalog.Operation, error) {
	params, _, err := normalizedParams(legacy.Params, truth)
	if err != nil {
		return developercatalog.Operation{}, err
	}
	return developercatalog.Operation{
		OperationID: lowerKebab(legacy.Slug), SurfaceID: surfaceID, Protocol: "rest", Method: strings.ToUpper(legacy.Method),
		PathTemplate: canonicalPath(legacy.Path), Description: description(legacy), Params: params,
		SideEffect: sideEffect, Idempotent: idempotent, RequiredScope: scope,
	}, nil
}

func normalizedParams(input []parameter, truth *bool) ([]developercatalog.Parameter, []string, error) {
	result := make([]developercatalog.Parameter, 0, len(input))
	required := make([]string, 0)
	byKey := map[string]int{}
	for _, legacy := range input {
		if legacy.In != "query" && legacy.In != "path" {
			return nil, nil, fmt.Errorf("unsupported param location %q", legacy.In)
		}
		name := lowerSnake(legacy.Name)
		key := legacy.In + ":" + name
		requiredValue := legacy.Required
		schemaType := legacy.Type
		if schemaType != "string" && schemaType != "integer" && schemaType != "number" && schemaType != "boolean" {
			return nil, nil, fmt.Errorf("unsupported param type %q", schemaType)
		}
		schema, _ := json.Marshal(map[string]any{"type": schemaType})
		if index, exists := byKey[key]; exists {
			var prior map[string]any
			_ = json.Unmarshal(result[index].Schema, &prior)
			if prior["type"] != schemaType {
				return nil, nil, fmt.Errorf("normalized param %q has conflicting types", key)
			}
			if legacy.Required {
				result[index].Required = truth
			}
			continue
		}
		byKey[key] = len(result)
		value := requiredValue
		result = append(result, developercatalog.Parameter{Name: name, In: legacy.In, Required: &value, Description: legacy.Description, Schema: schema})
	}
	for _, param := range result {
		if param.Required != nil && *param.Required {
			required = append(required, param.Name)
		}
	}
	sort.Strings(required)
	return result, required, nil
}

func description(value endpoint) string {
	if strings.TrimSpace(value.Description) != "" {
		return strings.TrimSpace(value.Description)
	}
	return strings.TrimSpace(value.Label)
}

var separators = regexp.MustCompile(`[-_]+`)

func lowerKebab(value string) string { return normalizeName(value, '-') }
func lowerSnake(value string) string { return normalizeName(value, '_') }
func normalizeName(value string, separator rune) string {
	value = separators.ReplaceAllString(value, string(separator))
	var out []rune
	for i, r := range []rune(value) {
		if unicode.IsUpper(r) {
			if i > 0 && len(out) > 0 && out[len(out)-1] != separator {
				out = append(out, separator)
			}
			r = unicode.ToLower(r)
		}
		out = append(out, r)
	}
	return strings.Trim(strings.ToLower(string(out)), string(separator))
}

func canonicalPath(value string) string {
	parts := strings.Split(strings.TrimPrefix(value, "/"), "/")
	for i, part := range parts {
		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			continue
		}
		parts[i] = lowerKebab(part)
	}
	return "/" + strings.Join(parts, "/")
}

func intPtr(value int) *int { return &value }
func fail(format string, args ...any) {
	fmt.Fprintln(os.Stderr, "import-apis-v1:", fmt.Sprintf(format, args...))
	os.Exit(1)
}
