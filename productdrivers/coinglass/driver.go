package coinglass

import (
	"sort"

	"github.com/quant-store/quantstore-cli/driverapi"
)

const Slug = "coinglass"

type Driver struct {
	handlers map[string]driverapi.Handler
}

func New() *Driver {
	handlers := make(map[string]driverapi.Handler, len(operationIDs))
	for _, operationID := range operationIDs {
		handlers[operationID] = defaultHandler(operationID)
	}
	handlers["futures-price-history"] = futuresPriceHistoryHandler()
	handlers["futures-v2-taker-buy-sell-volume-history"] = futuresV2TakerBuySellVolumeHistoryHandler()
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
