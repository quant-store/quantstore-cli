package producthttp

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/quant-store/quantstore-cli/core/internal/transport/httpx"
)

const (
	testKey    = "qs_live_product_transport_secret"
	testDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

type captureRoundTripper struct {
	mu       sync.Mutex
	requests []*http.Request
	status   int
	header   http.Header
	body     string
	err      error
	close    *trackingCloser
}

func (transport *captureRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.mu.Lock()
	transport.requests = append(transport.requests, request.Clone(request.Context()))
	transport.mu.Unlock()
	if transport.err != nil {
		return nil, transport.err
	}
	status := transport.status
	if status == 0 {
		status = http.StatusOK
	}
	body := io.NopCloser(strings.NewReader(transport.body))
	if transport.close != nil {
		body = transport.close
	}
	return &http.Response{StatusCode: status, Header: transport.header.Clone(), Body: body, Request: request}, nil
}

func (transport *captureRoundTripper) count() int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return len(transport.requests)
}

type trackingCloser struct {
	reader io.Reader
	closed int
}

func (body *trackingCloser) Read(value []byte) (int, error) { return body.reader.Read(value) }
func (body *trackingCloser) Close() error                   { body.closed++; return nil }

func TestClientSendsExactCredentialBearingRequest(t *testing.T) {
	transport := &captureRoundTripper{
		header: http.Header{"Content-Type": {"application/json; charset=utf-8"}},
		body:   `{"price":100}`,
	}
	original := &http.Client{Transport: transport, Timeout: time.Second}
	client, err := New(Options{
		Origin:        "https://demo.proxy.quant-store.com",
		APIKey:        testKey,
		CatalogDigest: testDigest,
		UserAgent:     "quantstore-cli/test",
		Timeout:       12 * time.Second,
		HTTPClient:    original,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(context.Background(), http.MethodGet, "/api/quotes/BTC%20USD", url.Values{"limit": {"10"}})
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(response.Body) != `{"price":100}` {
		t.Fatalf("response = %#v", response)
	}
	if transport.count() != 1 {
		t.Fatalf("requests = %d", transport.count())
	}
	request := transport.requests[0]
	if request.URL.String() != "https://demo.proxy.quant-store.com/api/quotes/BTC%20USD?limit=10" {
		t.Fatalf("URL = %s", request.URL)
	}
	if request.Header.Get("Authorization") != "Bearer "+testKey || request.Header.Get("X-QS-Catalog-Digest") != testDigest {
		t.Fatalf("headers = %#v", request.Header)
	}
	if request.Header.Get("Accept") != "application/json" || request.Header.Get("Accept-Encoding") != "identity" || request.Header.Get("User-Agent") != "quantstore-cli/test" {
		t.Fatalf("headers = %#v", request.Header)
	}
	if request.Header.Get("Cookie") != "" || request.Header.Get("X-API-Key") != "" || request.Body != nil {
		t.Fatal("request carried an unexpected cookie, header, or body")
	}
	if original.Timeout != time.Second || original.CheckRedirect != nil {
		t.Fatal("New mutated the caller's HTTP client")
	}
}

func TestClientRejectsBeforeNetwork(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		query  url.Values
	}{
		{name: "method", method: http.MethodPost, path: "/api/quotes"},
		{name: "absolute path", method: http.MethodGet, path: "https://evil.invalid/x"},
		{name: "path traversal", method: http.MethodGet, path: "/api/../secret"},
		{name: "encoded slash", method: http.MethodGet, path: "/api/a%2Fb"},
		{name: "lowercase escape", method: http.MethodGet, path: "/api/a%2fb"},
		{name: "path credential value", method: http.MethodGet, path: "/api/" + testKey},
		{name: "query credential name", method: http.MethodGet, path: "/api/quotes", query: url.Values{"api_key": {"x"}}},
		{name: "query credential value", method: http.MethodGet, path: "/api/quotes", query: url.Values{"symbol": {testKey}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := successfulTransport()
			client := newTestClient(t, transport)
			if _, err := client.Do(context.Background(), test.method, test.path, test.query); err == nil {
				t.Fatal("expected rejection")
			}
			if transport.count() != 0 {
				t.Fatalf("network requests = %d", transport.count())
			}
		})
	}
}

func TestNewRejectsUntrustedOriginAndInvalidSecurityInputs(t *testing.T) {
	tests := []Options{
		{Origin: "http://demo.proxy.quant-store.com", APIKey: testKey, CatalogDigest: testDigest},
		{Origin: "https://evil.invalid", APIKey: testKey, CatalogDigest: testDigest},
		{Origin: "https://demo.proxy.quant-store.com/path", APIKey: testKey, CatalogDigest: testDigest},
		{Origin: "https://demo.proxy.quant-store.com:443", APIKey: testKey, CatalogDigest: testDigest},
		{Origin: "https://demo.proxy.quant-store.com", APIKey: "", CatalogDigest: testDigest},
		{Origin: "https://demo.proxy.quant-store.com", APIKey: "bad\nkey", CatalogDigest: testDigest},
		{Origin: "https://demo.proxy.quant-store.com", APIKey: testKey, CatalogDigest: "sha256:BAD"},
		{Origin: "https://demo.proxy.quant-store.com", APIKey: testKey, CatalogDigest: testDigest, Timeout: 6 * time.Minute},
	}
	for index, options := range tests {
		if _, err := New(options); err == nil {
			t.Fatalf("case %d accepted invalid options", index)
		}
	}
}

func TestClientRejectsRedirectInvalidJSONEncodingAndOversize(t *testing.T) {
	tests := []struct {
		name      string
		transport *captureRoundTripper
		code      string
	}{
		{name: "redirect", transport: &captureRoundTripper{status: http.StatusFound, header: http.Header{"Location": {"https://evil.invalid/"}}}, code: "redirect_refused"},
		{name: "invalid json", transport: &captureRoundTripper{header: http.Header{"Content-Type": {"application/json"}}, body: `not-json`}, code: "product_response_invalid"},
		{name: "content type", transport: &captureRoundTripper{header: http.Header{"Content-Type": {"text/plain"}}, body: `{}`}, code: "product_response_invalid"},
		{name: "encoding", transport: &captureRoundTripper{header: http.Header{"Content-Type": {"application/json"}, "Content-Encoding": {"gzip"}}, body: `{}`}, code: "product_response_invalid"},
		{name: "oversize", transport: &captureRoundTripper{header: http.Header{"Content-Type": {"application/json"}}, body: strings.Repeat("x", maxResponseBytes+1)}, code: "response_too_large"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := newTestClient(t, test.transport)
			_, err := client.Do(context.Background(), http.MethodGet, "/api/quotes", nil)
			var apiErr *httpx.APIError
			if !errors.As(err, &apiErr) || apiErr.Code != test.code {
				t.Fatalf("error = %#v", err)
			}
			if test.transport.count() != 1 {
				t.Fatalf("requests = %d", test.transport.count())
			}
		})
	}
}

