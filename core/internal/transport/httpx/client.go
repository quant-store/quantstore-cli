package httpx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/quant-store/quantstore-cli/core/internal/redact"
)

const maxResponseBytes = 8 << 20

type Options struct {
	BaseURL    string
	APIKey     string
	UserAgent  string
	Timeout    time.Duration
	HTTPClient *http.Client
	MaxRetries int
}

type Client struct {
	base       *url.URL
	apiKey     string
	userAgent  string
	httpClient *http.Client
	maxRetries int
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

type APIError struct {
	StatusCode int    `json:"-"`
	Code       string `json:"code"`
	Message    string `json:"message"`
	RequestID  string `json:"request_id"`
}

func (e *APIError) Error() string {
	code := e.Code
	if code == "" {
		code = "http_" + strconv.Itoa(e.StatusCode)
	}
	message := e.Message
	if message == "" {
		message = http.StatusText(e.StatusCode)
	}
	result := code + ": " + message
	if e.RequestID != "" {
		result += " (request_id=" + e.RequestID + ")"
	}
	return redact.String(result)
}

type TransportError struct{ Err error }

func (e *TransportError) Error() string   { return "network request failed" }
func (e *TransportError) Unwrap() error   { return e.Err }
func (e *TransportError) Timeout() bool   { var n net.Error; return errors.As(e.Err, &n) && n.Timeout() }
func (e *TransportError) Temporary() bool { return true }

func New(options Options) (*Client, error) {
	base, err := validateBaseURL(options.BaseURL)
	if err != nil {
		return nil, err
	}
	if options.APIKey == "" {
		return nil, errors.New("API key is empty")
	}
	baseClient := options.HTTPClient
	if baseClient == nil {
		baseClient = http.DefaultClient
	}
	clientCopy := *baseClient
	clientCopy.Jar = nil
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	clientCopy.Timeout = options.Timeout
	if options.MaxRetries < 0 {
		options.MaxRetries = 0
	}
	return &Client{
		base:       base,
		apiKey:     options.APIKey,
		userAgent:  options.UserAgent,
		httpClient: &clientCopy,
		maxRetries: options.MaxRetries,
	}, nil
}

func (c *Client) DoJSON(ctx context.Context, method, path string, query url.Values, body any) (*Response, error) {
	if err := validateOpenPath(path, c.apiKey); err != nil {
		return nil, err
	}
	if err := validateQuery(query, c.apiKey); err != nil {
		return nil, err
	}
	var encodedBody []byte
	var err error
	if body != nil {
		encodedBody, err = json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("encode request body: %w", err)
		}
		if redact.ContainsCredential(string(encodedBody), c.apiKey) {
			return nil, errors.New("request body contains credential material")
		}
	}
	retries := 0
	if method == http.MethodGet || method == http.MethodHead {
		retries = c.maxRetries
	}
	for attempt := 0; ; attempt++ {
		response, retryAfter, retryable, err := c.attempt(ctx, method, path, query, encodedBody)
		if err == nil || attempt >= retries || !retryable {
			return response, err
		}
		if retryAfter <= 0 {
			retryAfter = 250 * time.Millisecond * time.Duration(1<<attempt)
		}
		if retryAfter > 5*time.Second {
			retryAfter = 5 * time.Second
		}
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, &TransportError{Err: ctx.Err()}
		case <-timer.C:
		}
	}
}

func (c *Client) attempt(ctx context.Context, method, path string, query url.Values, encodedBody []byte) (*Response, time.Duration, bool, error) {
	target := *c.base
	target.Path = path
	target.RawPath = ""
	target.RawQuery = query.Encode()
	var bodyReader io.Reader
	if encodedBody != nil {
		bodyReader = bytes.NewReader(encodedBody)
	}
	request, err := http.NewRequestWithContext(ctx, method, target.String(), bodyReader)
	if err != nil {
		return nil, 0, false, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	if encodedBody != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.userAgent != "" {
		request.Header.Set("User-Agent", c.userAgent)
	}
	// Credentials are attached last, after the destination has passed exact
	// origin/path validation. Callers cannot supply or override headers.
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	response, err := c.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, false, &TransportError{Err: ctx.Err()}
		}
		return nil, 0, true, &TransportError{Err: err}
	}
	defer response.Body.Close()
	if isRetryableStatus(response.StatusCode) {
		retryAfter := parseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		raw, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
		apiErr := apiErrorFromResponse(response.StatusCode, response.Header, raw)
		return nil, retryAfter, true, apiErr
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return nil, 0, false, &TransportError{Err: err}
	}
	if len(raw) > maxResponseBytes {
		return nil, 0, false, &APIError{StatusCode: http.StatusBadGateway, Code: "response_too_large", Message: "server response exceeded 8 MiB", RequestID: response.Header.Get("X-Request-Id")}
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		return nil, 0, false, &APIError{StatusCode: response.StatusCode, Code: "redirect_refused", Message: "server returned a redirect; credentials were not forwarded", RequestID: response.Header.Get("X-Request-Id")}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, 0, false, apiErrorFromResponse(response.StatusCode, response.Header, raw)
	}
	return &Response{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: raw}, 0, false, nil
}

func apiErrorFromResponse(status int, header http.Header, raw []byte) *APIError {
	apiErr := &APIError{StatusCode: status, RequestID: header.Get("X-Request-Id")}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, apiErr)
	}
	if apiErr.RequestID == "" {
		apiErr.RequestID = header.Get("X-Request-ID")
	}
	return apiErr
}

func validateBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, errors.New("invalid base URL syntax")
	}
	if !parsed.IsAbs() || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Opaque != "" {
		return nil, errors.New("base URL must be an absolute origin without credentials, path, query or fragment")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("base URL must not contain a path")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if parsed.Scheme != "https" {
		ip := net.ParseIP(hostname)
		if parsed.Scheme != "http" || (hostname != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return nil, errors.New("base URL must use HTTPS; HTTP is allowed only for localhost")
		}
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func validateOpenPath(path string, secrets ...string) error {
	if !strings.HasPrefix(path, "/api/v1/open/") {
		return errors.New("request path is outside the platform Open API")
	}
	if redact.ContainsCredential(path, secrets...) {
		return errors.New("request path contains credential material")
	}
	if strings.Contains(path, "\\") || strings.Contains(path, "//") || strings.Contains(path, "?") || strings.Contains(path, "#") {
		return errors.New("request path contains forbidden characters")
	}
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "/") || strings.Contains(decoded, "\\") {
			return errors.New("request path contains an unsafe segment")
		}
		if redact.ContainsCredential(decoded, secrets...) {
			return errors.New("request path contains credential material")
		}
	}
	return nil
}

func validateQuery(query url.Values, secrets ...string) error {
	for name, values := range query {
		if redact.ContainsCredential(name, secrets...) {
			return errors.New("request query contains credential material")
		}
		switch strings.ToLower(name) {
		case "api_key", "apikey", "access_token", "token", "key":
			return fmt.Errorf("credential-like query parameter %q is forbidden", name)
		}
		for _, value := range values {
			if redact.ContainsCredential(value, secrets...) {
				return errors.New("request query contains credential material")
			}
		}
	}
	return nil
}

func isRetryableStatus(status int) bool {
	return status == http.StatusTooManyRequests || status == http.StatusBadGateway || status == http.StatusServiceUnavailable || status == http.StatusGatewayTimeout
}

func parseRetryAfter(value string, now time.Time) time.Duration {
	if seconds, err := strconv.Atoi(strings.TrimSpace(value)); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil && when.After(now) {
		return when.Sub(now)
	}
	return 0
}
