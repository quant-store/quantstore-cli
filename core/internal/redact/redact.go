package redact

import (
	"net/url"
	"regexp"
	"strings"
)

const Replacement = "[REDACTED]"

var liveKeyPattern = regexp.MustCompile(`qs_live_[A-Za-z0-9_-]{43}`)
var unsafeControlPattern = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)

var sensitiveHeaders = map[string]struct{}{
	"authorization":       {},
	"proxy-authorization": {},
	"cookie":              {},
	"set-cookie":          {},
	"x-api-key":           {},
	"x-qs-key":            {},
	"cg-api-key":          {},
}

var sensitiveQuery = map[string]struct{}{
	"api_key":      {},
	"apikey":       {},
	"access_token": {},
	"token":        {},
	"key":          {},
}

func String(value string, secrets ...string) string {
	redacted := liveKeyPattern.ReplaceAllString(value, Replacement)
	for _, secret := range secrets {
		if secret != "" {
			redacted = strings.ReplaceAll(redacted, secret, Replacement)
		}
	}
	redacted = strings.NewReplacer("\r", "\\r", "\n", "\\n", "\t", "\\t").Replace(redacted)
	return unsafeControlPattern.ReplaceAllString(redacted, "?")
}

func ContainsCredential(value string, secrets ...string) bool {
	if liveKeyPattern.MatchString(value) {
		return true
	}
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func HeaderName(name string) bool {
	_, ok := sensitiveHeaders[strings.ToLower(strings.TrimSpace(name))]
	return ok || strings.HasPrefix(strings.ToLower(strings.TrimSpace(name)), "x-qs-")
}

func URL(value *url.URL) string {
	if value == nil {
		return ""
	}
	copyURL := *value
	copyURL.User = nil
	query := copyURL.Query()
	for key := range query {
		if _, ok := sensitiveQuery[strings.ToLower(key)]; ok {
			query.Set(key, Replacement)
		}
	}
	copyURL.RawQuery = query.Encode()
	return String(copyURL.String())
}
