// Package auth owns routing for the auth top-level command.
package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.uber.org/zap"
	"golang.org/x/term"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/authfile"
)

// apiTestHook, if set, runs instead of the default API probe (unit tests only).
var apiTestHook func(ctx context.Context, apiBaseURL, token string) error

// httpDoHook, if set, runs HTTP requests instead of the default client (unit tests only).
var httpDoHook func(req *http.Request) (*http.Response, error)

// passwordPrompt reads a password without echoing it. Tests replace it to
// exercise the password-login branch without depending on a real terminal.
var passwordPrompt = func(stderr io.Writer) (string, error) {
	stdinFD, err := terminalFD(os.Stdin.Fd())
	if err != nil || !term.IsTerminal(stdinFD) {
		return "", core.NewWithBase(core.ErrAuthTTYRequired, "password login needs a TTY to prompt securely; pass --password for non-interactive login")
	}
	fmt.Fprint(stderr, "Enter platform account password: ")
	password, err := term.ReadPassword(stdinFD)
	fmt.Fprintln(stderr)
	if err != nil {
		return "", core.WrapWithBase(core.ErrAuthReadTokenFailed, err, fmt.Sprintf("read password: %v", err))
	}
	return string(password), nil
}

type manager struct {
	logger *zap.Logger
}

type loginFlags struct {
	apiURL         string
	email          string
	username       string
	password       string
	token          string
	tokenFromStdin bool
	registryHost   string
	profile        string
	skipVerify     bool
}

func newManager(runtime *core.Runtime) *manager {
	return &manager{logger: runtime.Logger()}
}

// New returns the auth command.
func New(runtime *core.Runtime) *cobra.Command {
	m := newManager(runtime)
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Log in to the platform API and manage saved credentials",
		Long: `Authenticate to the MCP Runtime platform using email/password or an API token (not Kubernetes).

Use this for day-to-day deploy and registry-related flows. Cluster install and admin work
use Kubernetes and the cluster commands, not this command.

The token is stored in a local file (mode 0600) under the user config directory, unless you set ` + authfile.EnvAPIToken + `.

Optional environment:
  ` + authfile.EnvAPIURL + `      default API base for login, e.g. https://platform.example.com
  ` + authfile.EnvAPIToken + `    use this token for API calls; overrides the saved token
  ` + authfile.EnvAPIProfile + `  select a saved credentials profile
  MCP_RUNTIME_CONFIG_DIR    override the config directory (default ~/.mcpruntime)`,
	}

	cmd.AddCommand(m.NewLoginCmd())
	cmd.AddCommand(m.NewLogoutCmd())
	cmd.AddCommand(m.NewUseCmd())
	cmd.AddCommand(m.NewStatusCmd())
	cmd.AddCommand(newProviderCheckCmd())
	return cmd
}

func (m *manager) NewLoginCmd() *cobra.Command {
	var f loginFlags
	cmd := &cobra.Command{
		Use:   "login",
		Short: "Log in and save platform credentials",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return m.runLogin(cmd, f)
		},
	}

	cmd.Flags().StringVar(&f.apiURL, "api-url", os.Getenv(authfile.EnvAPIURL), "Platform API base URL (scheme and host, no /api path)")
	cmd.Flags().StringVar(&f.email, "email", "", "Platform account email for password login")
	cmd.Flags().StringVar(&f.username, "username", "", "Alias for --email")
	cmd.Flags().StringVar(&f.password, "password", "", "Platform account password (if omitted with --email, prompt securely in a terminal)")
	cmd.Flags().StringVar(&f.token, "token", "", "API token (or use --token-stdin, or the interactive prompt)")
	cmd.Flags().BoolVar(&f.tokenFromStdin, "token-stdin", false, "Read the token from stdin (non-interactive)")
	cmd.Flags().StringVar(&f.registryHost, "registry-host", "", "Optional host:port for the platform image registry for later use with docker")
	cmd.Flags().StringVar(&f.profile, "profile", "", "Saved credential profile name (defaults to admin, the email local part, or default)")
	cmd.Flags().BoolVar(&f.skipVerify, "skip-verify", false, "Store credentials without calling the API to validate the token")

	return cmd
}

