package developercatalog

import (
	"errors"
	"net/url"
	"testing"
)

func TestMatcherExactRESTAndQueryAllowlist(t *testing.T) {
	catalog, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	op, ok, err := catalog.Matcher().Match("rest", "GET", "/api/v1/tweets/abc", "", url.Values{"expand": {"author"}})
	if err != nil || !ok || op.OperationID != "tweets.get" {
		t.Fatalf("op=%+v ok=%v err=%v", op, ok, err)
	}
	if _, _, err := catalog.Matcher().Match("rest", "GET", "/api/v1/tweets/abc", "", url.Values{"debug": {"1"}}); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("got %v", err)
	}
	if _, ok, err := catalog.Matcher().Match("rest", "POST", "/api/v1/tweets/abc", "", nil); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestMatcherRequiresDeclaredQueryAndReturnsCopy(t *testing.T) {
	catalog, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	truth := true
	catalog.Operations[0].Params[1].Required = &truth
	m, err := NewMatcher(catalog.Operations)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := m.Match("rest", "GET", "/api/v1/tweets/abc", "", nil); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("got %v", err)
	}
	op, ok, err := m.Match("rest", "GET", "/api/v1/tweets/abc", "", url.Values{"expand": {"author"}})
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	op.OperationID = "tampered"
	op2, ok, err := m.Match("rest", "GET", "/api/v1/tweets/abc", "", url.Values{"expand": {"author"}})
	if err != nil || !ok || op2.OperationID != "tweets.get" {
		t.Fatalf("op=%+v ok=%v err=%v", op2, ok, err)
	}
}

func TestMatcherMCPRequiresExactProtocolOperation(t *testing.T) {
	catalog, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	op, ok, err := catalog.Matcher().Match("mcp", "POST", "/api/v1/mcp", "tools/call:get_tweets", nil)
	if err != nil || !ok || op.OperationID != "tweets.search" {
		t.Fatalf("op=%+v ok=%v err=%v", op, ok, err)
	}
	if _, ok, err := catalog.Matcher().Match("mcp", "POST", "/api/v1/mcp", "tools/call:unknown", nil); err != nil || ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
}

func TestMatcherRejectsUnsafeEscapedPath(t *testing.T) {
	catalog, err := Parse(validFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/v1/tweets/%2Fetc", "/api/v1/tweets/%5Cetc", "/api/v1/tweets/%61", "/api/v1/tweets/%2e%2e", "/api/v1/tweets/%2E%2E", "/api/v1/tweets/%FF", "/api/v1/tweets/%0A", "/api/v1/tweets/\n", "/api/v1/tweets/..", "/api//v1/tweets/a", "/api/v1/tweets/a?x=1"} {
		t.Run(path, func(t *testing.T) {
			if _, _, err := catalog.Matcher().Match("rest", "GET", path, "", nil); !errors.Is(err, ErrInvalidCatalog) {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestNewMatcherRejectsLiteralParameterOverlap(t *testing.T) {
	truth := true
	ops := []Operation{
		{OperationID: "a", SurfaceID: "rest", Protocol: "rest", Method: "GET", PathTemplate: "/users/{id}", Params: []Parameter{{Name: "id", In: "path", Required: &truth}}},
		{OperationID: "b", SurfaceID: "rest", Protocol: "rest", Method: "GET", PathTemplate: "/users/me"},
	}
	if _, err := NewMatcher(ops); !errors.Is(err, ErrInvalidCatalog) {
		t.Fatalf("got %v", err)
	}
}
