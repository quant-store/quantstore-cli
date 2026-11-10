package builtin

import (
	"github.com/quant-store/quantstore-cli/driverapi"
	"github.com/quant-store/quantstore-cli/productdrivers/coinglass"
	"github.com/quant-store/quantstore-cli/productdrivers/smartmoneypro"
	"github.com/quant-store/quantstore-cli/productdrivers/twittermonitor"
)

// Drivers returns the product-specific drivers compiled into this CLI build.
// Runtime endpoint discovery remains owned by core and is deliberately absent here.
func Drivers() []driverapi.Driver {
	return []driverapi.Driver{
		coinglass.New(),
		twittermonitor.New(),
		smartmoneypro.New(nil),
	}
}
