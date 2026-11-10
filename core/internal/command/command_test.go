package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/quant-store/quantstore-cli/core/internal/config"
	"github.com/quant-store/quantstore-cli/core/internal/secret"
)

type memorySecrets struct {
	mu      sync.Mutex
	value   string
	profile string
}

type backendFailureSecrets struct{}

func (backendFailureSecrets) Get(context.Context, string) (string, error) {
	return "", &secret.BackendError{Operation: "read from", Err: errors.New("backend down")}
}
func (backendFailureSecrets) Set(context.Context, string, string) error {
	return &secret.BackendError{Operation: "write to", Err: errors.New("backend down")}
}
func (backendFailureSecrets) Delete(context.Context, string) error {
	return &secret.BackendError{Operation: "delete from", Err: errors.New("backend down")}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("writer failed") }

func (store *memorySecrets) Get(context.Context, string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.value == "" {
		return "", secret.ErrNotFound
	}
	return store.value, nil
}

func (store *memorySecrets) Set(_ context.Context, profile, value string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.profile, store.value = profile, value
	return nil
}

func (store *memorySecrets) Delete(context.Context, string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.value == "" {
		return secret.ErrNotFound
	}
	store.value = ""
	return nil
}

func TestWalletBalanceCapturesExactWire(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("W", 43)
	var capturedMethod, capturedPath, capturedAuthorization, capturedQuery string
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		capturedMethod = request.Method
		capturedPath = request.URL.Path
		capturedQuery = request.URL.RawQuery
		capturedAuthorization = request.Header.Get("Authorization")
		if request.Header.Get("X-API-Key") != "" {
			t.Errorf("CLI sent X-API-Key")
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"available_cents":100,"frozen_cents":2,"currency":"USD"}`))
	}))
	defer server.Close()
	configPath := setupConfig(t, server.URL)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "wallet", "balance"},
		Stdin:       bytes.NewReader(nil),
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  configPath,
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if capturedMethod != http.MethodGet || capturedPath != "/api/v1/open/wallet/balance" {
		t.Fatalf("wire = %s %s", capturedMethod, capturedPath)
	}
	if capturedAuthorization != "Bearer "+key {
		t.Fatalf("Authorization = %q", capturedAuthorization)
	}
	if capturedQuery != "" || strings.Contains(stdout.String(), key) || strings.Contains(stderr.String(), key) {
		t.Fatalf("key leaked or unexpected query: query=%q stdout=%q stderr=%q", capturedQuery, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), `"available_cents": 100`) {
		t.Fatalf("stdout = %s", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %s", stderr.String())
	}
}

func TestOpenGetCommandsCaptureExactSiblingWires(t *testing.T) {
	apiKeyID := "apk_0123456789abcdef0123"
	orderID := "ord_0123456789abcdef0123"
	tests := []struct {
		name  string
		args  []string
		path  string
		query url.Values
	}{
		{name: "auth status", args: []string{"auth", "status"}, path: "/api/v1/open/me"},
		{name: "account show", args: []string{"account", "show"}, path: "/api/v1/open/profile"},
		{name: "wallet balance", args: []string{"wallet", "balance"}, path: "/api/v1/open/wallet/balance"},
		{name: "wallet ledger", args: []string{"wallet", "ledger", "--cursor", "cursor_1", "--limit", "10"}, path: "/api/v1/open/wallet/ledger", query: url.Values{"cursor": {"cursor_1"}, "limit": {"10"}}},
		{name: "subscriptions list", args: []string{"subscriptions", "list"}, path: "/api/v1/open/subscriptions"},
		{name: "subscriptions show", args: []string{"subscriptions", "show", "coinglass"}, path: "/api/v1/open/subscriptions/coinglass"},
		{name: "orders list", args: []string{"orders", "list", "--cursor", "cursor_2", "--limit", "11", "--status", "paid"}, path: "/api/v1/open/orders", query: url.Values{"cursor": {"cursor_2"}, "limit": {"11"}, "status": {"paid"}}},
		{name: "orders show", args: []string{"orders", "show", orderID}, path: "/api/v1/open/orders/" + orderID},
		{name: "usage summary", args: []string{"usage", "summary", "--api-key-id", apiKeyID, "--surface", "data_api", "--product", "coinglass", "--since", "2026-07-01T00:00:00Z", "--until", "2026-07-02T00:00:00Z"}, path: "/api/v1/open/usage/summary", query: url.Values{"api_key_id": {apiKeyID}, "surface": {"data_api"}, "product_slug": {"coinglass"}, "since": {"2026-07-01T00:00:00Z"}, "until": {"2026-07-02T00:00:00Z"}}},
		{name: "usage calls", args: []string{"usage", "calls", "--api-key-id", apiKeyID, "--surface", "account_api", "--cursor", "cursor_3", "--limit", "12"}, path: "/api/v1/open/usage/calls", query: url.Values{"api_key_id": {apiKeyID}, "surface": {"account_api"}, "cursor": {"cursor_3"}, "limit": {"12"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearAPIKeyEnvironment(t)
			key := "qs_live_" + strings.Repeat("T", 43)
			var method, path, query, authorization, apiKeyHeader string
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				method = request.Method
				path = request.URL.Path
				query = request.URL.RawQuery
				authorization = request.Header.Get("Authorization")
				apiKeyHeader = request.Header.Get("X-API-Key")
				_, _ = writer.Write([]byte(`{}`))
			}))
			defer server.Close()
			var stdout, stderr bytes.Buffer
			args := append([]string{"--output", "json"}, test.args...)
			code := Execute(context.Background(), Options{
				Args:        args,
				Stdout:      &stdout,
				Stderr:      &stderr,
				ConfigPath:  setupConfig(t, server.URL),
				SecretStore: &memorySecrets{value: key},
				HTTPClient:  server.Client(),
				MaxRetries:  -1,
			})
			if code != 0 {
				t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
			}
			if method != http.MethodGet || path != test.path || query != test.query.Encode() {
				t.Fatalf("wire = %s %s?%s, want GET %s?%s", method, path, query, test.path, test.query.Encode())
			}
			if authorization != "Bearer "+key || apiKeyHeader != "" {
				t.Fatalf("auth headers: Authorization=%q X-API-Key=%q", authorization, apiKeyHeader)
			}
			if strings.Contains(path+"?"+query+stdout.String()+stderr.String(), key) {
				t.Fatal("API key leaked outside Authorization")
			}
		})
	}
}

func TestAccountUpdateSendsExactPatchBody(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("P", 43)
	var captured map[string]any
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPatch || request.URL.Path != "/api/v1/open/profile" {
			t.Errorf("wire = %s %s", request.Method, request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer "+key {
			t.Errorf("Authorization mismatch")
		}
		decoder := json.NewDecoder(request.Body)
		if err := decoder.Decode(&captured); err != nil {
			t.Errorf("decode body: %v", err)
		}
		_, _ = writer.Write([]byte(`{"locale":"en-US","username":"alice","updated_at":"2026-07-12T00:00:00Z"}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "account", "update", "--locale", "en-US"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, server.URL),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if len(captured) != 1 || captured["locale"] != "en-US" {
		t.Fatalf("captured body = %#v", captured)
	}
}

