package adapter

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"mcp-runtime/internal/adapterd"
	"mcp-runtime/internal/adapterservice"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/authfile"
	"mcp-runtime/pkg/runtimeconfig"
)

func newServiceCmd(_ *core.Runtime) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "service",
		Short: "Install and control the per-user adapter daemon",
		Long: `The adapter service keeps one loopback listener running for every saved
profile. Each profile is a path on that listener, for example
http://127.0.0.1:8099/cully/mcp. Only that port is used. Other localhost
ports are left alone. MCP clients call the profile URL directly. The daemon
presents that profile's enrolled certificate upstream and forwards the
client's OAuth bearer when the server requires one.

Foreground troubleshooting stays on "adapter proxy".`,
	}
	cmd.AddCommand(newServiceInstallCmd(), newServiceUninstallCmd(), newServiceStartCmd(), newServiceStopCmd(), newServiceRestartCmd(), newServiceStatusCmd(), newServiceLogsCmd(), newServiceRunCmd(), newServiceProfileCmd())
	return cmd
}

func newServiceInstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "install",
		Short: "Install the user LaunchAgent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("adapter service install is supported on macOS")
			}
			bin, err := os.Executable()
			if err != nil {
				return err
			}
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			installed, err := installServiceBinary(bin, home)
			if err != nil {
				return err
			}
			path := adapterd.LaunchdPlistPath(home)
			if err := adapterd.WriteLaunchdPlist(path, installed, launchdEnv()); err != nil {
				return err
			}
			if err := startLaunchd(); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "installed %s\n", path)
			return nil
		},
	}
}

func newServiceUninstallCmd() *cobra.Command {
	var purge bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the LaunchAgent and stop the managed daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if runtime.GOOS != "darwin" {
				return fmt.Errorf("adapter service uninstall is supported on macOS")
			}
			_ = stopLaunchd()
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			if err := adapterd.RemoveLaunchdPlist(adapterd.LaunchdPlistPath(home)); err != nil {
				return err
			}
			if purge {
				dir, err := stateDir()
				if err != nil {
					return err
				}
				if err := os.RemoveAll(dir); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), "uninstalled adapter service")
			return nil
		},
	}
	cmd.Flags().BoolVar(&purge, "purge", false, "Delete saved profiles and local credentials")
	return cmd
}

func newServiceStartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start",
		Short: "Start the adapter daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := startLaunchd(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "adapter daemon started")
			return nil
		},
	}
}

func newServiceStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the adapter daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := stopLaunchd(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "adapter daemon stopped")
			return nil
		},
	}
}

func newServiceRestartCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "restart",
		Short: "Restart the adapter daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			_ = stopLaunchd()
			if err := startLaunchd(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "adapter daemon restarted")
			return nil
		},
	}
}

func newServiceStatusCmd() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show daemon and profile status",
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := fetchStatus()
			if err != nil {
				if output == "json" {
					enc := json.NewEncoder(cmd.OutOrStdout())
					enc.SetIndent("", "  ")
					return enc.Encode(adapterd.Status{Protocol: adapterd.ProtocolVersion, ProcessRunning: false, CheckedAt: time.Now().UTC()})
				}
				fmt.Fprintln(cmd.OutOrStdout(), "adapter daemon is not running")
				return nil
			}
			if output == "json" {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(st)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "running listen=%s ready=%t profiles=%d\n", st.ListenAddr, st.ListenerReady, len(st.Profiles))
			for _, p := range st.Profiles {
				fmt.Fprintf(cmd.OutOrStdout(), "%s %s auth=%s\n", p.Server, p.LocalURL, p.AuthState)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&output, "output", "text", "Output format: text or json")
	return cmd
}

func newServiceLogsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "logs",
		Short: "Show recent redacted daemon logs",
		RunE: func(cmd *cobra.Command, _ []string) error {
			var payload struct {
				Lines []string `json:"lines"`
			}
			if err := controlJSON(http.MethodGet, "/v1/logs", nil, &payload); err != nil {
				return err
			}
			for _, line := range payload.Lines {
				fmt.Fprintln(cmd.OutOrStdout(), line)
			}
			return nil
		},
	}
}

func newServiceRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "run",
		Short:  "Run the adapter daemon in the foreground",
		Hidden: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			d, err := openDaemon()
			if err != nil {
				return err
			}
			d.Hooks.Enroll = enrollProfile
			d.SetStartupAtLogin(launchdInstalled())
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return d.Run(ctx)
		},
	}
}

func newServiceProfileCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "profile", Short: "Add or remove a daemon profile"}
	add := &cobra.Command{
		Use:   "add",
		Short: "Add a profile and print its local MCP URL once",
		RunE: func(cmd *cobra.Command, _ []string) error {
			req := adapterd.AddProfileRequest{}
			req.Server, _ = cmd.Flags().GetString("server")
			req.Namespace, _ = cmd.Flags().GetString("namespace")
			req.Team, _ = cmd.Flags().GetString("team")
			req.Agent, _ = cmd.Flags().GetString("agent")
			req.PlatformURL, _ = cmd.Flags().GetString("platform-url")
			req.RuntimeURL, _ = cmd.Flags().GetString("runtime-url")
			var created struct {
				Profile    adapterd.Profile `json:"profile"`
				LocalURL   string           `json:"localUrl"`
				LocalToken string           `json:"localToken"`
			}
			body, _ := json.Marshal(req)
			if err := controlJSON(http.MethodPost, "/v1/profiles", body, &created); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "local URL: %s\n", created.LocalURL)
			fmt.Fprintf(cmd.OutOrStdout(), "profile ID: %s\n", created.Profile.ID)
			fmt.Fprintf(cmd.OutOrStdout(), "write client credentials with: mcp-runtime adapter service profile client-config %s --output FILE\n", created.Profile.ID)
			return nil
		},
	}
	add.Flags().String("server", "", "MCP server name")
	add.Flags().String("namespace", "", "MCP server namespace")
	add.Flags().String("team", "", "Team label shown in status")
	add.Flags().String("agent", "", "Runtime agent id")
	add.Flags().String("platform-url", "", "Platform API base URL")
	add.Flags().String("runtime-url", "", "Upstream MCP URL")
	_ = add.MarkFlagRequired("server")
	_ = add.MarkFlagRequired("runtime-url")
	remove := &cobra.Command{
		Use:   "remove <id>",
		Short: "Remove a profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			purge, _ := cmd.Flags().GetBool("purge")
			path := "/v1/profiles/" + args[0]
			if purge {
				path += "?purge=true"
			}
			if err := controlJSON(http.MethodDelete, path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "removed profile")
			return nil
		},
	}
	remove.Flags().Bool("purge", false, "Delete stored credentials for the profile")
	var output string
	clientConfig := &cobra.Command{
		Use:   "client-config <id>",
		Short: "Write an MCP client configuration with this profile's local credential",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if output == "" {
				return fmt.Errorf("--output FILE is required; credentials are never printed to the terminal")
			}
			var config json.RawMessage
			path := "/v1/profiles/" + url.PathEscape(args[0]) + "/client-config"
			if err := controlJSON(http.MethodGet, path, nil, &config); err != nil {
				return err
			}
			if err := writeOwnerOnlyConfig(output, config); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Client configuration written. Treat the file as a credential; add upstream OAuth separately when required.")
			return nil
		},
	}
	clientConfig.Flags().StringVar(&output, "output", "", "New owner-only file for the secret client configuration (refuses overwrite)")
	cmd.AddCommand(add, remove, clientConfig)
	return cmd
}

func writeOwnerOnlyConfig(path string, config []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if _, err := f.Write(config); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return err
	}
	return nil
}

func openDaemon() (*adapterd.Daemon, error) {
	dir, err := stateDir()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	runDir := filepath.Join(filepath.Dir(dir), "run")
	secrets := secretStore(filepath.Join(dir, "secrets.json"))
	return &adapterd.Daemon{
		ListenAddr: adapterd.DefaultListenAddr,
		StateDir:   dir,
		SocketPath: filepath.Join(runDir, "adapterd.sock"),
		Store:      adapterd.NewStore(filepath.Join(dir, "profiles.json")),
		Secrets:    secrets,
		Router:     adapterd.NewRouter(),
		Logs:       adapterd.NewLogBuffer(200),
	}, nil
}

func secretStore(file string) adapterd.SecretStore {
	if runtime.GOOS == "darwin" {
		if os.Getenv("MCP_RUNTIME_ADAPTER_FILE_SECRETS") == "" {
			return adapterd.NewKeychainStore()
		}
	}
	return adapterd.NewFileSecretStore(file)
}

func stateDir() (string, error) {
	return runtimeconfig.Path("adapterd")
}

