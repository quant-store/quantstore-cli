package command

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/quant-store/quantstore-cli/core/internal/config"
	"github.com/quant-store/quantstore-cli/core/internal/exitcode"
	"github.com/quant-store/quantstore-cli/core/internal/output"
	"github.com/quant-store/quantstore-cli/core/internal/secret"
)

func (a *application) authCommand() *cobra.Command {
	command := &cobra.Command{Use: "auth", Short: "Manage the API key for a local profile"}
	var fromStdin bool
	set := &cobra.Command{
		Use:   "set --stdin",
		Short: "Store an API key in the OS keychain",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !fromStdin {
				return exitcode.New(exitcode.Local, "--stdin is required; API keys are never accepted as command arguments")
			}
			if _, exists := os.LookupEnv(secret.EnvironmentVariable); exists {
				return exitcode.New(exitcode.Local, "unset QS_API_KEY before storing a key; this prevents credential inheritance by the OS keychain helper")
			}
			profile, err := a.resolve(command.Context(), false)
			if err != nil {
				return err
			}
			value, err := readCredential(a.stdin, a.stderr)
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			if err := secret.Validate(value); err != nil {
				return exitcode.New(exitcode.Auth, err.Error())
			}
			if err := a.secrets.Set(command.Context(), profile.Name, value); err != nil {
				var backendErr *secret.BackendError
				if errors.As(err, &backendErr) {
					return exitcode.New(exitcode.Local, err.Error())
				}
				return exitcode.New(exitcode.Auth, err.Error())
			}
			return output.WriteValue(a.stdout, profile.Output, map[string]any{
				"profile": profile.Name,
				"source":  "os-keyring",
				"stored":  true,
			})
		},
	}
	set.Flags().BoolVar(&fromStdin, "stdin", false, "read the API key from stdin or a no-echo terminal prompt")
	status := &cobra.Command{
		Use:   "status",
		Short: "Verify the key and show enabled API surfaces",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return a.request(command.Context(), http.MethodGet, "/api/v1/open/me", nil, nil, false)
		},
	}
	remove := &cobra.Command{
		Use:     "delete",
		Aliases: []string{"logout"},
		Short:   "Delete the stored API key from the OS keychain",
		Args:    cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if _, exists := os.LookupEnv(secret.EnvironmentVariable); exists {
				return exitcode.New(exitcode.Local, "unset QS_API_KEY before deleting a stored key; environment credentials are process-local")
			}
			profile, err := a.resolve(command.Context(), false)
			if err != nil {
				return err
			}
			deleted := true
			if err := a.secrets.Delete(command.Context(), profile.Name); err != nil {
				if !errors.Is(err, secret.ErrNotFound) {
					var backendErr *secret.BackendError
					if errors.As(err, &backendErr) {
						return exitcode.New(exitcode.Local, err.Error())
					}
					return exitcode.New(exitcode.Auth, err.Error())
				}
				deleted = false
			}
			return output.WriteValue(a.stdout, profile.Output, map[string]any{"deleted": deleted, "profile": profile.Name})
		},
	}
	command.AddCommand(set, status, remove)
	return command
}

