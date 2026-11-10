package coinglass

import (
	"net/url"
	"strings"

	"github.com/quant-store/quantstore-cli/driverapi"
)

func canonicalizeQuery(query url.Values, key string, normalize func(string) string, warnings *[]driverapi.Warning) {
	values, exists := query[key]
	if !exists {
		return
	}
	for index, value := range values {
		canonical := normalize(value)
		if canonical == value {
			continue
		}
		values[index] = canonical
		*warnings = append(*warnings, driverapi.Warning{Code: "input_canonicalized", Field: "query." + key, From: value, To: canonical})
	}
}

func standardInterval(value string) (string, bool) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	aliases := map[string]string{
		"m1": "1m", "m5": "5m", "m15": "15m", "m30": "30m",
		"h1": "1h", "h4": "4h", "h12": "12h", "d1": "1d",
	}
	if replacement, exists := aliases[canonical]; exists {
		canonical = replacement
	}
	allowed := map[string]struct{}{"1m": {}, "5m": {}, "15m": {}, "30m": {}, "1h": {}, "4h": {}, "12h": {}, "1d": {}}
	_, exists := allowed[canonical]
	return canonical, exists
}

func v2Interval(value string) (string, bool) {
	canonical := strings.ToLower(strings.TrimSpace(value))
	aliases := map[string]string{
		"1m": "m1", "5m": "m5", "15m": "m15", "30m": "m30",
		"1h": "h1", "4h": "h4", "12h": "h12", "1d": "d1",
	}
	if replacement, exists := aliases[canonical]; exists {
		canonical = replacement
	}
	allowed := map[string]struct{}{"m1": {}, "m5": {}, "m15": {}, "m30": {}, "h1": {}, "h4": {}, "h12": {}, "d1": {}}
	_, exists := allowed[canonical]
	return canonical, exists
}