func enrollProfile(ctx context.Context, p adapterd.Profile) (adapterd.Material, error) {
	token, base, _, err := authfile.ResolveToken()
	if err != nil {
		return adapterd.Material{}, fmt.Errorf("sign-in required; run mcp-runtime auth login")
	}
	base = platformapi.NormalizeBaseURL(base)
	if p.PlatformURL != "" && platformapi.NormalizeBaseURL(p.PlatformURL) != base {
		return adapterd.Material{}, fmt.Errorf("sign-in required; select the profile's platform login")
	}
	if p.Session != "" {
		if !p.SessionExpires.After(time.Now()) {
			return adapterd.Material{}, fmt.Errorf("session expired; reauthorize this profile interactively")
		}
		cred, err := renewProfileCertificate(ctx, adapterservice.Profile{
			PlatformURL: base, PlatformToken: token, RuntimeURL: p.RuntimeURL,
			Namespace: p.Namespace, Server: p.Server, Agent: p.Agent, Human: p.Human,
			Team: p.Team, Session: p.Session, TrustDomain: p.TrustDomain,
			SessionExpires: p.SessionExpires,
		})
		if err != nil {
			return adapterd.Material{}, fmt.Errorf("renew the existing adapter session certificate: %w", err)
		}
		key, err := x509.MarshalPKCS8PrivateKey(cred.Certificate.PrivateKey)
		if err != nil {
			return adapterd.Material{}, fmt.Errorf("encode renewed adapter key: %w", err)
		}
		var certPEM []byte
		for _, der := range cred.Certificate.Certificate {
			certPEM = append(certPEM, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
		}
		return adapterd.Material{
			CertPEM: certPEM, KeyPEM: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}),
			Expires: cred.Expires, PlatformURL: base, Human: p.Human, Team: p.Team,
			Session: p.Session, SessionExpires: p.SessionExpires, TrustDomain: p.TrustDomain,
		}, nil
	}
	client, err := platformapi.NewCredentialClient(base, token)
	if err != nil {
		return adapterd.Material{}, err
	}
	flags := platformSessionFlags{server: p.Server, namespace: p.Namespace, agent: p.Agent, platformURL: base}
	cred, err := issueAdapterCredential(ctx, client, flags, p.TrustDomain)
	if err != nil {
		return adapterd.Material{}, err
	}
	return adapterd.Material{
		CertPEM: cred.CertPEM, KeyPEM: cred.KeyPEM, CA: cred.CABundle, Expires: cred.ExpiresAt,
		PlatformURL: base, Human: cred.Session.HumanID, Team: cred.Session.TeamID,
		Session: cred.Session.Name, SessionExpires: cred.Session.ExpiresAt, TrustDomain: cred.Session.TrustDomain,
	}, nil
}

func fetchStatus() (adapterd.Status, error) {
	var st adapterd.Status
	err := controlJSON(http.MethodGet, "/v1/status", nil, &st)
	return st, err
}

func controlJSON(method, path string, body []byte, dest any) error {
	sock, err := socketPath()
	if err != nil {
		return err
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", sock)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 10 * time.Second}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, "http://adapter"+path, reader)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("adapter daemon is not running: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		return fmt.Errorf("adapter daemon: %s", payload)
	}
	if dest == nil || len(payload) == 0 {
		return nil
	}
	return json.Unmarshal(payload, dest)
}

func socketPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Dir(dir), "run", "adapterd.sock"), nil
}

func installServiceBinary(src, home string) (string, error) {
	destDir := filepath.Join(home, ".mcpruntime", "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	dest := filepath.Join(destDir, "mcp-runtime")
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()
	// Copy beside the destination, then replace it atomically. Reinstalling from
	// the managed binary must not truncate the source or a running executable.
	out, err := os.CreateTemp(destDir, ".mcp-runtime-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(out.Name())
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Chmod(0o755); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return "", err
	}
	if err := out.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(out.Name(), dest); err != nil {
		return "", err
	}
	return dest, nil
}

func launchdEnv() map[string]string {
	env := map[string]string{}
	if configDir := os.Getenv(runtimeconfig.EnvDir); configDir != "" {
		env[runtimeconfig.EnvDir] = configDir
	}
	if os.Getenv("MCP_RUNTIME_ADAPTER_FILE_SECRETS") == "" {
		if len(env) == 0 {
			return nil
		}
		return env
	}
	env["MCP_RUNTIME_ADAPTER_FILE_SECRETS"] = os.Getenv("MCP_RUNTIME_ADAPTER_FILE_SECRETS")
	return env
}

func launchdInstalled() bool {
	home, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	_, err = os.Stat(adapterd.LaunchdPlistPath(home))
	return err == nil
}

func startLaunchd() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("adapter service start is supported on macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := adapterd.LaunchdPlistPath(home)
	if _, err := os.Stat(plist); err != nil {
		return fmt.Errorf("adapter service is not installed")
	}
	uid := os.Getuid()
	domain := fmt.Sprintf("gui/%d", uid)
	_ = exec.Command("launchctl", "bootout", domain, plist).Run()
	if out, err := exec.Command("launchctl", "bootstrap", domain, plist).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
	}
	target := fmt.Sprintf("%s/%s", domain, "org.mcpruntime.adapter")
	if out, err := exec.Command("launchctl", "kickstart", "-k", target).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart: %w: %s", err, out)
	}
	return nil
}

func stopLaunchd() error {
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("adapter service stop is supported on macOS")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	plist := adapterd.LaunchdPlistPath(home)
	uid := os.Getuid()
	out, err := exec.Command("launchctl", "bootout", fmt.Sprintf("gui/%d", uid), plist).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchctl bootout: %w: %s", err, out)
	}
	return nil
}