type profileView struct {
	Name           string `json:"name"`
	Current        bool   `json:"current"`
	BaseURL        string `json:"base_url"`
	Output         string `json:"output"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	CredentialRef  string `json:"credential_ref"`
}

func (a *application) profileCommand() *cobra.Command {
	command := &cobra.Command{Use: "profile", Short: "Manage local CLI profiles (never API key plaintext)"}
	list := &cobra.Command{
		Use:   "list",
		Short: "List local profiles",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			file, err := a.config.Load(command.Context())
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			names := make([]string, 0, len(file.Profiles))
			for name := range file.Profiles {
				names = append(names, name)
			}
			sort.Strings(names)
			items := make([]profileView, 0, len(names))
			for _, name := range names {
				items = append(items, makeProfileView(name, file.Profiles[name], file.CurrentProfile == name))
			}
			return output.WriteValue(a.stdout, a.localOutput(file), map[string]any{"items": items})
		},
	}
	use := &cobra.Command{
		Use:   "use <name>",
		Short: "Select the default local profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name, err := config.NormalizeProfileName(args[0])
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			if err := a.config.Update(command.Context(), func(file *config.File) error {
				if _, exists := file.Profiles[name]; !exists {
					return fmt.Errorf("profile %q does not exist", name)
				}
				file.CurrentProfile = name
				return nil
			}); err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			file, err := a.config.Load(command.Context())
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			return output.WriteValue(a.stdout, a.localOutput(file), makeProfileView(name, file.Profiles[name], true))
		},
	}
	var baseURL, defaultOutput string
	var timeoutSeconds int
	set := &cobra.Command{
		Use:   "set <name>",
		Short: "Create or update a local profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			name, err := config.NormalizeProfileName(args[0])
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			if defaultOutput != "" {
				defaultOutput = strings.ToLower(defaultOutput)
				if err := output.ValidateMode(defaultOutput); err != nil {
					return exitcode.New(exitcode.Local, err.Error())
				}
			}
			if timeoutSeconds != 0 && (timeoutSeconds < 1 || timeoutSeconds > 300) {
				return exitcode.New(exitcode.Local, "request timeout must be between 1 and 300 seconds")
			}
			if err := a.config.Update(command.Context(), func(file *config.File) error {
				profile, exists := file.Profiles[name]
				if !exists {
					profile = config.DefaultProfileConfig(name)
				}
				if command.Flags().Changed("base-url") {
					profile.BaseURL = baseURL
				}
				if command.Flags().Changed("default-output") {
					profile.Output = defaultOutput
				}
				if command.Flags().Changed("request-timeout") {
					profile.TimeoutSeconds = timeoutSeconds
				}
				file.Profiles[name] = profile
				return nil
			}); err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			file, err := a.config.Load(command.Context())
			if err != nil {
				return exitcode.New(exitcode.Local, err.Error())
			}
			return output.WriteValue(a.stdout, a.localOutput(file), makeProfileView(name, file.Profiles[name], file.CurrentProfile == name))
		},
	}
	set.Flags().StringVar(&baseURL, "base-url", "", "platform origin, for example https://api.quant-store.com")
	set.Flags().StringVar(&defaultOutput, "default-output", "", "default output mode")
	set.Flags().IntVar(&timeoutSeconds, "request-timeout", 0, "default request timeout in seconds")
	command.AddCommand(list, use, set)
	return command
}

func makeProfileView(name string, profile config.Profile, current bool) profileView {
	return profileView{
		Name:           name,
		Current:        current,
		BaseURL:        profile.BaseURL,
		Output:         profile.Output,
		TimeoutSeconds: profile.TimeoutSeconds,
		CredentialRef:  profile.CredentialRef,
	}
}

func (a *application) localOutput(file config.File) string {
	mode := a.outputFlag
	if mode == "" {
		mode = file.Profiles[file.CurrentProfile].Output
	}
	return strings.ToLower(mode)
}

func readCredential(reader io.Reader, stderr io.Writer) (string, error) {
	if file, ok := reader.(*os.File); ok && term.IsTerminal(int(file.Fd())) {
		_, _ = fmt.Fprint(stderr, "API key: ")
		raw, err := term.ReadPassword(int(file.Fd()))
		_, _ = fmt.Fprintln(stderr)
		if err != nil {
			return "", fmt.Errorf("read API key: %w", err)
		}
		return string(raw), nil
	}
	raw, err := io.ReadAll(io.LimitReader(reader, 4097))
	if err != nil {
		return "", fmt.Errorf("read API key: %w", err)
	}
	if len(raw) > 4096 {
		return "", errors.New("API key input exceeds 4096 bytes")
	}
	raw = bytes.TrimSuffix(raw, []byte("\n"))
	raw = bytes.TrimSuffix(raw, []byte("\r"))
	if len(raw) == 0 {
		return "", errors.New("API key input is empty")
	}
	return string(raw), nil
}
