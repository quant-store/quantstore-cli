package command

import (
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/quant-store/quantstore-cli/core/internal/exitcode"
)

var (
	productSlugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	apiKeyIDPattern    = regexp.MustCompile(`^apk_[0-9a-f]{20}$`)
	orderIDPattern     = regexp.MustCompile(`^ord_[0-9a-f]{20}$`)
)

func (a *application) accountCommand() *cobra.Command {
	command := &cobra.Command{Use: "account", Short: "Read or update the account profile"}
	show := &cobra.Command{
		Use:   "show",
		Short: "Show the account profile",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/profile", nil, nil, false)
		},
	}
	var locale string
	update := &cobra.Command{
		Use:   "update",
		Short: "Set the account locale",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !command.Flags().Changed("locale") {
				return exitcode.New(exitcode.Local, "--locale is required")
			}
			return a.request(command.Context(), http.MethodPatch, "/api/v1/open/profile", nil, map[string]string{"locale": locale}, false)
		},
	}
	update.Flags().StringVar(&locale, "locale", "", "target locale, for example zh-CN")
	command.AddCommand(show, update)
	return command
}

func (a *application) walletCommand() *cobra.Command {
	command := &cobra.Command{Use: "wallet", Short: "Read wallet projections"}
	balance := &cobra.Command{
		Use:   "balance",
		Short: "Show available and frozen wallet balances",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/wallet/balance", nil, nil, false)
		},
	}
	var cursor string
	var limit int
	ledger := &cobra.Command{
		Use:   "ledger",
		Short: "List wallet ledger entries",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			query, err := paginationQuery(cursor, limit, 100)
			if err != nil {
				return err
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/wallet/ledger", query, nil, false)
		},
	}
	ledger.Flags().StringVar(&cursor, "cursor", "", "opaque next_cursor from the previous page")
	ledger.Flags().IntVar(&limit, "limit", 0, "page size (1-100; server default 20)")
	command.AddCommand(balance, ledger)
	return command
}

func (a *application) subscriptionsCommand() *cobra.Command {
	command := &cobra.Command{Use: "subscriptions", Short: "Read live subproduct subscription summaries"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List live subscription summaries",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/subscriptions", nil, nil, false)
		},
	}
	show := &cobra.Command{
		Use:   "show <product-slug>",
		Short: "Show one live subscription summary",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !productSlugPattern.MatchString(args[0]) {
				return exitcode.New(exitcode.Local, "invalid product slug")
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/subscriptions/"+args[0], nil, nil, false)
		},
	}
	command.AddCommand(list, show)
	return command
}

func (a *application) ordersCommand() *cobra.Command {
	command := &cobra.Command{Use: "orders", Short: "Read owner-scoped orders"}
	var cursor, status string
	var limit int
	list := &cobra.Command{
		Use:   "list",
		Short: "List orders",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			query, err := paginationQuery(cursor, limit, 100)
			if err != nil {
				return err
			}
			if status != "" {
				if len(status) > 64 || strings.ContainsAny(status, "\r\n") {
					return exitcode.New(exitcode.Local, "invalid order status")
				}
				query.Set("status", status)
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/orders", query, nil, false)
		},
	}
	list.Flags().StringVar(&cursor, "cursor", "", "opaque next_cursor from the previous page")
	list.Flags().IntVar(&limit, "limit", 0, "page size (1-100; server default 20)")
	list.Flags().StringVar(&status, "status", "", "order status filter (applied within each server page)")
	show := &cobra.Command{
		Use:   "show <order-id>",
		Short: "Show one order",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if !orderIDPattern.MatchString(args[0]) {
				return exitcode.New(exitcode.Local, "invalid order id")
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/orders/"+args[0], nil, nil, false)
		},
	}
	command.AddCommand(list, show)
	return command
}

type usageFlags struct {
	APIKeyID    string
	Surface     string
	ProductSlug string
	Since       string
	Until       string
	Cursor      string
	Limit       int
}

