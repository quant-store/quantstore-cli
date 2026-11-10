package catalog

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

func TestR2FetcherRejectsInvalidDigestBeforeNetwork(t *testing.T) {
	t.Parallel()

	calls := 0
	fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("transport must not be called")
	}))

	invalid := []string{
		"",
		strings.Repeat("0", 64),
		"sha256:" + strings.Repeat("0", 63),
		"sha256:" + strings.Repeat("0", 65),
		"sha256:" + strings.Repeat("A", 64),
		" sha256:" + strings.Repeat("0", 64),
		"sha256:" + strings.Repeat("0", 63) + "g",
		"sha256:" + strings.Repeat("0", 63) + "/",
		"sha256:" + strings.Repeat("0", 63) + "?",
		"sha256:" + strings.Repeat("0", 63) + "%",
		"sha256:" + strings.Repeat("0", 63) + "#",
		"sha256:" + strings.Repeat("0", 64) + "\n",
	}
	for _, digest := range invalid {
		if _, err := fetcher.Fetch(context.Background(), digest); !errors.Is(err, ErrDigestInvalid) {
			t.Fatalf("Fetch(%q) error = %v, want ErrDigestInvalid", digest, err)
		}
	}
	if calls != 0 {
		t.Fatalf("transport calls = %d, want 0", calls)
	}
}

