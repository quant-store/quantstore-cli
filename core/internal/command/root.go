package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	clientcatalog "github.com/quant-store/quantstore-cli/core/internal/catalog"
	"github.com/quant-store/quantstore-cli/core/internal/config"
	"github.com/quant-store/quantstore-cli/core/internal/exitcode"
	"github.com/quant-store/quantstore-cli/core/internal/output"
	"github.com/quant-store/quantstore-cli/core/internal/productexec"
	"github.com/quant-store/quantstore-cli/core/internal/redact"
	"github.com/quant-store/quantstore-cli/core/internal/secret"
	"github.com/quant-store/quantstore-cli/core/internal/transport/httpx"
	"github.com/quant-store/quantstore-cli/core/internal/version"
)

type Options struct {
	Args        []string
	Stdin       io.Reader
	Stdout      io.Writer
	Stderr      io.Writer
	ConfigPath  string
	SecretStore secret.Store
	HTTPClient  *http.Client
	MaxRetries  int
	CatalogDir  string
	Catalogs    CatalogResolver
	Products    *productexec.Registry
}

// CatalogResolver is owned by the command consumer so tests and future
// callers can supply an exact catalog source without exposing R2 internals.
type CatalogResolver interface {
	Resolve(context.Context, string) (clientcatalog.Resolved, error)
}

type application struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	config      *config.Store
	secrets     secret.Store
	httpClient  *http.Client
	maxRetries  int
	catalogs    CatalogResolver
	catalogDir  string
	products    *productexec.Registry
	profileFlag string
	outputFlag  string
	timeoutFlag int
}

func Execute(ctx context.Context, options Options) int {
	root, err := newRoot(options)
	if err != nil {
		_, _ = fmt.Fprintln(defaultWriter(options.Stderr, os.Stderr), "error:", redact.String(err.Error()))
		return exitcode.From(err)
	}
	root.SetArgs(options.Args)
	if err := root.ExecuteContext(ctx); err != nil {
		_, _ = fmt.Fprintln(defaultWriter(options.Stderr, os.Stderr), "error:", redact.String(err.Error()))
		return exitcode.From(err)
	}
	return exitcode.OK
}

