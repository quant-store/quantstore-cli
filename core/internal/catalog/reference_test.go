package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchReferenceUsesFixedR2PathAndNoCredentials(t *testing.T) {
	reference := validReference("twitter-monitor", "gateway", strings.Repeat("a", 64))
	raw, err := json.Marshal(reference)
	if err != nil {
		t.Fatal(err)
	}
	requests := 0
	fetcher := newR2Fetcher(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method != http.MethodGet || request.URL.String() != "https://downloads.quant-store.com/catalogs/v1/products/twitter-monitor.json" {
			t.Fatalf("unexpected request %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Accept") != "application/json" || request.Header.Get("Accept-Encoding") != "identity" {
			t.Fatalf("unexpected headers: %v", request.Header)
		}
		for _, forbidden := range []string{"Authorization", "Cookie", "Referer", "X-QS-Api-Key"} {
			if request.Header.Get(forbidden) != "" {
				t.Fatalf("credential-bearing header %s was sent", forbidden)
			}
		}
		return jsonResponse(http.StatusOK, raw), nil
	}))

	got, err := fetcher.fetchReference(context.Background(), "twitter-monitor")
	if err != nil {
		t.Fatal(err)
	}
	if got != reference || requests != 1 {
		t.Fatalf("got %+v requests=%d", got, requests)
	}
}

func TestFetchReferenceRejectsInvalidSlugBeforeNetwork(t *testing.T) {
	requests := 0
	fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		return nil, errors.New("must not run")
	}))
	for _, slug := range []string{"", "a", "Twitter", "twitter_monitor", "-twitter", "twitter/monitor", strings.Repeat("a", 65)} {
		if _, err := fetcher.fetchReference(context.Background(), slug); !errors.Is(err, ErrReferenceInvalid) {
			t.Fatalf("slug %q error=%v", slug, err)
		}
	}
	if requests != 0 {
		t.Fatalf("transport called %d times", requests)
	}
}

func TestDecodeAndValidateReferenceStrict(t *testing.T) {
	digest := "sha256:" + strings.Repeat("b", 64)
	valid := validReference("coinglass", "proxy", strings.Repeat("b", 64))
	validRaw, _ := json.Marshal(valid)
	if decoded, err := decodeReference(validRaw); err != nil || validateReference(decoded, "coinglass") != nil {
		t.Fatalf("valid reference rejected: %+v %v", decoded, err)
	}

	tests := map[string]string{
		"unknown field":    strings.TrimSuffix(string(validRaw), "}") + `,"extra":true}`,
		"duplicate field":  strings.Replace(string(validRaw), `"schema_version":1`, `"schema_version":1,"schema_version":1`, 1),
		"missing field":    `{"schema_version":1,"product_slug":"coinglass","catalog_id":"product/coinglass/proxy","catalog_digest":"` + digest + `"}`,
		"trailing value":   string(validRaw) + `{}`,
		"wrong field type": strings.Replace(string(validRaw), `"schema_version":1`, `"schema_version":"1"`, 1),
		"invalid utf8":     string(append(append([]byte(nil), validRaw...), 0xff)),
		"non-object":       `[]`,
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeReference([]byte(raw)); !errors.Is(err, ErrReferenceInvalid) {
				t.Fatalf("error=%v", err)
			}
		})
	}

	invalidValues := map[string]Reference{
		"schema":       withReference(valid, func(r *Reference) { r.SchemaVersion = 2 }),
		"slug":         withReference(valid, func(r *Reference) { r.ProductSlug = "arkham" }),
		"catalog slug": withReference(valid, func(r *Reference) { r.CatalogID = "product/arkham/proxy" }),
		"family":       withReference(valid, func(r *Reference) { r.CatalogID = "product/coinglass/control" }),
		"digest":       withReference(valid, func(r *Reference) { r.CatalogDigest = strings.Repeat("b", 64) }),
		"origin host":  withReference(valid, func(r *Reference) { r.HTTPSOrigin = "https://evil.invalid" }),
		"origin path":  withReference(valid, func(r *Reference) { r.HTTPSOrigin += "/api" }),
	}
	for name, reference := range invalidValues {
		t.Run(name, func(t *testing.T) {
			if !errors.Is(validateReference(reference, "coinglass"), ErrReferenceInvalid) {
				t.Fatalf("reference accepted: %+v", reference)
			}
		})
	}
}

func TestFetchReferenceRejectsResponseEnvelope(t *testing.T) {
	valid := validReference("coinglass", "proxy", strings.Repeat("c", 64))
	raw, _ := json.Marshal(valid)
	tests := map[string]func() *http.Response{
		"status": func() *http.Response { return jsonResponse(http.StatusNotFound, raw) },
		"content type": func() *http.Response {
			response := jsonResponse(http.StatusOK, raw)
			response.Header.Set("Content-Type", "text/plain")
			return response
		},
		"content encoding": func() *http.Response {
			response := jsonResponse(http.StatusOK, raw)
			response.Header.Set("Content-Encoding", "gzip")
			return response
		},
		"oversize": func() *http.Response {
			body := []byte(strings.Repeat("x", maxReferenceBytes+1))
			return jsonResponse(http.StatusOK, body)
		},
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			fetcher := newR2Fetcher(roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(), nil
			}))
			if _, err := fetcher.fetchReference(context.Background(), "coinglass"); err == nil {
				t.Fatal("response accepted")
			}
		})
	}
}

func validReference(slug, family, hexDigest string) Reference {
	return Reference{
		SchemaVersion: 1,
		ProductSlug:   slug,
		CatalogID:     "product/" + slug + "/" + family,
		CatalogDigest: "sha256:" + hexDigest,
		HTTPSOrigin:   "https://" + slug + "." + family + ".quant-store.com",
	}
}

func withReference(reference Reference, mutate func(*Reference)) Reference {
	mutate(&reference)
	return reference
}

func jsonResponse(status int, body []byte) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": {"application/json"}},
		Body:          io.NopCloser(strings.NewReader(string(body))),
		ContentLength: int64(len(body)),
	}
}