func TestAuthSetNeverPersistsOrPrintsPlaintextKey(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("K", 43)
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "config.json")
	secrets := &memorySecrets{}
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "auth", "set", "--stdin"},
		Stdin:       strings.NewReader(key + "\n"),
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  configPath,
		SecretStore: secrets,
		MaxRetries:  -1,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if secrets.value != key || secrets.profile != config.DefaultProfile {
		t.Fatalf("keychain fake did not receive exact profile/key")
	}
	if strings.Contains(stdout.String(), key) || strings.Contains(stderr.String(), key) {
		t.Fatalf("key leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := assertDirectoryDoesNotContain(t, root, key); err != nil {
		t.Fatal(err)
	}
}

func TestProfileSetRejectsCredentialInBaseURLWithoutDiskLeak(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("B", 43)
	root := t.TempDir()
	configPath := filepath.Join(root, "config", "config.json")
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "profile", "set", "unsafe", "--base-url", "https://" + key + ".example.com"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  configPath,
		SecretStore: &memorySecrets{},
		MaxRetries:  -1,
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	if strings.Contains(stdout.String()+stderr.String(), key) {
		t.Fatalf("key leaked to output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if err := assertDirectoryDoesNotContain(t, root, key); err != nil {
		t.Fatal(err)
	}
}

func TestUnauthorizedMapsExitCodeAndRedactsError(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("U", 43)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("X-Request-Id", "req_auth_1")
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"code":"auth_unauthorized","message":"invalid ` + key + `"}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "auth", "status"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, server.URL),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 3 {
		t.Fatalf("exit = %d, want 3; stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 || strings.Contains(stderr.String(), key) || strings.Contains(stderr.String(), "qs_live_") {
		t.Fatalf("unsafe output: stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "req_auth_1") || !strings.Contains(stderr.String(), "[REDACTED]") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestPartialSubscriptionResponseWarnsOnStderr(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("S", 43)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"items":[],"partial":true,"unavailable_products":[{"product_slug":"twitter-monitor","code":"source_unavailable"}]}`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "subscriptions", "list"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, server.URL),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "response is partial") || !strings.Contains(stderr.String(), "twitter-monitor") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `"partial": true`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestKeychainBackendFailureIsLocalExitCode(t *testing.T) {
	clearAPIKeyEnvironment(t)
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"auth", "status"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  filepath.Join(t.TempDir(), "config.json"),
		SecretStore: backendFailureSecrets{},
		MaxRetries:  -1,
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "backend unavailable") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestValidResponseWriterFailureIsLocalExitCode(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("I", 43)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`{"available_cents":1}`))
	}))
	defer server.Close()
	var stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "wallet", "balance"},
		Stdout:      failingWriter{},
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, server.URL),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 2 {
		t.Fatalf("exit = %d, want 2; stderr=%s", code, stderr.String())
	}
}

