package redact

import (
	"net/url"
	"strings"
	"testing"
)

func TestStringAndURLRedactCredentials(t *testing.T) {
	key := "qs_live_" + strings.Repeat("A", 43)
	if got := String("failure for " + key); strings.Contains(got, key) || !strings.Contains(got, Replacement) {
		t.Fatalf("String() = %q", got)
	}
	parsed, err := url.Parse("https://user:pass@example.com/path?api_key=" + url.QueryEscape(key) + "&symbol=BTC")
	if err != nil {
		t.Fatal(err)
	}
	got := URL(parsed)
	if strings.Contains(got, "user") || strings.Contains(got, "pass") || strings.Contains(got, key) || !strings.Contains(got, "symbol=BTC") {
		t.Fatalf("URL() = %q", got)
	}
}

func TestHeaderNameIsCaseInsensitiveAndCoversQSHeaders(t *testing.T) {
	for _, name := range []string{"Authorization", "x-api-key", "Cookie", "X-QS-Catalog-Digest", "CG-API-KEY"} {
		if !HeaderName(name) {
			t.Fatalf("header %q not classified sensitive", name)
		}
	}
	if HeaderName("Content-Type") {
		t.Fatal("Content-Type unexpectedly classified sensitive")
	}
}

func TestContainsCredentialDetectsPatternAndExactSecret(t *testing.T) {
	key := "qs_live_" + strings.Repeat("Z", 43)
	if !ContainsCredential("prefix/"+key) || !ContainsCredential("opaque-secret", "opaque-secret") {
		t.Fatal("credential material was not detected")
	}
	if ContainsCredential("apk_0123456789abcdef0123") {
		t.Fatal("API key id was misclassified as plaintext credential")
	}
}