func (m *manager) runLogin(cmd *cobra.Command, f loginFlags) error {
	stdout := io.Writer(os.Stdout)
	stderr := io.Writer(os.Stderr)
	if cmd != nil {
		stdout = cmd.OutOrStdout()
		stderr = cmd.ErrOrStderr()
	}

	apiURL := strings.TrimSpace(f.apiURL)
	if apiURL == "" {
		msg := fmt.Sprintf("api URL is required (set --api-url or %s)", authfile.EnvAPIURL)
		return core.NewWithBase(core.ErrAuthAPIURLRequired, msg)
	}
	apiURL = platformapi.NormalizeBaseURL(apiURL)
	if apiURL == "" {
		return core.NewWithBase(core.ErrAuthAPIURLInvalid, "api URL must include scheme and host")
	}

	loginEmail, err := core.ResolveEmailAlias(f.email, f.username)
	if err != nil {
		return err
	}

	var token, loginRole string
	if loginEmail != "" || strings.TrimSpace(f.password) != "" {
		if loginEmail == "" {
			return core.NewWithBase(core.ErrAuthEmailPasswordRequired, "email and password are both required for password login")
		}
		password := f.password
		if strings.TrimSpace(password) == "" {
			password, err = passwordPrompt(stderr)
			if err != nil {
				return err
			}
			if strings.TrimSpace(password) == "" {
				return core.NewWithBase(core.ErrAuthTokenRequired, "password is required")
			}
		}
		tok, role, err := loginPlatformPassword(context.Background(), apiURL, loginEmail, password)
		if err != nil {
			return core.WrapWithBase(core.ErrAuthPlatformLoginFailed, err, fmt.Sprintf("platform login failed: %v", err))
		}
		token = tok
		loginRole = role
		f.skipVerify = true
	} else if f.tokenFromStdin {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			return core.WrapWithBase(core.ErrAuthReadStdinFailed, err, fmt.Sprintf("read stdin: %v", err))
		}
		token = strings.TrimSpace(string(b))
	} else if strings.TrimSpace(f.token) != "" {
		token = strings.TrimSpace(f.token)
	} else {
		stdinFD, err := terminalFD(os.Stdin.Fd())
		if err != nil || !term.IsTerminal(stdinFD) {
			return core.NewWithBase(core.ErrAuthTTYRequired, "not a TTY: pass --token, --token-stdin, or run in an interactive terminal")
		}
		fmt.Fprint(stderr, "Enter platform API token: ")
		tok, err := term.ReadPassword(stdinFD)
		fmt.Fprintln(stderr)
		if err != nil {
			return core.WrapWithBase(core.ErrAuthReadTokenFailed, err, fmt.Sprintf("read token: %v", err))
		}
		token = strings.TrimSpace(string(tok))
	}
	if token == "" {
		return core.NewWithBase(core.ErrAuthTokenRequired, "token is required")
	}

	if !f.skipVerify {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		var err error
		if apiTestHook != nil {
			err = apiTestHook(ctx, apiURL, token)
		} else {
			err = verifyPlatformAPIToken(ctx, apiURL, token)
		}
		if err != nil {
			return core.WrapWithBase(core.ErrAuthTokenVerificationFailed, err, fmt.Sprintf("API token could not be verified: %v", err))
		}
	}

	path, err := authfile.FilePath()
	if err != nil {
		return err
	}
	profile := loginProfileName(f.profile, loginEmail, loginRole)
	c := authfile.CredentialAccount{
		APIBaseURL:   apiURL,
		Token:        token,
		Role:         loginRole,
		RegistryHost: defaultRegistryHostForLogin(apiURL, f.registryHost),
		Username:     loginEmail,
	}
	if err := authfile.SaveProfile(path, profile, c); err != nil {
		return err
	}
	if m.logger != nil {
		m.logger.Info("saved platform credentials", zap.String("api", apiURL), zap.String("path", path), zap.String("profile", profile))
	}
	fmt.Fprintf(stdout, "Platform credentials saved to %s\n", path)
	fmt.Fprintf(stdout, "Current profile: %s\n", profile)
	if c.RegistryHost != "" {
		fmt.Fprintf(stdout, "Registry host recorded: %s\n", c.RegistryHost)
	}
	return nil
}

func loginProfileName(explicit, email, role string) string {
	if profile := authfile.NormalizeProfileName(explicit); profile != "" {
		return profile
	}
	if strings.EqualFold(strings.TrimSpace(role), "admin") {
		return "admin"
	}
	email = strings.TrimSpace(email)
	if local, _, ok := strings.Cut(email, "@"); ok {
		if profile := authfile.NormalizeProfileName(local); profile != "" {
			return profile
		}
	}
	if profile := authfile.NormalizeProfileName(email); profile != "" {
		return profile
	}
	return "default"
}

func defaultRegistryHostForLogin(apiURL, explicit string) string {
	if host := strings.TrimSpace(explicit); host != "" {
		return host
	}
	u, err := url.Parse(strings.TrimSpace(apiURL))
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" || host == "localhost" || host == "127.0.0.1" {
		return ""
	}
	if strings.HasPrefix(host, "platform.") {
		host = "registry." + strings.TrimPrefix(host, "platform.")
	}
	if port := strings.TrimSpace(u.Port()); port != "" {
		return host + ":" + port
	}
	return host
}

func (m *manager) NewLogoutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logout",
		Short: "Delete saved platform credentials on this machine",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := authfile.FilePath()
			if err != nil {
				return err
			}
			if err := authfile.Remove(path); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Logged out from the platform (local credentials removed).")
			return nil
		},
	}
}

func (m *manager) NewUseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "use PROFILE",
		Short: "Switch the current saved platform credential profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := authfile.FilePath()
			if err != nil {
				return err
			}
			profile := authfile.NormalizeProfileName(args[0])
			if profile == "" {
				return errors.New("profile is required")
			}
			if err := authfile.SelectProfile(path, profile); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Current platform profile: %s\n", profile)
			return nil
		},
	}
}

