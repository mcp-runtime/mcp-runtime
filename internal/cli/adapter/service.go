//go:build darwin || linux

package adapter

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"mcp-runtime/internal/adapterservice"
	"mcp-runtime/internal/cli/platformapi"
	"mcp-runtime/pkg/authfile"
	"mcp-runtime/pkg/certauth"
)

func addServiceCommands(parent *cobra.Command) {
	store, _ := adapterservice.DefaultStore()
	parent.PersistentFlags().StringVar(&store.Dir, "state-dir", store.Dir, "Owner-only adapter state directory")
	// Keep help goldens and copied command output independent of the current
	// user's home path while retaining the resolved absolute runtime default.
	parent.PersistentFlags().Lookup("state-dir").DefValue = "~/.mcpruntime/adapter"
	service := &cobra.Command{Use: "service", Short: "Manage the per-user macOS background adapter"}
	parent.AddCommand(service)
	for _, action := range []string{"install", "uninstall", "start", "stop", "restart", "status", "logs", "reconnect", "run"} {
		action := action
		var purge bool
		cmd := &cobra.Command{Use: action, Short: map[string]string{
			"install": "Install or update the LaunchAgent and start at login", "uninstall": "Stop and remove the LaunchAgent; preserve profiles by default",
			"start": "Start the installed user service", "stop": "Stop the user service until started or next login", "restart": "Drain and restart the user service",
			"status": "Show measured daemon and profile state as JSON", "logs": "Show bounded, redacted diagnostic events as JSON", "reconnect": "Reload validated profiles and reconnect without extending consent",
			"run": "Run the protected daemon in the foreground",
		}[action], Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			switch action {
			case "install":
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				return store.Install(ctx, exe)
			case "uninstall":
				return store.Uninstall(ctx, purge)
			case "start":
				return store.Start(ctx)
			case "stop":
				return store.Stop(ctx)
			case "restart":
				if err := store.Stop(ctx); err != nil {
					return err
				}
				return store.Start(ctx)
			case "run":
				return (&adapterservice.Daemon{Store: store, Issue: renewProfileCertificate}).Run(ctx)
			case "status", "logs", "reconnect":
				method, path := "GET", "/v1/status"
				if action == "reconnect" {
					method, path = "POST", "/v1/reload"
				}
				status, err := store.Request(ctx, method, path)
				if err != nil {
					if action != "status" {
						return err
					}
					// A missing service differs from an inaccessible or unsafe socket.
					if _, statErr := os.Lstat(store.Socket()); statErr == nil {
						return err
					}
					status = adapterservice.Status{Version: adapterservice.ProtocolVersion, StartAtLogin: store.Installed(), Profiles: []adapterservice.ProfileStatus{}, Events: []adapterservice.Event{}}
				}
				if action == "logs" {
					return json.NewEncoder(cmd.OutOrStdout()).Encode(status.Events)
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(status)
			}
			return nil
		}}
		if action == "uninstall" {
			cmd.Flags().BoolVar(&purge, "purge", false, "Also delete saved profiles and their credentials")
		}
		service.AddCommand(cmd)
	}
	profiles := &cobra.Command{Use: "profile", Short: "Authorize isolated adapter profiles and configure local MCP clients"}
	parent.AddCommand(profiles)
	var p adapterservice.Profile
	add := &cobra.Command{Use: "add NAME", Short: "Create a profile using the selected platform login and an authorized session", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		lock, err := store.Lock("profiles")
		if err != nil {
			return err
		}
		defer lock.Close()
		list, err := store.Load()
		if err != nil {
			return err
		}
		for _, existing := range list {
			if existing.Name == args[0] {
				return errors.New("profile exists; use adapter profile authorize to renew consent")
			}
		}
		p.Name = args[0]
		p.LocalToken, err = adapterservice.Token()
		if err != nil {
			return err
		}
		// Validate the non-secret configuration before requesting platform consent.
		candidate := p
		candidate.PlatformURL = "https://validation.invalid"
		candidate.PlatformToken = "pending"
		candidate.Human = "pending"
		candidate.Session = "pending"
		candidate.TrustDomain = "pending"
		candidate.SessionExpires = time.Now()
		if err := adapterservice.ValidateProfiles(append(list, candidate)); err != nil {
			return err
		}
		if err := authorizeProfile(cmd.Context(), &p); err != nil {
			return err
		}
		if err := store.Save(append(list, p)); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Profile saved. Run adapter service reconnect (or install), then adapter profile client-config NAME --output FILE.")
		return nil
	}}
	add.Flags().StringVar(&p.RuntimeURL, "runtime-url", "", "Platform-issued HTTPS MCP endpoint")
	add.Flags().StringVar(&p.Namespace, "namespace", "", "Target MCPServer namespace")
	add.Flags().StringVar(&p.Server, "server", "", "Target MCPServer name")
	add.Flags().StringVar(&p.Agent, "agent", "", "Active managed agent ID")
	add.Flags().StringVar(&p.Listen, "listen", "127.0.0.1:8099", "Unique local 127.0.0.1 address and port")
	profiles.AddCommand(add)
	for _, action := range []string{"authorize", "remove"} {
		action := action
		profiles.AddCommand(&cobra.Command{Use: action + " NAME", Short: map[string]string{"authorize": "Explicitly authorize a session with the selected platform login", "remove": "Remove a profile and its stored credentials"}[action], Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			lock, err := store.Lock("profiles")
			if err != nil {
				return err
			}
			defer lock.Close()
			list, err := store.Load()
			if err != nil {
				return err
			}
			for i := range list {
				if list[i].Name == args[0] {
					if action == "remove" {
						list = append(list[:i], list[i+1:]...)
					} else if err := authorizeProfile(cmd.Context(), &list[i]); err != nil {
						return err
					}
					if err := store.Save(list); err != nil {
						return err
					}
					if _, err := store.Request(cmd.Context(), "POST", "/v1/reload"); err != nil {
						fmt.Fprintln(cmd.ErrOrStderr(), "Profiles saved; daemon reload unavailable. Run adapter service start or reconnect.")
					}
					return nil
				}
			}
			return errors.New("profile not found")
		}})
	}
	var output string
	config := &cobra.Command{Use: "client-config NAME", Short: "Write a local MCP client config including its credential to an owner-only file", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if output == "" {
			return errors.New("--output FILE is required; credentials are never printed to the terminal")
		}
		list, err := store.Load()
		if err != nil {
			return err
		}
		for _, p := range list {
			if p.Name == args[0] {
				config := map[string]any{"mcpServers": map[string]any{p.Name: map[string]any{"url": "http://" + p.Listen + "/mcp", "headers": map[string]string{adapterservice.LocalCredentialHeader: p.LocalToken}}}}
				b, err := json.MarshalIndent(config, "", "  ")
				if err != nil {
					return err
				}
				f, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600) // #nosec G304 -- explicit user-selected destination; exclusive creation and owner-only mode prevent overwrite and credential exposure.
				if err != nil {
					return err
				}
				_, writeErr := f.Write(b)
				closeErr := f.Close()
				if writeErr != nil {
					return writeErr
				}
				if closeErr != nil {
					return closeErr
				}
				fmt.Fprintln(cmd.OutOrStdout(), "Client configuration written. Treat this file as a credential; add upstream OAuth separately when required.")
				return nil
			}
		}
		return errors.New("profile not found")
	}}
	config.Flags().StringVar(&output, "output", "", "New file for secret client configuration (0600; refuses overwrite)")
	profiles.AddCommand(config)
}

