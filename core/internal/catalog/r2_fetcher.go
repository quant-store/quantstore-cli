package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

const (
	r2CatalogOrigin     = "https://downloads.quant-store.com"
	r2CatalogPathPrefix = "/catalogs/v1/sha256/"
	r2CatalogTimeout    = 10 * time.Second
)

var (
	ErrDigestInvalid      = errors.New("catalog digest is invalid")
	ErrCatalogUnavailable = errors.New("catalog is unavailable")
	ErrCatalogRejected    = errors.New("catalog response was rejected")
)

// R2Fetcher retrieves one immutable catalog object from the compiled R2
// origin. It intentionally owns no cache, parser, resolver, or credentials.
type R2Fetcher struct {
	client *http.Client
}

// NewR2Fetcher constructs the production fetcher with a fixed origin and
// transport policy. Neither profile nor environment configuration can replace
// the catalog origin.
func NewR2Fetcher() *R2Fetcher {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableCompression = true
	return newR2Fetcher(transport)
}

// newR2Fetcher is an in-package test seam for the transport only. The origin,
// timeout, headers, redirect policy, and response limits remain compiled.
func newR2Fetcher(transport http.RoundTripper) *R2Fetcher {
	return &R2Fetcher{client: &http.Client{
		Transport: transport,
		Timeout:   r2CatalogTimeout,
		Jar:       nil,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}}
}

// Fetch returns the raw catalog bytes only after the exact HTTP and SHA-256
// contract succeeds. Catalog parsing and identity validation belong to the
// later B8R-RESOLVE batch.
func (f *R2Fetcher) Fetch(ctx context.Context, digest string) ([]byte, error) {
	if !validCatalogDigest(digest) {
		return nil, ErrDigestInvalid
	}
	if err := ctx.Err(); err != nil {
		return nil, unavailableWithContext(err)
	}

	hexDigest := strings.TrimPrefix(digest, "sha256:")
	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		r2CatalogOrigin+r2CatalogPathPrefix+hexDigest+".json",
		nil,
	)
	if err != nil {
		return nil, ErrCatalogUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")

	response, err := f.client.Do(request)
	if err != nil {
		return nil, classifyTransportError(ctx, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()

	if response.StatusCode != http.StatusOK {
		return nil, ErrCatalogUnavailable
	}
	if !validCatalogContentType(response.Header) || len(response.Header.Values("Content-Encoding")) != 0 {
		return nil, ErrCatalogRejected
	}

	maxBytes := int64(developercatalog.MaxCatalogBytes)
	if response.ContentLength == 0 || response.ContentLength < -1 || response.ContentLength > maxBytes {
		return nil, ErrCatalogRejected
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return nil, classifyBodyReadError(ctx, err)
	}
	if len(body) == 0 || int64(len(body)) > maxBytes {
		return nil, ErrCatalogRejected
	}
	if response.ContentLength >= 0 && response.ContentLength != int64(len(body)) {
		return nil, ErrCatalogRejected
	}

	sum := sha256.Sum256(body)
	actualDigest := "sha256:" + hex.EncodeToString(sum[:])
	if actualDigest != digest {
		return nil, ErrCatalogRejected
	}
	return body, nil
}

func validCatalogDigest(digest string) bool {
	if len(digest) != len("sha256:")+sha256.Size*2 || !strings.HasPrefix(digest, "sha256:") {
		return false
	}
	for _, character := range digest[len("sha256:"):] {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func validCatalogContentType(header http.Header) bool {
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
	if len(parameters) != 1 {
		return false
	}
	charset, ok := parameters["charset"]
	return ok && strings.EqualFold(charset, "utf-8")
}

func classifyTransportError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return unavailableWithContext(contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return unavailableWithContext(context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailableWithContext(context.DeadlineExceeded)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return unavailableWithContext(context.DeadlineExceeded)
	}
	return ErrCatalogUnavailable
}

func classifyBodyReadError(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return unavailableWithContext(contextErr)
	}
	if errors.Is(err, context.Canceled) {
		return unavailableWithContext(context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return unavailableWithContext(context.DeadlineExceeded)
	}
	var networkError net.Error
	if errors.As(err, &networkError) && networkError.Timeout() {
		return unavailableWithContext(context.DeadlineExceeded)
	}
	return ErrCatalogRejected
}

func unavailableWithContext(contextErr error) error {
	return errors.Join(ErrCatalogUnavailable, contextErr)
}
