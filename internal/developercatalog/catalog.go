// Package developercatalog parses and validates QuantStore CLI-safe developer
// catalogs. It is a product-neutral authorization primitive shared by the CLI,
// platform gateway and product runtimes; it never resolves origins or users.
package developercatalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/quant-store/quantstore-cli/internal/wirehash"
)

const (
	MaxCatalogBytes         = 8 << 20
	MaxDeveloperAccessBytes = 16 << 10
	MaxOperations           = 4096
	MaxJSONDepth            = 32
	MaxJSONNodes            = 200000
	MaxStringBytes          = 64 << 10
	maxSchemaBranch         = 256
)

var (
	ErrInvalidCatalog         = errors.New("developercatalog: invalid catalog")
	ErrInvalidDeveloperAccess = errors.New("developercatalog: invalid developer access")
	idRE                      = regexp.MustCompile(`^[a-z][a-z0-9._:-]{0,199}$`)
	surfaceRE                 = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	scopeRE                   = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
	slugRE                    = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	semverRE                  = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?$`)
	catalogIDRE               = regexp.MustCompile(`^(platform/[a-z][a-z0-9-]*|product/[a-z][a-z0-9_-]{1,63}/(proxy|gateway|control))$`)
	toolNameRE                = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	protocolOpRE              = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,255}$`)
	cliCapabilityRE           = regexp.MustCompile(`^[a-z][a-z0-9_.:-]{0,127}$`)
)

const (
	CLIExecutionDeclarative    = "declarative"
	CLIExecutionDriverRequired = "driver_required"
)

// DeveloperAccess is the strict manifest developer_access block. It carries
// logical interface identity only; origins, runtime digests, operations,
// scopes and user state belong to other contracts.
type DeveloperAccess struct {
	Interfaces []DeveloperInterface `json:"interfaces"`
}

type DeveloperInterface struct {
	ID            string `json:"id"`
	Kind          string `json:"kind"`
	RuntimeFamily string `json:"runtime_family"`
	CatalogID     string `json:"catalog_id"`
}

// ParseDeveloperAccess validates one manifest developer_access JSON value.
// productSlug binds product catalog IDs to the owning manifest slug. Platform
// catalog IDs remain syntactically valid because they are part of the frozen
// manifest v1 wire, but they never weaken kind/runtime-family compatibility.
func ParseDeveloperAccess(raw []byte, productSlug string) (*DeveloperAccess, error) {
	if len(raw) == 0 || len(raw) > MaxDeveloperAccessBytes {
		return nil, invalidDeveloperAccess("contract size must be 1..%d bytes", MaxDeveloperAccessBytes)
	}
	if !slugRE.MatchString(productSlug) {
		return nil, invalidDeveloperAccess("invalid product slug")
	}
	canonical, err := wirehash.CanonicalizeJSON(raw)
	if err != nil {
		return nil, invalidDeveloperAccess("canonical JSON: %v", err)
	}
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	var access DeveloperAccess
	if err := dec.Decode(&access); err != nil {
		return nil, invalidDeveloperAccess("contract: %v", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, invalidDeveloperAccess("multiple JSON values")
	}
	if len(access.Interfaces) == 0 || len(access.Interfaces) > 16 {
		return nil, invalidDeveloperAccess("interfaces must contain 1..16 entries")
	}
	seen := make(map[string]struct{}, len(access.Interfaces))
	for i, iface := range access.Interfaces {
		if !surfaceRE.MatchString(iface.ID) {
			return nil, invalidDeveloperAccess("interface %d has invalid id", i)
		}
		if _, exists := seen[iface.ID]; exists {
			return nil, invalidDeveloperAccess("duplicate interface id %q", iface.ID)
		}
		seen[iface.ID] = struct{}{}
		family, ok := developerInterfaceFamily(iface.Kind)
		if iface.Kind == "mcp_http" {
			if iface.RuntimeFamily != "proxy" && iface.RuntimeFamily != "gateway" {
				return nil, invalidDeveloperAccess("interface %q has incompatible kind/runtime_family", iface.ID)
			}
			family = iface.RuntimeFamily
		} else if !ok || iface.RuntimeFamily != family {
			return nil, invalidDeveloperAccess("interface %q has incompatible kind/runtime_family", iface.ID)
		}
		if !catalogIDRE.MatchString(iface.CatalogID) {
			return nil, invalidDeveloperAccess("interface %q has invalid catalog_id", iface.ID)
		}
		if strings.HasPrefix(iface.CatalogID, "product/") {
			want := "product/" + productSlug + "/" + developerCatalogFamily(family)
			if iface.CatalogID != want {
				return nil, invalidDeveloperAccess("interface %q catalog_id does not match product/runtime family", iface.ID)
			}
		}
	}
	return &access, nil
}

func developerInterfaceFamily(kind string) (string, bool) {
	switch kind {
	case "product_control_rest":
		return "product_control", true
	case "proxy_rest", "proxy_wss":
		return "proxy", true
	case "gateway_rest", "gateway_wss":
		return "gateway", true
	case "mcp_http":
		// MCP needs runtime_family to disambiguate proxy from gateway, so it
		// is validated by the caller below rather than inferred here.
		return "", true
	default:
		return "", false
	}
}

func developerCatalogFamily(runtimeFamily string) string {
	if runtimeFamily == "product_control" {
		return "control"
	}
	return runtimeFamily
}

func invalidDeveloperAccess(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidDeveloperAccess, fmt.Sprintf(format, args...))
}

