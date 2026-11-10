package twittermonitor

import (
	"sort"

	"github.com/quant-store/quantstore-cli/driverapi"
)

const Slug = "twitter-monitor"

type Driver struct {
	handlers map[string]driverapi.Handler
}

func New() *Driver {
	handlers := map[string]driverapi.Handler{
		"tweets.search":          tweetsSearchHandler(),
		"handles.resolve":        handleResolveHandler(),
		"handles.profile":        handleProfileHandler(),
		"seats.list":             defaultHandler("seats.list"),
		"rules.list":             defaultHandler("rules.list"),
		"points.get":             defaultHandler("points.get"),
		"stats.get":              defaultHandler("stats.get"),
		"push-overview.get":      defaultHandler("push-overview.get"),
		"push-history.list":      pushHistoryHandler(),
		"stream.events":          defaultHandler("stream.events"),
		"mcp.get-tweets":         tweetsSearchHandlerWithID("mcp.get-tweets"),
		"mcp.resolve-handle":     handleBodyHandler("mcp.resolve-handle"),
		"mcp.get-handle-profile": handleBodyHandler("mcp.get-handle-profile"),
		"mcp.get-seats":          defaultHandler("mcp.get-seats"),
		"mcp.get-rules":          defaultHandler("mcp.get-rules"),
		"mcp.get-points":         defaultHandler("mcp.get-points"),
		"mcp.get-stats":          defaultHandler("mcp.get-stats"),
		"mcp.get-push-overview":  defaultHandler("mcp.get-push-overview"),
		"mcp.get-push-history":   defaultHandler("mcp.get-push-history"),
	}
	return &Driver{handlers: handlers}
}

func (*Driver) Slug() string {
	return Slug
}

func (driver *Driver) OperationIDs() []string {
	result := make([]string, 0, len(driver.handlers))
	for operationID := range driver.handlers {
		result = append(result, operationID)
	}
	sort.Strings(result)
	return result
}

func (driver *Driver) Handler(operationID string) (driverapi.Handler, bool) {
	handler, exists := driver.handlers[operationID]
	return handler, exists
}