func TestInvalidSuccessPayloadIsRemoteExitCode(t *testing.T) {
	clearAPIKeyEnvironment(t)
	key := "qs_live_" + strings.Repeat("J", 43)
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte(`not-json`))
	}))
	defer server.Close()
	var stdout, stderr bytes.Buffer
	code := Execute(context.Background(), Options{
		Args:        []string{"--output", "json", "wallet", "balance"},
		Stdout:      &stdout,
		Stderr:      &stderr,
		ConfigPath:  setupConfig(t, server.URL),
		SecretStore: &memorySecrets{value: key},
		HTTPClient:  server.Client(),
		MaxRetries:  -1,
	})
	if code != 8 {
		t.Fatalf("exit = %d, want 8; stderr=%s", code, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("invalid response reached stdout: %q", stdout.String())
	}
}

func setupConfig(t *testing.T, baseURL string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.json")
	store := config.NewStore(path)
	if err := store.Update(context.Background(), func(file *config.File) error {
		profile := file.Profiles[config.DefaultProfile]
		profile.BaseURL = baseURL
		profile.Output = "json"
		file.Profiles[config.DefaultProfile] = profile
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return path
}

func clearAPIKeyEnvironment(t *testing.T) {
	t.Helper()
	old, existed := os.LookupEnv(secret.EnvironmentVariable)
	if err := os.Unsetenv(secret.EnvironmentVariable); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(secret.EnvironmentVariable, old)
		} else {
			_ = os.Unsetenv(secret.EnvironmentVariable)
		}
	})
}

func assertDirectoryDoesNotContain(t *testing.T, root, value string) error {
	t.Helper()
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil || entry.IsDir() {
			return walkErr
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(raw, []byte(value)) {
			return errors.New("sensitive value found on disk at " + path)
		}
		return nil
	})
}
