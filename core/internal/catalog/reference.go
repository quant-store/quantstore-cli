package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	r2ReferencePathPrefix = "/catalogs/v1/products/"
	maxReferenceBytes     = 16 << 10
)

var (
	ErrReferenceInvalid = errors.New("catalog reference is invalid")
	productSlugPattern  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,63}$`)
)

// Reference is the mutable, product-scoped pointer to one immutable catalog
// object. HTTPSOrigin is constrained by CatalogID and cannot redirect a
// credential-bearing request to an arbitrary host.
type Reference struct {
	SchemaVersion int    `json:"schema_version"`
	ProductSlug   string `json:"product_slug"`
	CatalogID     string `json:"catalog_id"`
	CatalogDigest string `json:"catalog_digest"`
	HTTPSOrigin   string `json:"https_origin"`
}

func (f *R2Fetcher) fetchReference(ctx context.Context, productSlug string) (Reference, error) {
	if !productSlugPattern.MatchString(productSlug) {
		return Reference{}, ErrReferenceInvalid
	}
	if err := ctx.Err(); err != nil {
		return Reference{}, unavailableWithContext(err)
	}

	request, err := http.NewRequestWithContext(
		ctx,
		http.MethodGet,
		r2CatalogOrigin+r2ReferencePathPrefix+productSlug+".json",
		nil,
	)
	if err != nil {
		return Reference{}, ErrCatalogUnavailable
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")

	response, err := f.client.Do(request)
	if err != nil {
		return Reference{}, classifyTransportError(ctx, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return Reference{}, ErrCatalogUnavailable
	}
	if !validCatalogContentType(response.Header) || len(response.Header.Values("Content-Encoding")) != 0 {
		return Reference{}, ErrCatalogRejected
	}
	if response.ContentLength == 0 || response.ContentLength < -1 || response.ContentLength > maxReferenceBytes {
		return Reference{}, ErrCatalogRejected
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, maxReferenceBytes+1))
	if err != nil {
		return Reference{}, classifyBodyReadError(ctx, err)
	}
	if len(body) == 0 || len(body) > maxReferenceBytes {
		return Reference{}, ErrCatalogRejected
	}
	if response.ContentLength >= 0 && response.ContentLength != int64(len(body)) {
		return Reference{}, ErrCatalogRejected
	}

	reference, err := decodeReference(body)
	if err != nil || validateReference(reference, productSlug) != nil {
		return Reference{}, ErrReferenceInvalid
	}
	return reference, nil
}

func decodeReference(raw []byte) (Reference, error) {
	var reference Reference
	if !utf8.Valid(raw) {
		return reference, ErrReferenceInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return reference, ErrReferenceInvalid
	}

	seen := make(map[string]struct{}, 5)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return Reference{}, ErrReferenceInvalid
		}
		name, ok := token.(string)
		if !ok {
			return Reference{}, ErrReferenceInvalid
		}
		if _, duplicate := seen[name]; duplicate {
			return Reference{}, ErrReferenceInvalid
		}
		seen[name] = struct{}{}
		switch name {
		case "schema_version":
			err = decoder.Decode(&reference.SchemaVersion)
		case "product_slug":
			err = decoder.Decode(&reference.ProductSlug)
		case "catalog_id":
			err = decoder.Decode(&reference.CatalogID)
		case "catalog_digest":
			err = decoder.Decode(&reference.CatalogDigest)
		case "https_origin":
			err = decoder.Decode(&reference.HTTPSOrigin)
		default:
			return Reference{}, ErrReferenceInvalid
		}
		if err != nil {
			return Reference{}, ErrReferenceInvalid
		}
	}
	end, err := decoder.Token()
	if err != nil || end != json.Delim('}') {
		return Reference{}, ErrReferenceInvalid
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return Reference{}, ErrReferenceInvalid
	}
	if len(seen) != 5 {
		return Reference{}, ErrReferenceInvalid
	}
	return reference, nil
}

func validateReference(reference Reference, expectedSlug string) error {
	if reference.SchemaVersion != 1 || reference.ProductSlug != expectedSlug || !productSlugPattern.MatchString(reference.ProductSlug) {
		return ErrReferenceInvalid
	}
	if !validCatalogDigest(reference.CatalogDigest) {
		return ErrReferenceInvalid
	}

	prefix := "product/" + expectedSlug + "/"
	if !strings.HasPrefix(reference.CatalogID, prefix) {
		return ErrReferenceInvalid
	}
	family := strings.TrimPrefix(reference.CatalogID, prefix)
	if family != "proxy" && family != "gateway" {
		return ErrReferenceInvalid
	}
	wantOrigin := "https://" + expectedSlug + "." + family + ".quant-store.com"
	if reference.CatalogID != prefix+family || reference.HTTPSOrigin != wantOrigin {
		return ErrReferenceInvalid
	}
	return nil
}
