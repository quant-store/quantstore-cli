package smartmoneypro

import (
	"sort"

	"github.com/quant-store/quantstore-cli/driverapi"
)

const Slug = "smart-money-monitor-pro"

type Driver struct {
	handlers map[string]driverapi.Handler
}

func New(sources SourceCatalog) *Driver {
	handlers := make(map[string]driverapi.Handler)
	for _, operationID := range subjectOperationIDs {
		handlers[operationID] = subjectHandler(operationID, sources)
	}
	for _, operationID := range ordinaryOperationIDs {
		handlers[operationID] = defaultHandler(operationID)
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

var subjectOperationIDs = []string{
	ProfileGetOperation,
	"backtest.get",
	"aggregate.get",
	"account.get",
	"transfers.list",
	"pnl.get",
	"cex.performance",
	"cex.chart",
	"trading-stats.get",
	"pnl-trend.get",
	"trade-detail.get",
	"spot-trades.list",
	"pnl-split.get",
	"activity-timeline.list",
	"risk-metrics.get",
	"position-cost.get",
	"positions.history",
	"token-holdings.list",
	"staking.list",
}

var ordinaryOperationIDs = []string{
	"discovery.get",
	"positions.screen",
	"positions.search",
	"positions.by-coin",
	"positions.whales-by-coin",
	"positions.opens-by-coin",
	"positions.list",
	"market.ohlc",
	"dashboard.query",
	"dashboard.catalog",
	"twitter-profile.get",
	"twitter-recent.list",
	"stream.events",
	"mcp.whale-backtest",
	"mcp.whale-dashboard-catalog",
	"mcp.whale-dashboard-query",
	"mcp.whale-discovery",
	"mcp.whale-net-exposure",
	"mcp.whale-position-screen",
	"mcp.whale-profile",
}
