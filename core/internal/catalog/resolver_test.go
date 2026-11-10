package catalog

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

func TestResolverFetchesParsesAndCachesExactCatalog(t *testing.T) {
	raw, digest := canonicalCatalog(t, "coinglass", "proxy")
	reference := validReference("coinglass", "proxy", strings.TrimPrefix(digest, "sha256:"))
	transport := &catalogTransport{reference: reference, catalog: raw}
	resolver, err := newResolver(t.TempDir(), newR2Fetcher(transport))
	if err != nil {
		t.Fatal(err)
	}

	first, err := resolver.Resolve(context.Background(), "coinglass")
	if err != nil {
		t.Fatal(err)
	}
	if first.Reference != reference || first.Catalog.CatalogID != reference.CatalogID || first.Catalog.Digest() != digest {
		t.Fatalf("unexpected result: %+v catalog=%+v", first.Reference, first.Catalog)
	}
	cachePath := resolver.catalogPath(digest)
	info, err := os.Stat(cachePath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("cache mode=%o", info.Mode().Perm())
	}

	second, err := resolver.Resolve(context.Background(), "coinglass")
	if err != nil {
		t.Fatal(err)
	}
	if second.Catalog.Digest() != digest || transport.referenceRequests != 2 || transport.catalogRequests != 1 {
		t.Fatalf("requests ref=%d catalog=%d", transport.referenceRequests, transport.catalogRequests)
	}
}

func TestResolverRepairsParseInvalidCacheFromExactObject(t *testing.T) {
	raw, digest := canonicalCatalog(t, "exchange-flow", "gateway")
	reference := validReference("exchange-flow", "gateway", strings.TrimPrefix(digest, "sha256:"))
	transport := &catalogTransport{reference: reference, catalog: raw}
	cacheRoot := t.TempDir()
	resolver, err := newResolver(cacheRoot, newR2Fetcher(transport))
	if err != nil {
		t.Fatal(err)
	}
	path := resolver.catalogPath(digest)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}

	resolved, err := resolver.Resolve(context.Background(), "exchange-flow")
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Catalog.Digest() != digest || transport.catalogRequests != 1 {
		t.Fatalf("resolved=%s catalog requests=%d", resolved.Catalog.Digest(), transport.catalogRequests)
	}
}

func TestResolverRejectsCatalogIdentityMismatch(t *testing.T) {
	raw, digest := canonicalCatalog(t, "arkham", "proxy")
	reference := validReference("coinglass", "proxy", strings.TrimPrefix(digest, "sha256:"))
	transport := &catalogTransport{reference: reference, catalog: raw}
	resolver, err := newResolver(t.TempDir(), newR2Fetcher(transport))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "coinglass"); !errors.Is(err, ErrCatalogIdentity) {
		t.Fatalf("error=%v", err)
	}
	if _, err := os.Stat(resolver.catalogPath(digest)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched catalog was cached: %v", err)
	}
}

func TestResolverRejectsNonCanonicalObjectEvenWhenRawDigestMatches(t *testing.T) {
	pretty := catalogFixture("coinglass", "proxy")
	sum := sha256.Sum256(pretty)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	reference := validReference("coinglass", "proxy", strings.TrimPrefix(digest, "sha256:"))
	transport := &catalogTransport{reference: reference, catalog: pretty}
	resolver, err := newResolver(t.TempDir(), newR2Fetcher(transport))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Resolve(context.Background(), "coinglass"); !errors.Is(err, ErrCatalogIdentity) {
		t.Fatalf("error=%v", err)
	}
}

func TestNewResolverRequiresAbsoluteCachePath(t *testing.T) {
	if _, err := NewResolver("relative/cache"); err == nil {
		t.Fatal("relative cache path accepted")
	}
	resolver, err := NewResolver(t.TempDir())
	if err != nil || resolver.cacheDir == "" {
		t.Fatalf("resolver=%+v err=%v", resolver, err)
	}
}

type catalogTransport struct {
	reference         Reference
	catalog           []byte
	referenceRequests int
	catalogRequests   int
}

func (transport *catalogTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if strings.HasPrefix(request.URL.Path, r2ReferencePathPrefix) {
		transport.referenceRequests++
		raw, err := json.Marshal(transport.reference)
		if err != nil {
			return nil, err
		}
		return jsonResponse(http.StatusOK, raw), nil
	}
	if strings.HasPrefix(request.URL.Path, r2CatalogPathPrefix) {
		transport.catalogRequests++
		return jsonResponse(http.StatusOK, transport.catalog), nil
	}
	return jsonResponse(http.StatusNotFound, []byte(`{}`)), nil
}

func canonicalCatalog(t *testing.T, slug, family string) ([]byte, string) {
	t.Helper()
	catalog, err := developercatalog.Parse(catalogFixture(slug, family))
	if err != nil {
		t.Fatal(err)
	}
	return catalog.CanonicalBytes(), catalog.Digest()
}

func catalogFixture(slug, family string) []byte {
	return []byte(`{
  "catalog_id": "product/` + slug + `/` + family + `",
  "product_slug": "` + slug + `",
  "schema_version": 2,
  "revision": 1,
  "min_cli_version": "0.1.0",
  "mode": "native",
  "scopes": [{"code":"data.read","description":"Read data"}],
  "operations": [{
    "operation_id": "status.get",
    "surface_id": "data-rest",
    "protocol": "rest",
    "method": "GET",
    "path_template": "/api/v1/status",
    "params": [],
    "side_effect": "none",
    "idempotent": true,
    "required_scope": "data.read"
  }]
}`)
}
