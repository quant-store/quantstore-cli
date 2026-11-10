package coinglass

import "github.com/quant-store/quantstore-cli/driverapi"

func futuresV2TakerBuySellVolumeHistoryHandler() driverapi.Handler {
	return intervalHandler("futures-v2-taker-buy-sell-volume-history", v2Interval)
}