func TestR2FetcherSendsExactCredentialFreeRequest(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"catalog":"ok"}`)
	digest := rawDigest(payload)
	var captured *http.Request
	fetcher := newR2Fetcher(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		captured = request.Clone(request.Context())
		captured.Header = request.Header.Clone()
		return successfulResponse(payload, "application/json", int64(len(payload))), nil
	}))

	got, err := fetcher.Fetch(context.Background(), digest)
	if err != nil {
		t.Fatalf("Fetch() error = %v", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("Fetch() body = %q, want %q", got, payload)
	}
	if captured == nil {
		t.Fatal("request was not captured")
	}
	if captured.Method != http.MethodGet {
		t.Fatalf("method = %q, want GET", captured.Method)
	}
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	wantURL := r2CatalogOrigin + r2CatalogPathPrefix + hexDigest + ".json"
	if captured.URL.String() != wantURL {
		t.Fatalf("URL = %q, want %q", captured.URL.String(), wantURL)
	}
	if captured.URL.RawQuery != "" || captured.URL.Fragment != "" {
		t.Fatalf("unexpected query/fragment: %q #%q", captured.URL.RawQuery, captured.URL.Fragment)
	}
	if got := captured.Header.Values("Accept"); len(got) != 1 || got[0] != "application/json" {
		t.Fatalf("Accept = %#v", got)
	}
	if got := captured.Header.Values("Accept-Encoding"); len(got) != 1 || got[0] != "identity" {
		t.Fatalf("Accept-Encoding = %#v", got)
	}
	for _, name := range []string{"Authorization", "Cookie", "X-API-Key", "Referer"} {
		if values := captured.Header.Values(name); len(values) != 0 {
			t.Fatalf("%s unexpectedly present: %#v", name, values)
		}
	}
	if cookies := captured.Cookies(); len(cookies) != 0 {
		t.Fatalf("cookies = %#v, want none", cookies)
	}
}

func TestR2FetcherProductionHTTPPolicy(t *testing.T) {
	t.Parallel()

	fetcher := NewR2Fetcher()
	if fetcher.client.Timeout != 10*time.Second {
		t.Fatalf("Timeout = %s, want 10s", fetcher.client.Timeout)
	}
	if fetcher.client.Jar != nil {
		t.Fatal("CookieJar must be nil")
	}
	transport, ok := fetcher.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport type = %T, want *http.Transport", fetcher.client.Transport)
	}
	if !transport.DisableCompression {
		t.Fatal("DisableCompression = false")
	}
	if transport == http.DefaultTransport {
		t.Fatal("production transport must clone http.DefaultTransport")
	}
	request, err := http.NewRequest(http.MethodGet, r2CatalogOrigin, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := fetcher.client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("CheckRedirect() error = %v, want http.ErrUseLastResponse", err)
	}
}

func TestR2FetcherContextAndTransportErrors(t *testing.T) {
	t.Parallel()

	t.Run("already canceled", func(t *testing.T) {
		calls := 0
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls++
			return nil, errors.New("must not be called")
		}))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := fetcher.Fetch(ctx, rawDigest([]byte("unused")))
		assertUnavailableContext(t, err, context.Canceled)
		if calls != 0 {
			t.Fatalf("transport calls = %d, want 0", calls)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		fetcher := newR2Fetcher(roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}))
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		_, err := fetcher.Fetch(ctx, rawDigest([]byte("unused")))
		assertUnavailableContext(t, err, context.DeadlineExceeded)
	})

	t.Run("transport timeout", func(t *testing.T) {
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, timeoutFailure("secret timeout detail")
		}))
		_, err := fetcher.Fetch(context.Background(), rawDigest([]byte("unused")))
		assertUnavailableContext(t, err, context.DeadlineExceeded)
		assertErrorOmits(t, err, "secret timeout detail")
	})

	t.Run("ordinary transport failure", func(t *testing.T) {
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("secret transport detail")
		}))
		_, err := fetcher.Fetch(context.Background(), rawDigest([]byte("unused")))
		if !errors.Is(err, ErrCatalogUnavailable) {
			t.Fatalf("error = %v, want ErrCatalogUnavailable", err)
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("ordinary transport error gained context sentinel: %v", err)
		}
		assertErrorOmits(t, err, "secret transport detail")
	})
}

func TestR2FetcherRejectsRedirectsWithoutFollowing(t *testing.T) {
	t.Parallel()

	for _, status := range []int{301, 302, 303, 307, 308} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			body := &trackingBody{reader: strings.NewReader("redirect secret")}
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{
					StatusCode:    status,
					Header:        http.Header{"Location": []string{r2CatalogOrigin + "/forbidden"}},
					Body:          body,
					ContentLength: int64(len("redirect secret")),
				}, nil
			}))
			_, err := fetcher.Fetch(context.Background(), rawDigest([]byte("unused")))
			if !errors.Is(err, ErrCatalogUnavailable) {
				t.Fatalf("error = %v, want ErrCatalogUnavailable", err)
			}
			if calls != 1 {
				t.Fatalf("transport calls = %d, want 1", calls)
			}
			if body.closeCount != 1 {
				t.Fatalf("body close count = %d, want 1", body.closeCount)
			}
			assertErrorOmits(t, err, r2CatalogOrigin, "redirect secret", "/forbidden")
		})
	}
}

func TestR2FetcherRejectsNonOKStatusAndClosesBody(t *testing.T) {
	t.Parallel()

	for _, status := range []int{204, 206, 404, 429, 500, 503} {
		status := status
		t.Run(http.StatusText(status), func(t *testing.T) {
			body := &trackingBody{reader: strings.NewReader("status body secret")}
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    status,
					Header:        http.Header{"Location": []string{"https://secret.invalid/path"}},
					Body:          body,
					ContentLength: int64(len("status body secret")),
				}, nil
			}))
			_, err := fetcher.Fetch(context.Background(), rawDigest([]byte("unused")))
			if !errors.Is(err, ErrCatalogUnavailable) {
				t.Fatalf("error = %v, want ErrCatalogUnavailable", err)
			}
			if body.closeCount != 1 {
				t.Fatalf("body close count = %d, want 1", body.closeCount)
			}
			assertErrorOmits(t, err, "status body secret", "secret.invalid")
		})
	}
}

func TestR2FetcherContentHeaders(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"ok":true}`)
	digest := rawDigest(payload)
	tests := []struct {
		name      string
		header    http.Header
		wantError bool
	}{
		{name: "plain json", header: http.Header{"Content-Type": []string{"application/json"}}},
		{name: "utf8 json", header: http.Header{"Content-Type": []string{"application/json; charset=utf-8"}}},
		{name: "missing content type", header: http.Header{}, wantError: true},
		{name: "wrong content type", header: http.Header{"Content-Type": []string{"text/plain"}}, wantError: true},
		{name: "duplicate content type", header: http.Header{"Content-Type": []string{"application/json", "application/json"}}, wantError: true},
		{name: "unknown parameter", header: http.Header{"Content-Type": []string{"application/json; profile=x"}}, wantError: true},
		{name: "non utf8", header: http.Header{"Content-Type": []string{"application/json; charset=iso-8859-1"}}, wantError: true},
		{name: "gzip", header: http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{"gzip"}}, wantError: true},
		{name: "empty encoding value", header: http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{""}}, wantError: true},
		{name: "second encoding value", header: http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{"", "gzip"}}, wantError: true},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			body := &trackingBody{reader: bytes.NewReader(payload)}
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Header:        test.header.Clone(),
					Body:          body,
					ContentLength: int64(len(payload)),
				}, nil
			}))
			got, err := fetcher.Fetch(context.Background(), digest)
			if test.wantError {
				if !errors.Is(err, ErrCatalogRejected) {
					t.Fatalf("error = %v, want ErrCatalogRejected", err)
				}
			} else {
				if err != nil {
					t.Fatalf("Fetch() error = %v", err)
				}
				if !bytes.Equal(got, payload) {
					t.Fatalf("body = %q, want %q", got, payload)
				}
			}
			if body.closeCount != 1 {
				t.Fatalf("body close count = %d, want 1", body.closeCount)
			}
		})
	}
}

