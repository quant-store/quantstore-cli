package httpx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientSendsExactBearerWithoutCredentialQuery(t *testing.T) {
	key := "qs_live_" + strings.Repeat("A", 43)
	var capturedAuthorization, capturedQuery, capturedAPIKeyHeader string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capturedAuthorization = request.Header.Get("Authorization")
		capturedAPIKeyHeader = request.Header.Get("X-API-Key")
		capturedQuery = request.URL.RawQuery
		if request.URL.Path != "/api/v1/open/wallet/balance" {
			t.Errorf("path = %q", request.URL.Path)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"available_cents":123}`))
	}))
	defer server.Close()
	client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client(), Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.DoJSON(context.Background(), http.MethodGet, "/api/v1/open/wallet/balance", url.Values{"api_key_id": {"apk_1"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if capturedAuthorization != "Bearer "+key {
		t.Fatalf("Authorization = %q", capturedAuthorization)
	}
	if capturedAPIKeyHeader != "" {
		t.Fatalf("X-API-Key unexpectedly set: %q", capturedAPIKeyHeader)
	}
	if strings.Contains(capturedQuery, key) || strings.Contains(capturedQuery, "qs_live_") {
		t.Fatalf("credential leaked into query: %q", capturedQuery)
	}
	if capturedQuery != "api_key_id=apk_1" {
		t.Fatalf("query = %q", capturedQuery)
	}
}

func TestClientNeverFollowsRedirects(t *testing.T) {
	key := "qs_live_" + strings.Repeat("R", 43)
	statuses := []int{301, 302, 303, 307, 308}
	for _, status := range statuses {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetHits atomic.Int32
			target := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
				targetHits.Add(1)
			}))
			defer target.Close()
			source := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Header.Get("Authorization") != "Bearer "+key {
					t.Errorf("source Authorization mismatch")
				}
				writer.Header().Set("Location", target.URL+"/sink?api_key="+url.QueryEscape(key))
				writer.WriteHeader(status)
			}))
			defer source.Close()
			client, err := New(Options{BaseURL: source.URL, APIKey: key, HTTPClient: source.Client(), Timeout: time.Second})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.DoJSON(context.Background(), http.MethodGet, "/api/v1/open/me", nil, nil)
			if err == nil || !strings.Contains(err.Error(), "redirect_refused") {
				t.Fatalf("expected redirect_refused, got %v", err)
			}
			if strings.Contains(err.Error(), key) || strings.Contains(err.Error(), "qs_live_") {
				t.Fatalf("redirect error leaked key: %v", err)
			}
			if targetHits.Load() != 0 {
				t.Fatalf("redirect target was hit %d times", targetHits.Load())
			}
		})
	}
}

func TestClientNeverFollowsSameOriginRedirectOrChain(t *testing.T) {
	key := "qs_live_" + strings.Repeat("L", 43)
	for _, locationMode := range []string{"relative", "absolute"} {
		for _, status := range []int{301, 302, 303, 307, 308} {
			t.Run(locationMode+"/"+http.StatusText(status), func(t *testing.T) {
				var hopHits, sinkHits atomic.Int32
				var serverURL string
				server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					switch request.URL.Path {
					case "/api/v1/open/me":
						location := "/hop"
						if locationMode == "absolute" {
							location = serverURL + location
						}
						writer.Header().Set("Location", location)
						writer.WriteHeader(status)
					case "/hop":
						hopHits.Add(1)
						writer.Header().Set("Location", "/sink")
						writer.WriteHeader(http.StatusTemporaryRedirect)
					case "/sink":
						sinkHits.Add(1)
					}
				}))
				serverURL = server.URL
				defer server.Close()
				client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client(), Timeout: time.Second})
				if err != nil {
					t.Fatal(err)
				}
				_, err = client.DoJSON(context.Background(), http.MethodGet, "/api/v1/open/me", nil, nil)
				if err == nil || !strings.Contains(err.Error(), "redirect_refused") {
					t.Fatalf("expected redirect_refused, got %v", err)
				}
				if hopHits.Load() != 0 || sinkHits.Load() != 0 {
					t.Fatalf("redirect chain was followed: hop=%d sink=%d", hopHits.Load(), sinkHits.Load())
				}
			})
		}
	}
}

func TestClientRetriesSafeMethodsButNotMutation(t *testing.T) {
	key := "qs_live_" + strings.Repeat("Y", 43)
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		t.Run(method+" retries", func(t *testing.T) {
			var hits atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if hits.Add(1) == 1 {
					writer.WriteHeader(http.StatusServiceUnavailable)
					_, _ = writer.Write([]byte(`{"code":"sys_not_ready","message":"retry"}`))
					return
				}
				_, _ = writer.Write([]byte(`{"ok":true}`))
			}))
			defer server.Close()
			client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client(), Timeout: 2 * time.Second, MaxRetries: 1})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.DoJSON(context.Background(), method, "/api/v1/open/me", nil, nil); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 2 {
				t.Fatalf("safe method hits = %d, want 2", hits.Load())
			}
		})
	}
	t.Run("PATCH does not retry", func(t *testing.T) {
		var hits atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits.Add(1)
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte(`{"code":"sys_not_ready","message":"do not retry"}`))
		}))
		defer server.Close()
		client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client(), Timeout: time.Second, MaxRetries: 2})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := client.DoJSON(context.Background(), http.MethodPatch, "/api/v1/open/profile", nil, map[string]string{"locale": "en-US"}); err == nil {
			t.Fatal("expected service unavailable")
		}
		if hits.Load() != 1 {
			t.Fatalf("mutation hits = %d, want 1", hits.Load())
		}
	})
}

func TestClientRejectsUnsafeOriginAndPathBeforeNetwork(t *testing.T) {
	key := "qs_live_" + strings.Repeat("A", 43)
	for _, base := range []string{
		"http://example.com",
		"https://user:password@example.com",
		"https://example.com/base",
		"//example.com",
	} {
		if _, err := New(Options{BaseURL: base, APIKey: key}); err == nil {
			t.Fatalf("accepted unsafe base URL %q", base)
		}
	}
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		"/api/v1/open/../admin",
		"/api/v1/open//evil",
		"/api/v1/admin/users",
		"/api/v1/open/%2e%2e/admin",
	} {
		if _, err := client.DoJSON(context.Background(), http.MethodGet, path, nil, nil); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("unsafe request reached server %d times", hits.Load())
	}
}

func TestClientRejectsCredentialMaterialInPathQueryAndBodyBeforeNetwork(t *testing.T) {
	key := "qs_live_" + strings.Repeat("C", 43)
	var hits atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer server.Close()
	client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client()})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		path  string
		query url.Values
		body  any
	}{
		{name: "path", path: "/api/v1/open/orders/" + key},
		{name: "query", path: "/api/v1/open/usage/calls", query: url.Values{"api_key_id": {key}}},
		{name: "cursor", path: "/api/v1/open/orders", query: url.Values{"cursor": {"prefix-" + key}}},
		{name: "body", path: "/api/v1/open/profile", body: map[string]string{"locale": key}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := client.DoJSON(context.Background(), http.MethodGet, test.path, test.query, test.body)
			if err == nil || !strings.Contains(err.Error(), "credential material") {
				t.Fatalf("expected credential rejection, got %v", err)
			}
		})
	}
	if hits.Load() != 0 {
		t.Fatalf("credential-bearing requests reached server %d times", hits.Load())
	}
}

func TestAPIErrorUsesStableFieldsAndRedactsServerMessage(t *testing.T) {
	key := "qs_live_" + strings.Repeat("S", 43)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(writer).Encode(map[string]any{
			"code":       "scope_forbidden",
			"message":    "denied " + key,
			"request_id": "req_123",
			"meta":       map[string]any{"scope": "wallet_balance_read"},
		})
	}))
	defer server.Close()
	client, err := New(Options{BaseURL: server.URL, APIKey: key, HTTPClient: server.Client(), MaxRetries: 0})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DoJSON(context.Background(), http.MethodGet, "/api/v1/open/wallet/balance", nil, nil)
	apiErr, ok := err.(*APIError)
	if !ok {
		t.Fatalf("error type = %T, want *APIError", err)
	}
	if apiErr.Code != "scope_forbidden" || apiErr.RequestID != "req_123" {
		t.Fatalf("parsed error = %+v", apiErr)
	}
	if strings.Contains(apiErr.Error(), key) || !strings.Contains(apiErr.Error(), "[REDACTED]") {
		t.Fatalf("API error was not redacted: %s", apiErr.Error())
	}
}