func (m *manager) NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show whether platform API credentials are configured",
		RunE: func(cmd *cobra.Command, _ []string) error {
			stdout := cmd.OutOrStdout()
			if t := strings.TrimSpace(os.Getenv(authfile.EnvAPIToken)); t != "" {
				fmt.Fprintln(stdout, "A platform API token is set in "+authfile.EnvAPIToken+" and overrides any saved token.")
			} else {
				p, perr := authfile.FilePath()
				if perr == nil {
					if _, fErr := os.Stat(p); fErr == nil {
						fmt.Fprintln(stdout, "Credentials file: "+p)
					} else {
						fmt.Fprintln(stdout, "Credentials file: "+p+" (not present)")
					}
				}
			}
			tok, api, src, rerr := authfile.ResolveToken()
			if rerr != nil {
				if errors.Is(rerr, authfile.ErrNotFound) {
					fmt.Fprintln(stdout, "Not logged in. Run `mcp-runtime auth login` or set "+authfile.EnvAPIToken+".")
					return nil
				}
				return rerr
			}
			fmt.Fprintln(stdout, "Status: have platform API token")
			fmt.Fprintln(stdout, "  source:", src)
			if api != "" {
				fmt.Fprintln(stdout, "  API base URL:", api)
			} else {
				fmt.Fprintln(stdout, "  API base URL: (set --api-url on login or "+authfile.EnvAPIURL+")")
			}
			if c, cErr := fileCredentialsIfRelevant(); cErr == nil && c != nil {
				account, profile, aErr := c.SelectedAccount(os.Getenv(authfile.EnvAPIProfile))
				if aErr == nil {
					if profile != "" {
						fmt.Fprintln(stdout, "  profile:", profile)
					}
					if account.RegistryHost != "" {
						fmt.Fprintln(stdout, "  saved registry host:", account.RegistryHost)
					}
					if account.Role != "" {
						fmt.Fprintln(stdout, "  role (from saved file):", account.Role)
					}
				}
				if names := c.ProfileNames(); len(names) > 0 {
					fmt.Fprintln(stdout, "  saved profiles:", strings.Join(names, ", "))
				}
			}
			fmt.Fprintln(stdout, "  token (masked):", authfile.MaskToken(tok))
			return nil
		},
	}
}

func loginPlatformPassword(ctx context.Context, apiBaseURL, email, password string) (token, role string, err error) {
	body, err := json.Marshal(map[string]string{"email": strings.TrimSpace(email), "password": password})
	if err != nil {
		return "", "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	u := platformapi.NormalizeBaseURL(apiBaseURL) + "/api/v1/auth/login"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("content-type", "application/json")
	req.Header.Set("x-mcp-source", "cli")
	var resp *http.Response
	if httpDoHook != nil {
		resp, err = httpDoHook(req)
	} else {
		resp, err = (&http.Client{Timeout: 30 * time.Second}).Do(req)
	}
	if err != nil {
		return "", "", err
	}
	defer drainAndCloseBody(resp.Body)
	var out struct {
		AccessToken string `json:"access_token"`
		User        struct {
			Role string `json:"role"`
		} `json:"user"`
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", core.NewWithBase(core.ErrAuthLoginHTTPStatus, fmt.Sprintf("HTTP %d", resp.StatusCode))
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", "", err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return "", "", core.NewWithBase(core.ErrAuthLoginResponseMissingAccessToken, "login response did not include access_token")
	}
	return strings.TrimSpace(out.AccessToken), strings.TrimSpace(out.User.Role), nil
}

func fileCredentialsIfRelevant() (*authfile.Credentials, error) {
	if strings.TrimSpace(os.Getenv(authfile.EnvAPIToken)) != "" {
		return nil, nil
	}
	path, err := authfile.FilePath()
	if err != nil {
		return nil, err
	}
	return authfile.Load(path)
}

func verifyPlatformAPIToken(ctx context.Context, apiBaseURL, token string) error {
	u := platformapi.NormalizeBaseURL(apiBaseURL) + "/api/v1/auth/me"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", token)
	req.Header.Set("authorization", "Bearer "+token)
	var resp *http.Response
	if httpDoHook != nil {
		resp, err = httpDoHook(req)
	} else {
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err = client.Do(req)
	}
	if err != nil {
		return err
	}
	defer drainAndCloseBody(resp.Body)
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		return core.NewWithBase(core.ErrAuthServerRejectedToken, fmt.Sprintf("server rejected the token (HTTP %d)", resp.StatusCode))
	case http.StatusNotFound:
		return core.NewWithBase(core.ErrAuthAPIURLMayBeWrong, fmt.Sprintf("API URL may be wrong (path returned HTTP 404, expected %q)", u))
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return core.NewWithBase(core.ErrAuthVerifyRequestFailed, fmt.Sprintf("verify request failed: HTTP %d", resp.StatusCode))
}

func terminalFD(fd uintptr) (int, error) {
	if fd > uintptr(math.MaxInt) {
		return 0, core.NewWithBase(core.ErrAuthFileDescriptorOutOfRange, "file descriptor out of range")
	}
	return int(fd), nil
}

func drainAndCloseBody(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}