type Catalog struct {
	CatalogID     string      `json:"catalog_id"`
	ProductSlug   string      `json:"product_slug"`
	SchemaVersion int         `json:"schema_version"`
	Revision      int64       `json:"revision"`
	MinCLIVersion string      `json:"min_cli_version"`
	Mode          string      `json:"mode"`
	Scopes        []Scope     `json:"scopes"`
	Operations    []Operation `json:"operations"`

	canonical []byte
	digest    string
	matcher   *Matcher
}

type Scope struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

type Operation struct {
	OperationID             string          `json:"operation_id"`
	SurfaceID               string          `json:"surface_id"`
	Protocol                string          `json:"protocol"`
	Method                  string          `json:"method"`
	PathTemplate            string          `json:"path_template"`
	ProtocolOperation       string          `json:"protocol_operation,omitempty"`
	Description             string          `json:"description,omitempty"`
	Params                  []Parameter     `json:"params"`
	BodySchema              json.RawMessage `json:"body_schema,omitempty"`
	ResponseSchema          json.RawMessage `json:"response_schema,omitempty"`
	SideEffect              string          `json:"side_effect"`
	Idempotent              *bool           `json:"idempotent"`
	RequiredScope           string          `json:"required_scope"`
	CLIExecution            string          `json:"cli_execution,omitempty"`
	RequiredCLICapabilities []string        `json:"required_cli_capabilities,omitempty"`
	MCP                     *MCPTool        `json:"mcp,omitempty"`
	WSS                     *WSS            `json:"wss,omitempty"`
}

type Parameter struct {
	Name        string          `json:"name"`
	In          string          `json:"in"`
	Required    *bool           `json:"required"`
	Description string          `json:"description,omitempty"`
	Schema      json.RawMessage `json:"schema"`
}

type MCPTool struct {
	ToolName     string          `json:"tool_name"`
	Title        string          `json:"title"`
	Description  string          `json:"description"`
	InputSchema  json.RawMessage `json:"input_schema"`
	OutputSchema json.RawMessage `json:"output_schema,omitempty"`
}

type WSS struct {
	Delivery             string          `json:"delivery"`
	Resume               *bool           `json:"resume"`
	ReplayWindowSeconds  *int            `json:"replay_window_seconds"`
	SubscribeSchema      json.RawMessage `json:"subscribe_schema,omitempty"`
	CursorField          string          `json:"cursor_field,omitempty"`
	ReconcileOperationID string          `json:"reconcile_operation_id,omitempty"`
}

