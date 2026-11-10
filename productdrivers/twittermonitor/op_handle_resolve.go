package twittermonitor

import "github.com/quant-store/quantstore-cli/driverapi"

func handleResolveHandler() driverapi.Handler {
	return handleBodyHandler("handles.resolve")
}