func newRoot(options Options) (*cobra.Command, error) {
	stdin := defaultReader(options.Stdin, os.Stdin)
	stdout := defaultWriter(options.Stdout, os.Stdout)
	stderr := defaultWriter(options.Stderr, os.Stderr)
	configPath := options.ConfigPath
	if configPath == "" {
		var err error
		configPath, err = config.DefaultPath()
		if err != nil {
			return nil, exitcode.New(exitcode.Local, err.Error())
		}
	}
	secretStore := options.SecretStore
	if secretStore == nil {
		secretStore = secret.KeyringStore{}
	}
	maxRetries := options.MaxRetries
	if maxRetries == 0 {
		maxRetries = 2
	} else if maxRetries < 0 {
		maxRetries = 0
	}
	app := &application{
		stdin:      stdin,
		stdout:     stdout,
		stderr:     stderr,
		config:     config.NewStore(configPath),
		secrets:    secretStore,
		httpClient: options.HTTPClient,
		maxRetries: maxRetries,
		catalogs:   options.Catalogs,
		catalogDir: options.CatalogDir,
		products:   options.Products,
	}
	root := &cobra.Command{
		Use:           "qs",
		Short:         "QuantStore command-line client",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.PersistentFlags().StringVar(&app.profileFlag, "profile", "", "local profile to use")
	root.PersistentFlags().StringVarP(&app.outputFlag, "output", "o", "", "output: auto, table, json, jsonl, yaml or raw")
	root.PersistentFlags().IntVar(&app.timeoutFlag, "timeout", 0, "request timeout in seconds (1-300)")
	root.AddCommand(
		app.authCommand(),
		app.profileCommand(),
		app.accountCommand(),
		app.walletCommand(),
		app.subscriptionsCommand(),
		app.ordersCommand(),
		app.usageCommand(),
		app.productCommand(),
		app.versionCommand(),
		app.completionCommand(root),
	)
	return root, nil
}

type resolvedProfile struct {
	Name    string
	Config  config.Profile
	Output  string
	Timeout time.Duration
	APIKey  string
}

func (a *application) resolve(ctx context.Context, requireKey bool) (resolvedProfile, error) {
	file, err := a.config.Load(ctx)
	if err != nil {
		return resolvedProfile{}, exitcode.New(exitcode.Local, err.Error())
	}
	name := file.CurrentProfile
	if a.profileFlag != "" {
		name, err = config.NormalizeProfileName(a.profileFlag)
		if err != nil {
			return resolvedProfile{}, exitcode.New(exitcode.Local, err.Error())
		}
	}
	profile, exists := file.Profiles[name]
	if !exists {
		return resolvedProfile{}, exitcode.New(exitcode.Local, fmt.Sprintf("profile %q does not exist", name))
	}
	mode := profile.Output
	if a.outputFlag != "" {
		mode = strings.ToLower(a.outputFlag)
	}
	if err := output.ValidateMode(mode); err != nil {
		return resolvedProfile{}, exitcode.New(exitcode.Local, err.Error())
	}
	timeoutSeconds := profile.TimeoutSeconds
	if a.timeoutFlag != 0 {
		if a.timeoutFlag < 1 || a.timeoutFlag > 300 {
			return resolvedProfile{}, exitcode.New(exitcode.Local, "timeout must be between 1 and 300 seconds")
		}
		timeoutSeconds = a.timeoutFlag
	}
	result := resolvedProfile{Name: name, Config: profile, Output: mode, Timeout: time.Duration(timeoutSeconds) * time.Second}
	if requireKey {
		result.APIKey, _, err = secret.Resolve(ctx, name, a.secrets)
		if err != nil {
			if errors.Is(err, secret.ErrNotFound) {
				return resolvedProfile{}, exitcode.New(exitcode.Auth, "API key not found; run 'qs auth set --stdin' or set QS_API_KEY for this process")
			}
			var backendErr *secret.BackendError
			if errors.As(err, &backendErr) {
				return resolvedProfile{}, exitcode.New(exitcode.Local, err.Error())
			}
			return resolvedProfile{}, exitcode.New(exitcode.Auth, err.Error())
		}
	}
	return result, nil
}

func (a *application) request(ctx context.Context, method, path string, query map[string][]string, body any, bestEffort bool) error {
	profile, err := a.resolve(ctx, true)
	if err != nil {
		return err
	}
	client, err := httpx.New(httpx.Options{
		BaseURL:    profile.Config.BaseURL,
		APIKey:     profile.APIKey,
		UserAgent:  "quantstore-cli/" + version.Version,
		Timeout:    profile.Timeout,
		HTTPClient: a.httpClient,
		MaxRetries: a.maxRetries,
	})
	if err != nil {
		return exitcode.New(exitcode.Local, err.Error())
	}
	response, err := client.DoJSON(ctx, method, path, query, body)
	if err != nil {
		return err
	}
	if bestEffort {
		_, _ = fmt.Fprintln(a.stderr, "warning: usage is a best-effort read model and is not billing or audit truth")
	}
	a.warnPartial(response.Body)
	if err := output.Write(a.stdout, profile.Output, response.Body); err != nil {
		var payloadErr *output.PayloadError
		if errors.As(err, &payloadErr) {
			return exitcode.New(exitcode.Remote, err.Error())
		}
		return exitcode.New(exitcode.Local, err.Error())
	}
	return nil
}

func (a *application) warnPartial(raw []byte) {
	var status struct {
		Partial             bool `json:"partial"`
		UnavailableProducts []struct {
			ProductSlug string `json:"product_slug"`
			Code        string `json:"code"`
		} `json:"unavailable_products"`
		UnavailableSources []string `json:"unavailable_sources"`
	}
	if json.Unmarshal(raw, &status) != nil || !status.Partial {
		return
	}
	parts := make([]string, 0, len(status.UnavailableProducts)+len(status.UnavailableSources))
	for _, unavailable := range status.UnavailableProducts {
		if unavailable.ProductSlug != "" {
			parts = append(parts, unavailable.ProductSlug)
		}
	}
	parts = append(parts, status.UnavailableSources...)
	message := "warning: response is partial"
	if len(parts) > 0 {
		message += "; unavailable: " + strings.Join(parts, ",")
	}
	_, _ = fmt.Fprintln(a.stderr, redact.String(message))
}

func (a *application) versionCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Show CLI build information",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			mode := a.outputFlag
			if mode == "" {
				mode = "auto"
			}
			return output.WriteValue(a.stdout, mode, version.Current())
		},
	}
}

func (a *application) completionCommand(root *cobra.Command) *cobra.Command {
	return &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(_ *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletion(a.stdout)
			case "zsh":
				return root.GenZshCompletion(a.stdout)
			case "fish":
				return root.GenFishCompletion(a.stdout, true)
			case "powershell":
				return root.GenPowerShellCompletion(a.stdout)
			default:
				return exitcode.New(exitcode.Local, "unsupported shell")
			}
		},
	}
}

func defaultReader(value, fallback io.Reader) io.Reader {
	if value == nil {
		return fallback
	}
	return value
}

func defaultWriter(value, fallback io.Writer) io.Writer {
	if value == nil {
		return fallback
	}
	return value
}