func Parse(raw []byte) (*Catalog, error) {
	if len(raw) == 0 || len(raw) > MaxCatalogBytes {
		return nil, invalid("catalog size must be 1..%d bytes", MaxCatalogBytes)
	}
	canonical, err := wirehash.CanonicalizeJSON(raw)
	if err != nil {
		return nil, invalid("canonical JSON: %v", err)
	}
	var generic any
	dec := json.NewDecoder(bytes.NewReader(canonical))
	dec.UseNumber()
	if err := dec.Decode(&generic); err != nil {
		return nil, invalid("JSON: %v", err)
	}
	if err := validateResourceTree(generic, 1, new(int)); err != nil {
		return nil, err
	}
	if err := validateCLIExecutionWire(generic); err != nil {
		return nil, err
	}

	dec = json.NewDecoder(bytes.NewReader(canonical))
	dec.DisallowUnknownFields()
	var catalog Catalog
	if err := dec.Decode(&catalog); err != nil {
		return nil, invalid("contract: %v", err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, invalid("multiple JSON values")
	}
	if err := catalog.validate(); err != nil {
		return nil, err
	}
	matcher, err := NewMatcher(catalog.Operations)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	catalog.canonical = append([]byte(nil), canonical...)
	catalog.digest = "sha256:" + hex.EncodeToString(sum[:])
	catalog.matcher = matcher
	return &catalog, nil
}

func (c *Catalog) CanonicalBytes() []byte {
	if c == nil {
		return nil
	}
	return append([]byte(nil), c.canonical...)
}

func (c *Catalog) Digest() string {
	if c == nil {
		return ""
	}
	return c.digest
}

func (c *Catalog) Matcher() *Matcher {
	if c == nil {
		return nil
	}
	return c.matcher
}

func (c *Catalog) validate() error {
	if c.SchemaVersion != 2 {
		return invalid("schema_version must equal 2")
	}
	if c.Revision < 1 {
		return invalid("revision must be positive")
	}
	if !slugRE.MatchString(c.ProductSlug) {
		return invalid("invalid product_slug")
	}
	if !validCatalogID(c.CatalogID, c.ProductSlug) {
		return invalid("catalog_id does not match product_slug")
	}
	if !semverRE.MatchString(c.MinCLIVersion) {
		return invalid("invalid min_cli_version")
	}
	if c.Mode != "native" && c.Mode != "passthrough" {
		return invalid("invalid mode")
	}
	if len(c.Scopes) > 512 {
		return invalid("too many scopes")
	}
	if c.Scopes == nil {
		return invalid("scopes is required")
	}
	if len(c.Operations) == 0 || len(c.Operations) > MaxOperations {
		return invalid("operations must contain 1..%d entries", MaxOperations)
	}

	scopes := make(map[string]struct{}, len(c.Scopes))
	for _, scope := range c.Scopes {
		if !scopeRE.MatchString(scope.Code) || strings.TrimSpace(scope.Description) == "" || len(scope.Description) > 1000 {
			return invalid("invalid scope %q", scope.Code)
		}
		if _, exists := scopes[scope.Code]; exists {
			return invalid("duplicate scope %q", scope.Code)
		}
		scopes[scope.Code] = struct{}{}
	}
	seenOps := make(map[string]struct{}, len(c.Operations))
	for i := range c.Operations {
		op := &c.Operations[i]
		if err := validateOperation(op, scopes); err != nil {
			return invalid("operation %q: %v", op.OperationID, err)
		}
		if _, exists := seenOps[op.OperationID]; exists {
			return invalid("duplicate operation_id %q", op.OperationID)
		}
		seenOps[op.OperationID] = struct{}{}
	}
	for _, op := range c.Operations {
		if op.WSS != nil && op.WSS.ReconcileOperationID != "" {
			if _, ok := seenOps[op.WSS.ReconcileOperationID]; !ok {
				return invalid("operation %q references unknown reconcile operation", op.OperationID)
			}
		}
	}
	return nil
}

func validateOperation(op *Operation, scopes map[string]struct{}) error {
	if !idRE.MatchString(op.OperationID) {
		return errors.New("invalid operation_id")
	}
	if !surfaceRE.MatchString(op.SurfaceID) {
		return errors.New("invalid surface_id")
	}
	if op.Protocol != "rest" && op.Protocol != "wss" && op.Protocol != "mcp" {
		return errors.New("invalid protocol")
	}
	if !validMethod(op.Method) {
		return errors.New("invalid method")
	}
	if op.Idempotent == nil {
		return errors.New("idempotent is required")
	}
	if op.SideEffect != "none" && op.SideEffect != "mutation" {
		return errors.New("side_effect is required and must be none or mutation")
	}
	switch op.CLIExecution {
	case "":
		// Catalogs created before this field existed remain parseable, but the
		// absence can never opt an operation into the generic executor.
		op.CLIExecution = CLIExecutionDriverRequired
	case CLIExecutionDeclarative, CLIExecutionDriverRequired:
	default:
		return errors.New("invalid cli_execution")
	}
	for _, capability := range op.RequiredCLICapabilities {
		if !cliCapabilityRE.MatchString(capability) {
			return fmt.Errorf("invalid required_cli_capability %q", capability)
		}
	}
	if !scopeRE.MatchString(op.RequiredScope) {
		return errors.New("invalid required_scope")
	}
	if _, ok := scopes[op.RequiredScope]; !ok {
		return errors.New("required_scope is not declared")
	}
	if len(op.Description) > 4000 {
		return errors.New("description too long")
	}
	if len(op.Params) > 128 {
		return errors.New("too many params")
	}
	if op.Params == nil {
		return errors.New("params is required")
	}

	pathParams, err := parseTemplate(op.PathTemplate)
	if err != nil {
		return err
	}
	seenParams := make(map[string]struct{}, len(op.Params))
	declaredPath := make(map[string]struct{})
	for _, p := range op.Params {
		if !paramRE.MatchString(p.Name) || (p.In != "path" && p.In != "query") || p.Required == nil {
			return fmt.Errorf("invalid parameter %q", p.Name)
		}
		if len(p.Description) > 1000 {
			return fmt.Errorf("parameter %q description too long", p.Name)
		}
		key := p.In + ":" + p.Name
		if _, exists := seenParams[key]; exists {
			return fmt.Errorf("duplicate parameter %q", key)
		}
		seenParams[key] = struct{}{}
		if p.In == "path" {
			if !*p.Required {
				return fmt.Errorf("path parameter %q must be required", p.Name)
			}
			declaredPath[p.Name] = struct{}{}
		}
		if err := validateSafeSchema(p.Schema); err != nil {
			return fmt.Errorf("parameter %q schema: %w", p.Name, err)
		}
	}
	if !sameStringSet(pathParams, declaredPath) {
		return errors.New("path template parameters and declared path params differ")
	}
	for label, raw := range map[string]json.RawMessage{"body_schema": op.BodySchema, "response_schema": op.ResponseSchema} {
		if len(raw) > 0 {
			if err := validateSafeSchema(raw); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
		}
	}

	switch op.Protocol {
	case "rest":
		if op.ProtocolOperation != "" || op.MCP != nil || op.WSS != nil {
			return errors.New("REST operation cannot have protocol_operation/mcp/wss")
		}
	case "mcp":
		if op.Method != "POST" || op.MCP == nil || op.WSS != nil {
			return errors.New("MCP requires POST and mcp metadata only")
		}
		if op.ProtocolOperation != "tools/call:"+op.MCP.ToolName {
			return errors.New("protocol_operation must equal tools/call:<tool_name>")
		}
		if !toolNameRE.MatchString(op.MCP.ToolName) || !protocolOpRE.MatchString(op.ProtocolOperation) {
			return errors.New("invalid MCP tool_name or protocol_operation")
		}
		if strings.TrimSpace(op.MCP.Title) == "" || len(op.MCP.Title) > 200 || strings.TrimSpace(op.MCP.Description) == "" || len(op.MCP.Description) > 4000 {
			return errors.New("MCP title and description are required")
		}
		if err := validateSafeSchema(op.MCP.InputSchema); err != nil {
			return fmt.Errorf("mcp input_schema: %w", err)
		}
		if len(op.MCP.OutputSchema) > 0 {
			if err := validateSafeSchema(op.MCP.OutputSchema); err != nil {
				return fmt.Errorf("mcp output_schema: %w", err)
			}
		}
	case "wss":
		if op.Method != "GET" || op.WSS == nil || op.MCP != nil || !protocolOpRE.MatchString(op.ProtocolOperation) {
			return errors.New("WSS requires GET, protocol_operation and wss metadata")
		}
		if op.WSS.Resume == nil || op.WSS.ReplayWindowSeconds == nil || (op.WSS.Delivery != "at_most_once" && op.WSS.Delivery != "at_least_once") || *op.WSS.ReplayWindowSeconds < 0 || *op.WSS.ReplayWindowSeconds > 604800 {
			return errors.New("invalid WSS delivery contract")
		}
		if len(op.WSS.SubscribeSchema) > 0 {
			if err := validateSafeSchema(op.WSS.SubscribeSchema); err != nil {
				return fmt.Errorf("wss subscribe_schema: %w", err)
			}
		}
		if *op.WSS.Resume && (op.WSS.CursorField == "" || op.WSS.ReconcileOperationID == "") {
			return errors.New("resumable WSS requires cursor_field and reconcile_operation_id")
		}
	}
	return nil
}

// validateCLIExecutionWire distinguishes an omitted optional field from an
// explicit JSON null. encoding/json maps null into the zero value for strings
// and slices, which would otherwise make a malformed new wire indistinguishable
// from a safe legacy catalog.
func validateCLIExecutionWire(value any) error {
	root, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	operations, ok := root["operations"].([]any)
	if !ok {
		return nil
	}
	for index, raw := range operations {
		operation, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if execution, exists := operation["cli_execution"]; exists {
			if _, ok := execution.(string); !ok {
				return invalid("operation %d cli_execution must be a string", index)
			}
		}
		if capabilities, exists := operation["required_cli_capabilities"]; exists {
			items, ok := capabilities.([]any)
			if !ok {
				return invalid("operation %d required_cli_capabilities must be an array", index)
			}
			for _, capability := range items {
				if _, ok := capability.(string); !ok {
					return invalid("operation %d required_cli_capabilities entries must be strings", index)
				}
			}
		}
	}
	return nil
}

func validCatalogID(id, slug string) bool {
	if !catalogIDRE.MatchString(id) {
		return false
	}
	if slug == "platform" && strings.HasPrefix(id, "platform/") {
		return true
	}
	return strings.HasPrefix(id, "product/"+slug+"/") && (strings.HasSuffix(id, "/proxy") || strings.HasSuffix(id, "/gateway") || strings.HasSuffix(id, "/control"))
}

func validMethod(method string) bool {
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE":
		return true
	}
	return false
}