func TestR2FetcherBodySizeBoundaries(t *testing.T) {
	t.Parallel()

	max := developercatalog.MaxCatalogBytes
	for _, size := range []int{max - 1, max} {
		size := size
		t.Run("accepted", func(t *testing.T) {
			payload := bytes.Repeat([]byte{'x'}, size)
			body := &trackingBody{reader: bytes.NewReader(payload)}
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return responseWithBody(http.StatusOK, jsonHeader(), int64(size), body), nil
			}))
			got, err := fetcher.Fetch(context.Background(), rawDigest(payload))
			if err != nil {
				t.Fatalf("Fetch(%d bytes) error = %v", size, err)
			}
			if len(got) != size {
				t.Fatalf("body length = %d, want %d", len(got), size)
			}
			if body.closeCount != 1 {
				t.Fatalf("body close count = %d, want 1", body.closeCount)
			}
		})
	}

	t.Run("unknown length accepted", func(t *testing.T) {
		payload := []byte(`{"unknown":true}`)
		body := &trackingBody{reader: bytes.NewReader(payload)}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), -1, body), nil
		}))
		if _, err := fetcher.Fetch(context.Background(), rawDigest(payload)); err != nil {
			t.Fatalf("Fetch() error = %v", err)
		}
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
	})

	t.Run("limit plus one rejected", func(t *testing.T) {
		payload := bytes.Repeat([]byte{'x'}, max+1)
		body := &trackingBody{reader: bytes.NewReader(payload)}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), -1, body), nil
		}))
		_, err := fetcher.Fetch(context.Background(), rawDigest(payload))
		if !errors.Is(err, ErrCatalogRejected) {
			t.Fatalf("error = %v, want ErrCatalogRejected", err)
		}
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
	})

	for _, test := range []struct {
		name           string
		payload        []byte
		declaredLength int64
	}{
		{name: "declared zero", payload: []byte("x"), declaredLength: 0},
		{name: "declared over limit", payload: []byte("x"), declaredLength: int64(max + 1)},
		{name: "declared too short", payload: []byte("value"), declaredLength: 4},
		{name: "declared too long", payload: []byte("value"), declaredLength: 6},
		{name: "empty actual body", payload: nil, declaredLength: -1},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			body := &trackingBody{reader: bytes.NewReader(test.payload)}
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return responseWithBody(http.StatusOK, jsonHeader(), test.declaredLength, body), nil
			}))
			_, err := fetcher.Fetch(context.Background(), rawDigest(test.payload))
			if !errors.Is(err, ErrCatalogRejected) {
				t.Fatalf("error = %v, want ErrCatalogRejected", err)
			}
			if body.closeCount != 1 {
				t.Fatalf("body close count = %d, want 1", body.closeCount)
			}
		})
	}
}

func TestR2FetcherRejectsReadAndDigestFailures(t *testing.T) {
	t.Parallel()

	t.Run("read failure", func(t *testing.T) {
		body := &trackingBody{reader: &failingReader{}}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), -1, body), nil
		}))
		_, err := fetcher.Fetch(context.Background(), rawDigest([]byte("x")))
		if !errors.Is(err, ErrCatalogRejected) {
			t.Fatalf("error = %v, want ErrCatalogRejected", err)
		}
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
		assertErrorOmits(t, err, "secret read failure")
	})

	t.Run("digest mismatch", func(t *testing.T) {
		payload := []byte(`{"actual":true}`)
		requested := rawDigest([]byte(`{"expected":true}`))
		body := &trackingBody{reader: bytes.NewReader(payload)}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), int64(len(payload)), body), nil
		}))
		_, err := fetcher.Fetch(context.Background(), requested)
		if !errors.Is(err, ErrCatalogRejected) {
			t.Fatalf("error = %v, want ErrCatalogRejected", err)
		}
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
		assertErrorOmits(t, err, requested, string(payload), r2CatalogOrigin)
	})
}