func (a *application) usageCommand() *cobra.Command {
	command := &cobra.Command{Use: "usage", Short: "Read best-effort API activity"}
	summaryFlags := &usageFlags{}
	summary := &cobra.Command{
		Use:   "summary",
		Short: "Show an activity summary (not billing truth)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			query, err := summaryFlags.query(false)
			if err != nil {
				return err
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/usage/summary", query, nil, true)
		},
	}
	bindUsageFlags(summary, summaryFlags, false)
	callsFlags := &usageFlags{}
	calls := &cobra.Command{
		Use:   "calls",
		Short: "List redacted activity calls (not billing truth)",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			query, err := callsFlags.query(true)
			if err != nil {
				return err
			}
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/usage/calls", query, nil, true)
		},
	}
	bindUsageFlags(calls, callsFlags, true)
	command.AddCommand(summary, calls)
	return command
}

func bindUsageFlags(command *cobra.Command, flags *usageFlags, page bool) {
	command.Flags().StringVar(&flags.APIKeyID, "api-key-id", "", "filter by API key id (never API key plaintext)")
	command.Flags().StringVar(&flags.Surface, "surface", "", "account_api or data_api")
	command.Flags().StringVar(&flags.ProductSlug, "product", "", "filter data_api by product slug")
	command.Flags().StringVar(&flags.Since, "since", "", "RFC3339 inclusive start time")
	command.Flags().StringVar(&flags.Until, "until", "", "RFC3339 exclusive end time")
	if page {
		command.Flags().StringVar(&flags.Cursor, "cursor", "", "opaque next_cursor; keep all filters unchanged")
		command.Flags().IntVar(&flags.Limit, "limit", 0, "page size (1-200; server default 50)")
	}
}

func (flags *usageFlags) query(page bool) (url.Values, error) {
	query := make(url.Values)
	if flags.APIKeyID != "" {
		if !apiKeyIDPattern.MatchString(flags.APIKeyID) {
			return nil, exitcode.New(exitcode.Local, "invalid API key id")
		}
		query.Set("api_key_id", flags.APIKeyID)
	}
	if flags.Surface != "" && flags.Surface != "account_api" && flags.Surface != "data_api" {
		return nil, exitcode.New(exitcode.Local, "surface must be account_api or data_api")
	}
	if flags.Surface != "" {
		query.Set("surface", flags.Surface)
	}
	if flags.ProductSlug != "" {
		if !productSlugPattern.MatchString(flags.ProductSlug) {
			return nil, exitcode.New(exitcode.Local, "invalid product slug")
		}
		if flags.Surface == "account_api" {
			return nil, exitcode.New(exitcode.Local, "--product cannot be combined with --surface account_api")
		}
		query.Set("product_slug", flags.ProductSlug)
	}
	var since, until time.Time
	var hasSince, hasUntil bool
	if flags.Since != "" {
		parsed, err := time.Parse(time.RFC3339, flags.Since)
		if err != nil {
			return nil, exitcode.New(exitcode.Local, "since must be RFC3339")
		}
		since, hasSince = parsed, true
		query.Set("since", flags.Since)
	}
	if flags.Until != "" {
		parsed, err := time.Parse(time.RFC3339, flags.Until)
		if err != nil {
			return nil, exitcode.New(exitcode.Local, "until must be RFC3339")
		}
		until, hasUntil = parsed, true
		query.Set("until", flags.Until)
	}
	if hasSince && hasUntil && !since.Before(until) {
		return nil, exitcode.New(exitcode.Local, "since must be before until")
	}
	if page {
		if flags.Cursor != "" {
			if len(flags.Cursor) > 4096 || strings.ContainsAny(flags.Cursor, "\r\n") {
				return nil, exitcode.New(exitcode.Local, "invalid cursor")
			}
			query.Set("cursor", flags.Cursor)
		}
		if flags.Limit != 0 {
			if flags.Limit < 1 || flags.Limit > 200 {
				return nil, exitcode.New(exitcode.Local, "limit must be between 1 and 200")
			}
			query.Set("limit", strconv.Itoa(flags.Limit))
		}
	}
	return query, nil
}

func paginationQuery(cursor string, limit, maxLimit int) (url.Values, error) {
	query := make(url.Values)
	if cursor != "" {
		if len(cursor) > 4096 || strings.ContainsAny(cursor, "\r\n") {
			return nil, exitcode.New(exitcode.Local, "invalid cursor")
		}
		query.Set("cursor", cursor)
	}
	if limit != 0 {
		if limit < 1 || limit > maxLimit {
			return nil, exitcode.New(exitcode.Local, fmt.Sprintf("limit must be between 1 and %d", maxLimit))
		}
		query.Set("limit", strconv.Itoa(limit))
	}
	return query, nil
}
