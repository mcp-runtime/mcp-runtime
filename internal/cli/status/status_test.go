package status_test

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"mcp-runtime/internal/cli/status"
	"mcp-runtime/pkg/authfile"
)

func TestQuickPlatformStatus(t *testing.T) {
	for _, tc := range []struct {
		name    string
		code    int
		body    string
		want    string
		wantErr string
	}{
		{"ready", 200, `{"role":"admin"}`, "READY", ""},
		{"rejected-login", 401, `{"error":"authentication required"}`, "LOGIN REQUIRED", "auth login"},
		{"rejected-login-empty-body", 401, "", "LOGIN REQUIRED", "auth login"},
		{"unavailable", 503, `{"error":"postgres unavailable"}`, "NOT READY", "postgres unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
			var requests atomic.Int64
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/api/v1/auth/me" || r.Method != http.MethodGet {
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("x-api-key") != "test-key" {
					t.Error("missing saved credentials")
				}
				w.WriteHeader(tc.code)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer api.Close()
			t.Setenv("MCP_PLATFORM_API_TOKEN", "test-key")
			t.Setenv("MCP_PLATFORM_API_URL", api.URL)
			// A nil runtime and empty PATH prove the command needs no kubectl or subprocesses.
			t.Setenv("PATH", "")
			cmd := status.New(nil)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			err := cmd.Execute()
			if tc.wantErr == "" && err != nil || tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)) {
				t.Fatalf("error = %v, want %q", err, tc.wantErr)
			}
			if got, want := out.String(), "Platform: "+api.URL+"\nStatus: "+tc.want+"\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
			if requests.Load() != 1 {
				t.Fatalf("requests = %d, want one quick API request", requests.Load())
			}
		})
	}
}

func TestQuickPlatformStatusMissingConfig(t *testing.T) {
	for _, tc := range []struct{ name, token, want string }{
		{"no-login", "", "LOGIN REQUIRED"},
		{"no-api-url", "test-key", "NOT CONFIGURED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
			t.Setenv("MCP_PLATFORM_API_TOKEN", tc.token)
			t.Setenv("MCP_PLATFORM_API_URL", "")
			cmd := status.New(nil)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			if err := cmd.Execute(); err == nil {
				t.Fatal("missing config should return a nonzero exit")
			}
			if got, want := out.String(), "Status: "+tc.want+"\n"; got != want {
				t.Fatalf("output = %q, want %q", got, want)
			}
		})
	}
}

func TestQuickPlatformStatusHonorsContext(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	var requests atomic.Int64
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		<-r.Context().Done()
	}))
	defer api.Close()
	t.Setenv("MCP_PLATFORM_API_TOKEN", "test-key")
	t.Setenv("MCP_PLATFORM_API_URL", api.URL)
	cmd := status.New(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := cmd.ExecuteContext(ctx)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(out.String(), "Status: NOT READY") {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("status ignored caller timeout: %s", elapsed)
	}
	if requests.Load() != 1 {
		t.Fatalf("requests = %d, want 1", requests.Load())
	}
}

func TestQuickPlatformStatusSavedProfile(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	t.Setenv("MCP_PLATFORM_API_TOKEN", "")
	t.Setenv("MCP_PLATFORM_API_URL", "")
	t.Setenv("MCP_PLATFORM_API_PROFILE", "")
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "saved-admin-key" {
			t.Error("active profile credentials were not used")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"role":"admin"}`))
	}))
	defer api.Close()
	path, err := authfile.FilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := authfile.SaveProfile(path, "prod-admin", authfile.CredentialAccount{APIBaseURL: api.URL, Token: "saved-admin-key", Role: "admin"}); err != nil {
		t.Fatal(err)
	}
	cmd := status.New(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if want := "Platform: " + api.URL + "\nStatus: READY\n"; out.String() != want {
		t.Fatalf("output = %q, want %q", out.String(), want)
	}
}

func TestQuickPlatformStatusDefaultTimeout(t *testing.T) {
	t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer api.Close()
	t.Setenv("MCP_PLATFORM_API_TOKEN", "test-key")
	t.Setenv("MCP_PLATFORM_API_URL", api.URL)
	cmd := status.New(nil)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	start := time.Now()
	err := cmd.Execute()
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(out.String(), "Status: NOT READY") {
		t.Fatalf("error = %v, output = %q", err, out.String())
	}
	if elapsed < 4*time.Second || elapsed > 10*time.Second {
		t.Fatalf("five-second timeout elapsed = %s", elapsed)
	}
}