func TestR2FetcherPreservesContextErrorsDuringBodyRead(t *testing.T) {
	t.Parallel()

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		started := make(chan struct{})
		body := &trackingBody{reader: &contextBlockingReader{ctx: ctx, started: started}}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), -1, body), nil
		}))
		canceled := make(chan struct{})
		go func() {
			<-started
			cancel()
			close(canceled)
		}()

		_, err := fetcher.Fetch(ctx, rawDigest([]byte("unused")))
		<-canceled
		assertUnavailableContext(t, err, context.Canceled)
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
	})

	t.Run("caller deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		started := make(chan struct{})
		body := &trackingBody{reader: &contextBlockingReader{ctx: ctx, started: started}}
		fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
			return responseWithBody(http.StatusOK, jsonHeader(), -1, body), nil
		}))

		_, err := fetcher.Fetch(ctx, rawDigest([]byte("unused")))
		assertUnavailableContext(t, err, context.DeadlineExceeded)
		if body.closeCount != 1 {
			t.Fatalf("body close count = %d, want 1", body.closeCount)
		}
	})
}

func TestR2FetcherReturnsIndependentBuffers(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"independent":true}`)
	digest := rawDigest(payload)
	closes := make([]*trackingBody, 0, 2)
	fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		body := &trackingBody{reader: bytes.NewReader(payload)}
		closes = append(closes, body)
		return responseWithBody(http.StatusOK, jsonHeader(), int64(len(payload)), body), nil
	}))

	first, err := fetcher.Fetch(context.Background(), digest)
	if err != nil {
		t.Fatalf("first Fetch() error = %v", err)
	}
	second, err := fetcher.Fetch(context.Background(), digest)
	if err != nil {
		t.Fatalf("second Fetch() error = %v", err)
	}
	if &first[0] == &second[0] {
		t.Fatal("Fetch calls returned the same backing buffer")
	}
	wantSecond := append([]byte(nil), second...)
	first[0] ^= 0xff
	if !bytes.Equal(second, wantSecond) {
		t.Fatal("mutating first result changed second result")
	}
	for index, body := range closes {
		if body.closeCount != 1 {
			t.Fatalf("body %d close count = %d, want 1", index, body.closeCount)
		}
	}
}

func TestR2FetcherErrorsDoNotLeakRemoteDetails(t *testing.T) {
	t.Parallel()

	body := &trackingBody{reader: strings.NewReader("super secret remote body")}
	fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode:    http.StatusBadGateway,
			Header:        http.Header{"Location": []string{"https://secret.invalid/private"}},
			Body:          body,
			ContentLength: int64(len("super secret remote body")),
		}, nil
	}))
	digest := rawDigest([]byte("sensitive digest input"))
	_, err := fetcher.Fetch(context.Background(), digest)
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("error = %v, want ErrCatalogUnavailable", err)
	}
	assertErrorOmits(t, err, r2CatalogOrigin, digest, "super secret remote body", "secret.invalid", "Authorization", "Cookie")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

type trackingBody struct {
	reader     io.Reader
	closeCount int
}

func (body *trackingBody) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *trackingBody) Close() error {
	body.closeCount++
	return nil
}

type failingReader struct {
	read bool
}

type contextBlockingReader struct {
	ctx     context.Context
	started chan struct{}
	read    bool
}

func (reader *contextBlockingReader) Read([]byte) (int, error) {
	if !reader.read {
		reader.read = true
		close(reader.started)
	}
	<-reader.ctx.Done()
	return 0, reader.ctx.Err()
}

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if !reader.read {
		reader.read = true
		buffer[0] = 'x'
		return 1, nil
	}
	return 0, errors.New("secret read failure")
}

type timeoutFailure string

func (failure timeoutFailure) Error() string { return string(failure) }
func (timeoutFailure) Timeout() bool         { return true }
func (timeoutFailure) Temporary() bool       { return true }

func rawDigest(payload []byte) string {
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func jsonHeader() http.Header {
	return http.Header{"Content-Type": []string{"application/json"}}
}

func successfulResponse(payload []byte, contentType string, contentLength int64) *http.Response {
	header := http.Header{"Content-Type": []string{contentType}}
	return responseWithBody(http.StatusOK, header, contentLength, io.NopCloser(bytes.NewReader(payload)))
}

func responseWithBody(status int, header http.Header, contentLength int64, body io.ReadCloser) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        header,
		Body:          body,
		ContentLength: contentLength,
	}
}

func assertUnavailableContext(t *testing.T, err error, contextError error) {
	t.Helper()
	if !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("error = %v, want ErrCatalogUnavailable", err)
	}
	if !errors.Is(err, contextError) {
		t.Fatalf("error = %v, want context sentinel %v", err, contextError)
	}
}

func assertErrorOmits(t *testing.T, err error, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatalf("error %q leaked %q", err, secret)
		}
	}
}
