package command

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/spf13/cobra"

	clientcatalog "github.com/quant-store/quantstore-cli/core/internal/catalog"
	"github.com/quant-store/quantstore-cli/core/internal/exitcode"
	"github.com/quant-store/quantstore-cli/core/internal/output"
	"github.com/quant-store/quantstore-cli/core/internal/productexec"
	"github.com/quant-store/quantstore-cli/core/internal/transport/producthttp"
	"github.com/quant-store/quantstore-cli/core/internal/version"
	"github.com/quant-store/quantstore-cli/internal/developercatalog"
)

func (a *application) productCommand() *cobra.Command {
	product := &cobra.Command{
		Use:   "product",
		Short: "Call catalog-approved product operations",
		Args:  cobra.NoArgs,
	}
	product.AddCommand(a.productRequestCommand())
	return product
}

func (a *application) productRequestCommand() *cobra.Command {
	var pathFlags []string
	var queryFlags []string
	command := &cobra.Command{
		Use:   "request <product-slug> <operation-id>",
		Short: "Call a catalog-approved product GET or HEAD operation",
		Args:  cobra.ExactArgs(2),
		RunE: func(command *cobra.Command, args []string) error {
			pathValues, err := parseAssignments(pathFlags, false)
			if err != nil {
				return exitcode.New(exitcode.Local, "invalid --path: "+err.Error())
			}
			queryValues, err := parseAssignments(queryFlags, true)
			if err != nil {
				return exitcode.New(exitcode.Local, "invalid --query: "+err.Error())
			}
			return a.requestProduct(command.Context(), args[0], args[1], pathValues, queryValues)
		},
	}
	command.Flags().StringArrayVar(&pathFlags, "path", nil, "path parameter as name=value (repeatable)")
	command.Flags().StringArrayVar(&queryFlags, "query", nil, "query parameter as name=value (repeatable)")
	return command
}

func (a *application) requestProduct(ctx context.Context, productSlug, operationID string, pathValues, queryValues map[string][]string) error {
	profile, err := a.resolve(ctx, true)
	if err != nil {
		return err
	}
	resolver := a.catalogs
	if resolver == nil {
		resolver, err = clientcatalog.NewResolver(a.catalogDir)
		if err != nil {
			return exitcode.New(exitcode.Local, err.Error())
		}
	}
	resolved, err := resolver.Resolve(ctx, productSlug)
	if err != nil {
		return catalogCommandError(err)
	}
	var (
		operation   *developercatalog.Operation
		escapedPath string
		query       url.Values
	)
	switch operationExecution(resolved.Catalog, operationID) {
	case developercatalog.CLIExecutionDeclarative:
		operation, escapedPath, query, err = productexec.PrepareDeclarative(resolved.Catalog, operationID, pathValues, queryValues)
	case developercatalog.CLIExecutionDriverRequired:
		var warnings []driverWarning
		operation, escapedPath, query, warnings, err = prepareDriverRead(ctx, a.products, resolved.Catalog, operationID, pathValues, queryValues)
		for _, warning := range warnings {
			message := "warning: " + warning.Code
			if warning.Field != "" {
				message += " (" + warning.Field + ")"
			}
			_, _ = fmt.Fprintln(a.stderr, message)
		}
	default:
		err = productexec.ErrDeclarativeRejected
	}
	if err != nil {
		return exitcode.New(exitcode.Local, err.Error())
	}
	client, err := producthttp.New(producthttp.Options{
		Origin:        resolved.Reference.HTTPSOrigin,
		APIKey:        profile.APIKey,
		CatalogDigest: resolved.Reference.CatalogDigest,
		UserAgent:     "quantstore-cli/" + version.Version,
		Timeout:       profile.Timeout,
		HTTPClient:    a.httpClient,
	})
	if err != nil {
		return exitcode.New(exitcode.Trust, err.Error())
	}
	response, err := client.Do(ctx, operation.Method, escapedPath, query)
	if err != nil {
		return err
	}
	if err := output.Write(a.stdout, profile.Output, response.Body); err != nil {
		var payloadErr *output.PayloadError
		if errors.As(err, &payloadErr) {
			return exitcode.New(exitcode.Remote, err.Error())
		}
		return exitcode.New(exitcode.Local, err.Error())
	}
	return nil
}

type driverWarning struct {
	Code  string
	Field string
}

func prepareDriverRead(ctx context.Context, registry *productexec.Registry, catalog *developercatalog.Catalog, operationID string, pathValues, queryValues map[string][]string) (*developercatalog.Operation, string, url.Values, []driverWarning, error) {
	operation, path, query, warnings, err := productexec.PrepareDriverRead(ctx, registry, catalog, operationID, pathValues, queryValues)
	result := make([]driverWarning, 0, len(warnings))
	for _, warning := range warnings {
		result = append(result, driverWarning{Code: warning.Code, Field: warning.Field})
	}
	return operation, path, query, result, err
}

func operationExecution(catalog *developercatalog.Catalog, operationID string) string {
	if catalog == nil {
		return ""
	}
	for _, operation := range catalog.Operations {
		if operation.OperationID == operationID {
			return operation.CLIExecution
		}
	}
	return ""
}

func catalogCommandError(err error) error {
	switch {
	case errors.Is(err, clientcatalog.ErrCatalogUnavailable):
		return exitcode.New(exitcode.Network, "catalog is unavailable")
	case errors.Is(err, clientcatalog.ErrDigestInvalid), errors.Is(err, clientcatalog.ErrCatalogRejected):
		return exitcode.New(exitcode.Trust, "catalog was rejected")
	default:
		return exitcode.New(exitcode.Trust, "catalog resolution failed")
	}
}

func parseAssignments(values []string, allowRepeated bool) (map[string][]string, error) {
	result := make(map[string][]string, len(values))
	for _, raw := range values {
		name, value, found := strings.Cut(raw, "=")
		if !found || name == "" {
			return nil, fmt.Errorf("expected name=value")
		}
		if strings.TrimSpace(name) != name || strings.ContainsAny(name+value, "\x00\r\n") {
			return nil, fmt.Errorf("unsafe parameter")
		}
		if !allowRepeated {
			if _, exists := result[name]; exists {
				return nil, fmt.Errorf("duplicate parameter %q", name)
			}
		}
		result[name] = append(result[name], value)
	}
	return result, nil
}
