package producthttp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/quant-store/quantstore-cli/core/internal/redact"
	"github.com/quant-store/quantstore-cli/core/internal/transport/httpx"
)

const (
	maxResponseBytes = 8 << 20
	maxQueryBytes    = 64 << 10
	defaultTimeout   = 30 * time.Second
	maximumTimeout   = 5 * time.Minute
)

var (
	productHostPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?[.](?:proxy|gateway)[.]quant-store[.]com$`)
	digestPattern      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Client sends one already-resolved declarative request to a compiled,
// platform-owned product origin. It cannot follow redirects, carry cookies,
// retry, or send methods with a body.
type Client struct {
	origin     *url.URL
	apiKey     string
	digest     string
	userAgent  string
	httpClient *http.Client
}

type Options struct {
	Origin        string
	APIKey        string
	CatalogDigest string
	UserAgent     string
	Timeout       time.Duration
	HTTPClient    *http.Client
}

type Response struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func New(options Options) (*Client, error) {
	parsedOrigin, err := validateOrigin(options.Origin)
	if err != nil {
		return nil, err
	}
	if options.APIKey == "" || len(options.APIKey) > 4096 || containsControl(options.APIKey) {
		return nil, errors.New("product API key is invalid")
	}
	if !digestPattern.MatchString(options.CatalogDigest) {
		return nil, errors.New("catalog digest is invalid")
	}
	if len(options.UserAgent) > 512 || containsControl(options.UserAgent) {
		return nil, errors.New("user agent is invalid")
	}
	if options.Timeout <= 0 {
		options.Timeout = defaultTimeout
	}
	if options.Timeout > maximumTimeout {
		return nil, errors.New("product request timeout exceeds five minutes")
	}

	baseClient := options.HTTPClient
	if baseClient == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.DisableCompression = true
		baseClient = &http.Client{Transport: transport}
	}
	clientCopy := *baseClient
	clientCopy.Timeout = options.Timeout
	clientCopy.Jar = nil
	clientCopy.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}

	return &Client{
		origin:     parsedOrigin,
		apiKey:     options.APIKey,
		digest:     options.CatalogDigest,
		userAgent:  options.UserAgent,
		httpClient: &clientCopy,
	}, nil
}

func (client *Client) Do(ctx context.Context, method, path string, query url.Values) (*Response, error) {
	if client == nil || client.origin == nil || client.httpClient == nil {
		return nil, errors.New("product HTTP client is unavailable")
	}
	if method != http.MethodGet && method != http.MethodHead {
		return nil, errors.New("product HTTP method must be GET or HEAD")
	}
	decodedPath, err := validateEscapedPath(path)
	if err != nil {
		return nil, err
	}
	if redact.ContainsCredential(path, client.apiKey) || redact.ContainsCredential(decodedPath, client.apiKey) {
		return nil, errors.New("product request path contains credential material")
	}
	if err := validateQuery(query, client.apiKey); err != nil {
		return nil, err
	}

	target := *client.origin
	target.Path = decodedPath
	target.RawPath = path
	target.RawQuery = cloneQuery(query).Encode()
	request, err := http.NewRequestWithContext(ctx, method, target.String(), nil)
	if err != nil {
		return nil, errors.New("build product request failed")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	request.Header.Set("Authorization", "Bearer "+client.apiKey)
	request.Header.Set("X-QS-Catalog-Digest", client.digest)
	if client.userAgent != "" {
		request.Header.Set("User-Agent", client.userAgent)
	}

	response, err := client.httpClient.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &httpx.TransportError{Err: ctx.Err()}
		}
		return nil, &httpx.TransportError{Err: err}
	}
	if response.Body == nil {
		return nil, &httpx.TransportError{Err: errors.New("response body is unavailable")}
	}
	defer response.Body.Close()

	if response.ContentLength > maxResponseBytes {
		return nil, responseTooLarge(response)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		if ctx.Err() != nil {
			return nil, &httpx.TransportError{Err: ctx.Err()}
		}
		return nil, &httpx.TransportError{Err: err}
	}
	if len(raw) > maxResponseBytes {
		return nil, responseTooLarge(response)
	}
	if len(response.Header.Values("Content-Encoding")) != 0 {
		return nil, invalidResponse(response, "encoded product responses are not accepted")
	}
	if response.StatusCode >= 300 && response.StatusCode <= 399 {
		return nil, &httpx.APIError{
			StatusCode: response.StatusCode,
			Code:       "redirect_refused",
			Message:    "product returned a redirect; credentials were not forwarded",
			RequestID:  requestID(response.Header),
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, apiError(response, raw)
	}
	if method == http.MethodHead {
		if len(raw) != 0 {
			return nil, invalidResponse(response, "HEAD response contained a body")
		}
		return &Response{StatusCode: response.StatusCode, Header: response.Header.Clone()}, nil
	}
	if len(raw) == 0 || !validJSONContentType(response.Header) || !singleJSONValue(raw) {
		return nil, invalidResponse(response, "product returned an invalid JSON response")
	}
	return &Response{StatusCode: response.StatusCode, Header: response.Header.Clone(), Body: raw}, nil
}

func validateOrigin(raw string) (*url.URL, error) {
	if strings.TrimSpace(raw) != raw || raw == "" {
		return nil, errors.New("product origin is invalid")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || parsed.Path != "" || parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("product origin must be a canonical HTTPS origin")
	}
	hostname := parsed.Hostname()
	if parsed.Port() != "" || parsed.Host != hostname || net.ParseIP(hostname) != nil || !productHostPattern.MatchString(hostname) {
		return nil, errors.New("product origin is outside the platform product domains")
	}
	return parsed, nil
}

func validateEscapedPath(path string) (string, error) {
	if path == "" || path[0] != '/' || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\\?#") {
		return "", errors.New("product request path is invalid")
	}
	segments := strings.Split(path[1:], "/")
	for _, rawSegment := range segments {
		if rawSegment == "" {
			return "", errors.New("product request path is invalid")
		}
		for index := 0; index < len(rawSegment); index++ {
			if rawSegment[index] != '%' {
				if rawSegment[index] < 0x20 || rawSegment[index] == 0x7f {
					return "", errors.New("product request path is invalid")
				}
				continue
			}
			if index+2 >= len(rawSegment) || !upperHex(rawSegment[index+1]) || !upperHex(rawSegment[index+2]) {
				return "", errors.New("product request path is not canonically escaped")
			}
			index += 2
		}
		decoded, err := url.PathUnescape(rawSegment)
		if err != nil || decoded == "." || decoded == ".." || strings.ContainsAny(decoded, "/\\") || containsControl(decoded) {
			return "", errors.New("product request path contains an unsafe segment")
		}
	}
	decoded, err := url.PathUnescape(path)
	if err != nil {
		return "", errors.New("product request path is invalid")
	}
	return decoded, nil
}

func validateQuery(query url.Values, apiKey string) error {
	for name, values := range query {
		if name == "" || len(name) > maxQueryBytes || containsControl(name) || redact.ContainsCredential(name, apiKey) {
			return errors.New("product request query is invalid")
		}
		switch strings.ToLower(name) {
		case "api_key", "apikey", "access_token", "token", "key":
			return errors.New("credential-like product query parameters are forbidden")
		}
		if len(values) == 0 {
			return errors.New("product request query is invalid")
		}
		for _, value := range values {
			if len(value) > maxQueryBytes || containsControl(value) || redact.ContainsCredential(value, apiKey) {
				return errors.New("product request query is invalid")
			}
		}
	}
	return nil
}

func cloneQuery(query url.Values) url.Values {
	cloned := make(url.Values, len(query))
	for name, values := range query {
		cloned[name] = append([]string(nil), values...)
	}
	return cloned
}

func validJSONContentType(header http.Header) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	if len(parameters) == 0 {
		return true
	}
	return len(parameters) == 1 && strings.EqualFold(parameters["charset"], "utf-8")
}

func singleJSONValue(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func apiError(response *http.Response, raw []byte) *httpx.APIError {
	result := &httpx.APIError{StatusCode: response.StatusCode, RequestID: requestID(response.Header)}
	if len(raw) > 0 && validJSONContentType(response.Header) && singleJSONValue(raw) {
		var payload struct {
			Code      string `json:"code"`
			Message   string `json:"message"`
			RequestID string `json:"request_id"`
		}
		if json.Unmarshal(raw, &payload) == nil {
			result.Code = payload.Code
			result.Message = payload.Message
			if result.RequestID == "" {
				result.RequestID = payload.RequestID
			}
		}
	}
	return result
}

func responseTooLarge(response *http.Response) *httpx.APIError {
	return &httpx.APIError{
		StatusCode: http.StatusBadGateway,
		Code:       "response_too_large",
		Message:    "product response exceeded 8 MiB",
		RequestID:  requestID(response.Header),
	}
}

func invalidResponse(response *http.Response, message string) *httpx.APIError {
	return &httpx.APIError{
		StatusCode: http.StatusBadGateway,
		Code:       "product_response_invalid",
		Message:    message,
		RequestID:  requestID(response.Header),
	}
}

func requestID(header http.Header) string {
	if value := header.Get("X-Request-Id"); value != "" {
		return value
	}
	return header.Get("X-Request-ID")
}

func containsControl(value string) bool {
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return true
		}
	}
	return false
}

func upperHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'A' && value <= 'F'
}
