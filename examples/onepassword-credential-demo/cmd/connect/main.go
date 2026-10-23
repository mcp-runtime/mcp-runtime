// Local-only connector. 1Password values never enter MCP tool arguments/results.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type connector struct{ gateway, account, usernameRef, passwordRef string }

func (c connector) validateTarget(raw string) (*url.URL, error) {
	target, err := url.Parse(raw)
	gateway, gerr := url.Parse(c.gateway)
	if err != nil || gerr != nil || gateway.Scheme != "https" || gateway.Host == "" || gateway.User != nil || gateway.RawQuery != "" || gateway.Fragment != "" || !strings.HasSuffix(gateway.Path, "/mcp") ||
		target.Scheme != "https" || target.Host != gateway.Host || target.User != nil || target.Fragment != "" || target.Path != gateway.Path+"/connect" || target.RawPath != "" || len(target.Query()) != 1 || len(target.Query()["ticket"]) != 1 || len(target.Query().Get("ticket")) != 43 {
		return nil, errors.New("connection URL does not match the configured HTTPS gateway")
	}
	return target, nil
}

func (c connector) read(ctx context.Context, reference string) (string, error) {
	if !strings.HasPrefix(reference, "op://") || strings.ContainsAny(reference, "\r\n") {
		return "", errors.New("configure an op:// reference")
	}
	args := []string{"read", reference}
	if c.account != "" {
		args = append(args, "--account", c.account)
	}
	cmd := exec.CommandContext(ctx, "op", args...)
	// Explicitly use the user's desktop/CLI identity, never an inherited service account or Connect server.
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "OP_SERVICE_ACCOUNT_TOKEN" || name == "OP_CONNECT_HOST" || name == "OP_CONNECT_TOKEN" || strings.HasPrefix(name, "OP_SESSION_") {
			continue
		}
		cmd.Env = append(cmd.Env, entry)
	}
	output, err := cmd.Output()
	if err != nil {
		return "", errors.New("1Password access failed; unlock the desktop app and enable CLI integration")
	}
	value := strings.TrimSuffix(strings.TrimSuffix(string(output), "\n"), "\r")
	if value == "" || len(value) > 1024 || strings.ContainsAny(value, "\r\n\x00") {
		return "", errors.New("1Password field is empty or unsuitable for this demo")
	}
	return value, nil
}

func (c connector) connect(ctx context.Context, raw string) error {
	target, err := c.validateTarget(raw)
	if err != nil {
		return err
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, "GET", target.String(), nil)
	if err != nil {
		return errors.New("invalid connection URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return errors.New("cannot reach configured demo gateway")
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16384))
	resp.Body.Close()
	if resp.StatusCode != 200 {
		return errors.New("connection expired; call connect_upstream again")
	}
	csrf := ""
	for _, cookie := range resp.Cookies() {
		if cookie.Name == "demo_csrf" && cookie.Secure && cookie.HttpOnly {
			csrf = cookie.Value
		}
	}
	if csrf == "" {
		return errors.New("gateway did not issue a connection approval cookie")
	}
	username, err := c.read(ctx, c.usernameRef)
	if err != nil {
		return err
	}
	password, err := c.read(ctx, c.passwordRef)
	if err != nil {
		return err
	}
	values := url.Values{"ticket": {target.Query().Get("ticket")}, "csrf": {csrf}, "username": {username}, "password": {password}}
	target.RawQuery = ""
	req, err = http.NewRequestWithContext(ctx, "POST", target.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return errors.New("cannot prepare connection")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", target.Scheme+"://"+target.Host)
	resp, err = client.Do(req)
	if err != nil {
		return errors.New("credential submission failed")
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 16384))
	if resp.StatusCode != 200 {
		return errors.New("gateway rejected the test connection; check the dedicated demo Login and retry with a new connection URL")
	}
	return nil
}

func run() error {
	var c connector
	var connectionURL string
	var stdio bool
	flag.StringVar(&c.gateway, "gateway", "https://mcp.mcpruntime.org/onepassword-credential-demo/mcp", "Exact HTTPS MCP resource URL allowed to receive the test credentials")
	flag.StringVar(&c.account, "account", "", "1Password account name or ID")
	flag.StringVar(&c.usernameRef, "username-ref", "", "Dedicated test Login's op:// username reference")
	flag.StringVar(&c.passwordRef, "password-ref", "", "Dedicated test Login's op:// password reference")
	flag.StringVar(&connectionURL, "connect-url", "", "One-use URL returned by the remote connect_upstream tool")
	flag.BoolVar(&stdio, "stdio", false, "Expose a local MCP tool for connecting the saved test Login")
	flag.Parse()
	if !strings.HasPrefix(c.usernameRef, "op://") || !strings.HasPrefix(c.passwordRef, "op://") {
		return errors.New("username-ref and password-ref must be op:// references to a dedicated test Login")
	}
	if stdio {
		server := mcp.NewServer(&mcp.Implementation{Name: "onepassword-desktop-demo", Version: "0.1.0"}, nil)
		type args struct {
			ConnectURL string `json:"connect_url" jsonschema:"One-use URL returned by the hosted demo's connect_upstream tool"`
		}
		mcp.AddTool(server, &mcp.Tool{Name: "connect_from_1password", Description: "Request local 1Password approval and connect the configured dedicated test Login to the pinned hosted demo. Credentials never appear in the tool response."}, func(ctx context.Context, _ *mcp.CallToolRequest, a args) (*mcp.CallToolResult, any, error) {
			ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
			defer cancel()
			if err := c.connect(ctx, a.ConnectURL); err != nil {
				return nil, nil, err
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "Connected the saved test Login. Call upstream_status on the hosted demo twice to verify credential reuse."}}}, nil, nil
		})
		return server.Run(context.Background(), &mcp.StdioTransport{})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := c.connect(ctx, connectionURL); err != nil {
		return err
	}
	fmt.Println("Connected the saved test Login. Call upstream_status in the same MCP OAuth session.")
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