func validateResourceTree(value any, depth int, nodes *int) error {
	*nodes++
	if depth > MaxJSONDepth {
		return invalid("JSON nesting exceeds %d", MaxJSONDepth)
	}
	if *nodes > MaxJSONNodes {
		return invalid("JSON node count exceeds %d", MaxJSONNodes)
	}
	switch v := value.(type) {
	case string:
		if len(v) > MaxStringBytes {
			return invalid("JSON string exceeds %d bytes", MaxStringBytes)
		}
	case []any:
		for _, item := range v {
			if err := validateResourceTree(item, depth+1, nodes); err != nil {
				return err
			}
		}
	case map[string]any:
		for k, item := range v {
			if len(k) > MaxStringBytes {
				return invalid("JSON key exceeds %d bytes", MaxStringBytes)
			}
			if err := validateResourceTree(item, depth+1, nodes); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateSafeSchema(raw json.RawMessage) error {
	if len(raw) == 0 {
		return errors.New("schema is required")
	}
	var root any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		return err
	}
	obj, ok := root.(map[string]any)
	if !ok {
		return errors.New("schema must be an object")
	}
	defs := map[string]any{}
	for _, name := range []string{"definitions", "$defs"} {
		if rawDefs, exists := obj[name]; exists {
			m, ok := rawDefs.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object", name)
			}
			for key, value := range m {
				defs["#/"+name+"/"+key] = value
			}
		}
	}
	branches := 0
	return walkSchema(obj, defs, map[string]bool{}, 1, &branches)
}

var safeSchemaKeywords = map[string]struct{}{
	"$ref": {}, "$defs": {}, "definitions": {}, "type": {}, "title": {}, "description": {}, "default": {}, "examples": {},
	"enum": {}, "const": {}, "format": {}, "minimum": {}, "maximum": {}, "exclusiveMinimum": {}, "exclusiveMaximum": {},
	"minLength": {}, "maxLength": {}, "pattern": {}, "minItems": {}, "maxItems": {}, "uniqueItems": {},
	"properties": {}, "required": {}, "additionalProperties": {}, "items": {}, "oneOf": {}, "anyOf": {}, "allOf": {}, "not": {},
}

func walkSchema(value any, defs map[string]any, visiting map[string]bool, depth int, branches *int) error {
	if depth > MaxJSONDepth {
		return errors.New("schema nesting too deep")
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return errors.New("schema node must be an object")
	}
	for key, child := range obj {
		if _, ok := safeSchemaKeywords[key]; !ok {
			return fmt.Errorf("unsupported schema keyword %q", key)
		}
		switch key {
		case "$ref":
			ref, ok := child.(string)
			if !ok || !strings.HasPrefix(ref, "#/") {
				return errors.New("only local $ref is allowed")
			}
			target, ok := defs[ref]
			if !ok {
				return fmt.Errorf("unresolved $ref %q", ref)
			}
			if visiting[ref] {
				return fmt.Errorf("cyclic $ref %q", ref)
			}
			visiting[ref] = true
			if err := walkSchema(target, defs, visiting, depth+1, branches); err != nil {
				return err
			}
			delete(visiting, ref)
		case "properties", "definitions", "$defs":
			m, ok := child.(map[string]any)
			if !ok {
				return fmt.Errorf("%s must be an object", key)
			}
			keys := make([]string, 0, len(m))
			for k := range m {
				if k == "" || len(k) > 128 || ((key == "definitions" || key == "$defs") && strings.ContainsAny(k, "~/")) {
					return fmt.Errorf("invalid %s key %q", key, k)
				}
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				if err := walkSchema(m[k], defs, visiting, depth+1, branches); err != nil {
					return err
				}
			}
		case "items", "not":
			if err := walkSchema(child, defs, visiting, depth+1, branches); err != nil {
				return err
			}
		case "additionalProperties":
			if _, ok := child.(bool); !ok {
				if err := walkSchema(child, defs, visiting, depth+1, branches); err != nil {
					return err
				}
			}
		case "oneOf", "anyOf", "allOf":
			items, ok := child.([]any)
			if !ok || len(items) == 0 {
				return fmt.Errorf("%s must be a non-empty array", key)
			}
			*branches += len(items)
			if *branches > maxSchemaBranch {
				return errors.New("schema branch limit exceeded")
			}
			for _, item := range items {
				if err := walkSchema(item, defs, visiting, depth+1, branches); err != nil {
					return err
				}
			}
		case "type":
			if err := validateSchemaType(child); err != nil {
				return err
			}
		case "title", "description", "format", "pattern":
			if _, ok := child.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		case "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum":
			if _, ok := child.(json.Number); !ok {
				return fmt.Errorf("%s must be a number", key)
			}
		case "minLength", "maxLength", "minItems", "maxItems":
			if !nonNegativeInteger(child) {
				return fmt.Errorf("%s must be a non-negative integer", key)
			}
		case "uniqueItems":
			if _, ok := child.(bool); !ok {
				return errors.New("uniqueItems must be boolean")
			}
		case "required":
			items, ok := child.([]any)
			if !ok {
				return errors.New("required must be an array")
			}
			seen := map[string]struct{}{}
			for _, item := range items {
				name, ok := item.(string)
				if !ok || name == "" || len(name) > 128 {
					return errors.New("required entries must be bounded strings")
				}
				if _, exists := seen[name]; exists {
					return fmt.Errorf("duplicate required property %q", name)
				}
				seen[name] = struct{}{}
			}
		case "enum", "examples":
			items, ok := child.([]any)
			if !ok || len(items) > 256 {
				return fmt.Errorf("%s must be an array with at most 256 items", key)
			}
		}
	}
	return nil
}

func validateSchemaType(value any) error {
	valid := func(name string) bool {
		switch name {
		case "null", "boolean", "object", "array", "number", "integer", "string":
			return true
		}
		return false
	}
	switch typed := value.(type) {
	case string:
		if !valid(typed) {
			return fmt.Errorf("unsupported schema type %q", typed)
		}
	case []any:
		if len(typed) == 0 || len(typed) > 7 {
			return errors.New("schema type array must contain 1..7 values")
		}
		seen := map[string]struct{}{}
		for _, item := range typed {
			name, ok := item.(string)
			if !ok || !valid(name) {
				return errors.New("schema type array contains an invalid value")
			}
			if _, exists := seen[name]; exists {
				return fmt.Errorf("duplicate schema type %q", name)
			}
			seen[name] = struct{}{}
		}
	default:
		return errors.New("schema type must be a string or string array")
	}
	return nil
}

func nonNegativeInteger(value any) bool {
	number, ok := value.(json.Number)
	if !ok {
		return false
	}
	parsed, err := number.Int64()
	return err == nil && parsed >= 0
}

func sameStringSet(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if _, ok := b[key]; !ok {
			return false
		}
	}
	return true
}

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidCatalog, fmt.Sprintf(format, args...))
}