func TestClientMapsJSONErrorAndClosesBodyOnce(t *testing.T) {
	body := &trackingCloser{reader: strings.NewReader(`{"code":"product_denied","message":"denied","request_id":"req_body"}`)}
	transport := &captureRoundTripper{
		status: http.StatusForbidden,
		header: http.Header{"Content-Type": {"application/json"}, "X-Request-Id": {"req_header"}},
		close:  body,
	}
	client := newTestClient(t, transport)
	_, err := client.Do(context.Background(), http.MethodGet, "/api/quotes", nil)
	var apiErr *httpx.APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusForbidden || apiErr.Code != "product_denied" || apiErr.RequestID != "req_header" {
		t.Fatalf("error = %#v", err)
	}
	if body.closed != 1 {
		t.Fatalf("close count = %d", body.closed)
	}
}

func TestClientHeadReturnsEmptyResponse(t *testing.T) {
	transport := &captureRoundTripper{status: http.StatusNoContent, header: make(http.Header)}
	client := newTestClient(t, transport)
	response, err := client.Do(context.Background(), http.MethodHead, "/api/health", nil)
	if err != nil || response == nil || response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		t.Fatalf("response=%#v error=%v", response, err)
	}
}

func successfulTransport() *captureRoundTripper {
	return &captureRoundTripper{header: http.Header{"Content-Type": {"application/json"}}, body: `{}`}
}

func newTestClient(t *testing.T, transport http.RoundTripper) *Client {
	t.Helper()
	client, err := New(Options{
		Origin:        "https://demo.proxy.quant-store.com",
		APIKey:        testKey,
		CatalogDigest: testDigest,
		UserAgent:     "quantstore-cli/test",
		Timeout:       time.Second,
		HTTPClient:    &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}
