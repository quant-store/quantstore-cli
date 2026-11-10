package core

import (
	"context"
	"fmt"
	"io"

	"github.com/quant-store/quantstore-cli/core/internal/command"
	"github.com/quant-store/quantstore-cli/core/internal/productexec"
	"github.com/quant-store/quantstore-cli/productdrivers/builtin"
)

var builtinRegistry, builtinDriverError = productexec.NewRegistry(builtin.Drivers()...)

func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if builtinDriverError != nil {
		fmt.Fprintf(stderr, "error: invalid built-in product driver registry: %v\n", builtinDriverError)
		return 2
	}
	return command.Execute(ctx, command.Options{
		Args:     args,
		Stdin:    stdin,
		Stdout:   stdout,
		Stderr:   stderr,
		Products: builtinRegistry,
	})
}