func authorizeProfile(ctx context.Context, p *adapterservice.Profile) error {
	token, base, _, err := authfile.ResolveToken()
	if err != nil {
		return errors.New("sign in interactively with mcp-runtime auth login first")
	}
	// Reauthorization must not silently switch a profile to another platform.
	if p.PlatformURL != "" && platformapi.NormalizeBaseURL(p.PlatformURL) != platformapi.NormalizeBaseURL(base) {
		return errors.New("selected login belongs to another platform; select the profile's platform login")
	}
	client, err := platformapi.NewCredentialClient(base, token)
	if err != nil {
		return err
	}
	session, err := client.CreateAdapterSession(ctx, platformapi.AdapterSessionRequest{ServerName: p.Server, Namespace: p.Namespace, AgentID: p.Agent})
	if err != nil {
		return errors.New("profile authorization failed; check login, active agent and matching grant")
	}
	if session.AgentID != p.Agent || session.ServerName != p.Server || session.Namespace != p.Namespace || session.HumanID == "" || session.Name == "" || !session.ExpiresAt.After(time.Now()) {
		return errors.New("platform session does not match requested profile")
	}
	if p.Human != "" && (p.Human != session.HumanID || p.Team != session.TeamID) {
		return errors.New("selected login would change the profile identity; create a separate profile")
	}
	p.PlatformURL = platformapi.NormalizeBaseURL(base)
	p.PlatformToken = token
	p.Human = session.HumanID
	p.Team = session.TeamID
	p.Session = session.Name
	p.TrustDomain = session.TrustDomain
	p.SessionExpires = session.ExpiresAt
	return nil
}

// Renewal signs a CSR for the same session. It deliberately does not call the
// session-creation endpoint, so a background retry cannot extend user consent.
func renewProfileCertificate(ctx context.Context, p adapterservice.Profile) (adapterservice.Credential, error) {
	if !time.Now().Before(p.SessionExpires) {
		return adapterservice.Credential{}, errors.New("session expired")
	}
	client, err := platformapi.NewCredentialClient(p.PlatformURL, p.PlatformToken)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	key, csr, _, err := certauth.BuildSessionCSR(p.TrustDomain, p.Namespace, p.Session)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	issued, err := client.IssueAdapterCertificate(ctx, platformapi.AdapterCertificateRequest{Namespace: p.Namespace, Session: p.Session, CSR: string(csr)})
	if err != nil {
		return adapterservice.Credential{}, err
	}
	cert, err := tls.X509KeyPair([]byte(issued.Certificate), key)
	if err != nil {
		return adapterservice.Credential{}, err
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return adapterservice.Credential{}, err
	}
	expected := "spiffe://" + p.TrustDomain + "/ns/" + p.Namespace + "/session/" + p.Session
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != expected || issued.SPIFFEID != expected || !leaf.NotAfter.Equal(issued.ExpiresAt) || leaf.NotAfter.After(p.SessionExpires) {
		return adapterservice.Credential{}, errors.New("certificate identity or lifetime mismatch")
	}
	roots, err := x509.SystemCertPool()
	if err != nil {
		return adapterservice.Credential{}, err
	}
	if !roots.AppendCertsFromPEM([]byte(issued.CABundle)) {
		return adapterservice.Credential{}, errors.New("invalid issued CA bundle")
	}
	return adapterservice.Credential{Certificate: cert, Roots: roots, Expires: leaf.NotAfter}, nil
}
