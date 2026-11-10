package catalog

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

var ErrCatalogIdentity = errors.New("catalog identity does not match its reference")

// Resolved pins the validated reference and immutable parsed catalog used by
// one command invocation.
type Resolved struct {
	Reference Reference
	Catalog   *developercatalog.Catalog
}

type Resolver struct {
	cacheDir string
	fetcher  *R2Fetcher
}

// DefaultCacheDir returns the per-user, non-configuration storage root used
// for immutable catalog objects.
func DefaultCacheDir() (string, error) {
	root, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate user cache directory: %w", err)
	}
	return filepath.Join(root, "QuantStore", "qs", "catalogs"), nil
}

func NewResolver(cacheDir string) (*Resolver, error) {
	return newResolver(cacheDir, NewR2Fetcher())
}

func newResolver(cacheDir string, fetcher *R2Fetcher) (*Resolver, error) {
	if fetcher == nil {
		return nil, errors.New("catalog fetcher is required")
	}
	if cacheDir == "" {
		var err error
		cacheDir, err = DefaultCacheDir()
		if err != nil {
			return nil, err
		}
	}
	cacheDir = filepath.Clean(cacheDir)
	if cacheDir == "." || !filepath.IsAbs(cacheDir) {
		return nil, errors.New("catalog cache directory must be absolute")
	}
	return &Resolver{cacheDir: cacheDir, fetcher: fetcher}, nil
}

// Resolve always refreshes the small product reference, then resolves its
// exact immutable catalog from local storage or R2. A cached catalog with a
// different digest is never used as a fallback.
func (r *Resolver) Resolve(ctx context.Context, productSlug string) (Resolved, error) {
	reference, err := r.fetcher.fetchReference(ctx, productSlug)
	if err != nil {
		return Resolved{}, err
	}

	path := r.catalogPath(reference.CatalogDigest)
	if raw, err := readCacheFile(path); err == nil {
		catalog, validationErr := parseExactCatalog(raw, reference)
		if validationErr == nil {
			return Resolved{Reference: reference, Catalog: catalog}, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return Resolved{}, ErrCatalogRejected
	}

	raw, err := r.fetcher.Fetch(ctx, reference.CatalogDigest)
	if err != nil {
		return Resolved{}, err
	}
	catalog, err := parseExactCatalog(raw, reference)
	if err != nil {
		return Resolved{}, err
	}
	canonical := catalog.CanonicalBytes()
	if err := writeCacheFile(path, canonical); err != nil {
		return Resolved{}, fmt.Errorf("store catalog cache: %w", err)
	}
	return Resolved{Reference: reference, Catalog: catalog}, nil
}

func (r *Resolver) catalogPath(digest string) string {
	hexDigest := strings.TrimPrefix(digest, "sha256:")
	return filepath.Join(r.cacheDir, "v1", "sha256", hexDigest+".json")
}

func parseExactCatalog(raw []byte, reference Reference) (*developercatalog.Catalog, error) {
	catalog, err := developercatalog.Parse(raw)
	if err != nil {
		return nil, ErrCatalogRejected
	}
	if catalog.ProductSlug != reference.ProductSlug || catalog.CatalogID != reference.CatalogID || catalog.Digest() != reference.CatalogDigest {
		return nil, ErrCatalogIdentity
	}
	return catalog, nil
}

func readCacheFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() <= 0 || info.Size() > developercatalog.MaxCatalogBytes {
		return nil, ErrCatalogRejected
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	body, err := io.ReadAll(io.LimitReader(file, developercatalog.MaxCatalogBytes+1))
	if err != nil || len(body) == 0 || len(body) > developercatalog.MaxCatalogBytes {
		return nil, ErrCatalogRejected
	}
	return body, nil
}

func writeCacheFile(path string, data []byte) error {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".catalog-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		// Another resolver may have installed the same immutable bytes first.
		existing, readErr := os.ReadFile(path)
		if readErr == nil && bytes.Equal(existing, data) {
			return os.Chmod(path, 0o600)
		}
		return err
	}
	return os.Chmod(path, 0o600)
}
