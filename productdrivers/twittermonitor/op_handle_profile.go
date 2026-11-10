package twittermonitor

import "github.com/quant-store/quantstore-cli/driverapi"

func handleProfileHandler() driverapi.Handler {
	return handleBodyHandler("handles.profile")
}
