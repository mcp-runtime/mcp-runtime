package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/authfile"
)

func TestVerifyPlatformAPIToken(t *testing.T) {
	prevHook := httpDoHook
	httpDoHook = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("path: %q", r.URL.Path)
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("x-api-key header")
			return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(bytes.NewReader(nil))}, nil
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("[]")))}, nil
	}
	defer func() { httpDoHook = prevHook }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := verifyPlatformAPIToken(ctx, "https://platform.example.com", "k"); err != nil {
		t.Fatal(err)
	}
}

func TestVerifyPlatformAPITokenUnauthorized(t *testing.T) {
	prevHook := httpDoHook
	httpDoHook = func(_ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusUnauthorized, Body: io.NopCloser(bytes.NewReader(nil))}, nil
	}
	defer func() { httpDoHook = prevHook }()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := verifyPlatformAPIToken(ctx, "https://platform.example.com", "k"); err == nil {
		t.Fatal("expected error")
	}
}

func TestAuthLoginSavesAndVerifies(t *testing.T) {
	d := t.TempDir()
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", d)

	prevHTTPHook := httpDoHook
	httpDoHook = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/auth/me" {
			t.Errorf("path: %q", r.URL.Path)
		}
		if r.Header.Get("x-api-key") != "good" {
			t.Errorf("x-api-key")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader([]byte("[]")))}, nil
	}
	defer func() { httpDoHook = prevHTTPHook }()

	cmd := New(core.NewRuntime(zap.NewNop()))
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	cmd.SetArgs([]string{"login", "--api-url", "https://platform.example.com", "--token", "good"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v stderr=%s", err, errb.String())
	}
	b, rerr := os.ReadFile(filepath.Join(d, "config.json"))
	if rerr != nil {
		t.Fatal(rerr)
	}
	var creds authfile.Credentials
	if err := json.Unmarshal(b, &creds); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	if creds.Token != "good" {
		t.Fatalf("token = %q, want good", creds.Token)
	}
	if creds.APIBaseURL != "https://platform.example.com" {
		t.Fatalf("api_url = %q, want https://platform.example.com", creds.APIBaseURL)
	}
	if creds.RegistryHost != "registry.example.com" {
		t.Fatalf("registry_host = %q, want registry.example.com", creds.RegistryHost)
	}
	if creds.Current != "default" {
		t.Fatalf("current = %q, want default", creds.Current)
	}
}

func TestAuthLoginPromptsForPasswordWhenEmailIsProvided(t *testing.T) {
	d := t.TempDir()
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", d)

	previousPrompt := passwordPrompt
	passwordPrompt = func(stderr io.Writer) (string, error) {
		io.WriteString(stderr, "Enter platform account password: ")
		return "private-test-password", nil
	}
	defer func() { passwordPrompt = previousPrompt }()

	previousHTTPHook := httpDoHook
	httpDoHook = func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/api/v1/auth/login" {
			t.Errorf("path = %q, want password login", r.URL.Path)
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["email"] != "publisher@example.test" || payload["password"] != "private-test-password" {
			t.Errorf("login payload had unexpected credentials")
		}
		body := `{"access_token":"saved-token","user":{"role":"user"}}`
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewBufferString(body))}, nil
	}
	defer func() { httpDoHook = previousHTTPHook }()

	cmd := New(core.NewRuntime(zap.NewNop()))
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	cmd.SetArgs([]string{"login", "--api-url", "https://platform.example.com", "--email", "publisher@example.test"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !bytes.Contains(stderr.Bytes(), []byte("Enter platform account password:")) {
		t.Fatalf("stderr missing password prompt: %q", stderr.String())
	}
	if bytes.Contains(stderr.Bytes(), []byte("private-test-password")) {
		t.Fatal("password was echoed to stderr")
	}
	creds, err := authfile.Load(filepath.Join(d, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if creds.Token != "saved-token" {
		t.Fatalf("saved token = %q, want saved-token", creds.Token)
	}
}

func TestAuthLoginNormalizesTrailingAPIPath(t *testing.T) {
	d := t.TempDir()
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", d)
	previousHook := apiTestHook
	apiTestHook = func(_ context.Context, apiBaseURL, token string) error {
		if apiBaseURL != "https://platform.example.com" {
			t.Fatalf("apiBaseURL = %q, want https://platform.example.com", apiBaseURL)
		}
		if token != "good" {
			t.Fatalf("token = %q, want good", token)
		}
		return nil
	}
	defer func() { apiTestHook = previousHook }()

	cmd := New(core.NewRuntime(zap.NewNop()))
	cmd.SetArgs([]string{"login", "--api-url", "https://platform.example.com/api/", "--token", "good"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(d, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	var creds authfile.Credentials
	if err := json.Unmarshal(b, &creds); err != nil {
		t.Fatalf("unmarshal credentials: %v", err)
	}
	if creds.APIBaseURL != "https://platform.example.com" {
		t.Fatalf("api_url = %q, want https://platform.example.com", creds.APIBaseURL)
	}
	if creds.Token != "good" {
		t.Fatalf("token = %q, want good", creds.Token)
	}
	if creds.RegistryHost != "registry.example.com" {
		t.Fatalf("registry_host = %q, want registry.example.com", creds.RegistryHost)
	}
}

func TestAuthLoginStoresMultipleProfilesAndUseSwitchesCurrent(t *testing.T) {
	d := t.TempDir()
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", d)

	previousHook := apiTestHook
	apiTestHook = func(_ context.Context, _ string, _ string) error { return nil }
	defer func() { apiTestHook = previousHook }()

	cmd := New(core.NewRuntime(zap.NewNop()))
	cmd.SetArgs([]string{"login", "--api-url", "https://platform.example.com", "--token", "admin-token", "--profile", "admin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("admin login: %v", err)
	}

	cmd = New(core.NewRuntime(zap.NewNop()))
	cmd.SetArgs([]string{"login", "--api-url", "https://platform.example.com", "--token", "acme-token", "--profile", "acme"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("acme login: %v", err)
	}

	creds, err := authfile.Load(filepath.Join(d, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if names := creds.ProfileNames(); len(names) != 2 || names[0] != "acme" || names[1] != "admin" {
		t.Fatalf("profiles = %#v", names)
	}
	if creds.Current != "acme" || creds.Token != "acme-token" {
		t.Fatalf("current=%q token=%q, want acme/acme-token", creds.Current, creds.Token)
	}

	cmd = New(core.NewRuntime(zap.NewNop()))
	cmd.SetArgs([]string{"use", "admin"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("use admin: %v", err)
	}
	creds, err = authfile.Load(filepath.Join(d, "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if creds.Current != "admin" || creds.Token != "admin-token" {
		t.Fatalf("current=%q token=%q, want admin/admin-token", creds.Current, creds.Token)
	}
}
