package platformapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/authfile"
)

func TestApplyAccessFromYAMLFile_MultiDocument(t *testing.T) {
	grantCalls := 0
	sessionCalls := 0
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			if r.Header.Get("x-mcp-source") != "cli" {
				t.Fatalf("x-mcp-source = %q, want cli", r.Header.Get("x-mcp-source"))
			}
			switch r.URL.Path {
			case "/api/v1/runtime/grants":
				grantCalls++
				body, _ := io.ReadAll(r.Body)
				var payload grantAPIBody
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode grant body: %v", err)
				}
				if payload.Subject.TeamID != "team-acme" {
					t.Fatalf("grant subject teamID = %q, want team-acme", payload.Subject.TeamID)
				}
				if payload.ExpiresAt == nil || payload.ExpiresAt.UTC().Format(time.RFC3339) != "2099-01-01T00:00:00Z" {
					t.Fatalf("grant expiresAt = %v, want 2099-01-01T00:00:00Z", payload.ExpiresAt)
				}
			case "/api/v1/runtime/sessions":
				sessionCalls++
				body, _ := io.ReadAll(r.Body)
				var payload sessionAPIBody
				if err := json.Unmarshal(body, &payload); err != nil {
					t.Fatalf("decode session body: %v", err)
				}
				if payload.Subject.TeamID != "team-acme" {
					t.Fatalf("session subject teamID = %q, want team-acme", payload.Subject.TeamID)
				}
			default:
				t.Fatalf("unexpected path %q", r.URL.Path)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}

	d := t.TempDir()
	manifest := filepath.Join(d, "access.yaml")
	if err := os.WriteFile(manifest, []byte(`apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata:
  name: grant-a
  namespace: mcp-servers
spec:
  serverRef:
    name: demo
  subject:
    humanID: user-1
    teamID: team-acme
  maxTrust: low
  expiresAt: "2099-01-01T00:00:00Z"
  allowedSideEffects:
    - read
  toolRules:
    - name: add
      decision: allow
---
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAgentSession
metadata:
  name: session-a
  namespace: mcp-servers
spec:
  serverRef:
    name: demo
  subject:
    humanID: user-1
    teamID: team-acme
  consentedTrust: low
`), 0o600); err != nil {
		t.Fatal(err)
	}

	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		http:      httpClient,
		apiPrefix: "/api/v1",
	}
	if err := client.ApplyAccessFromYAMLFile(context.Background(), manifest); err != nil {
		t.Fatalf("ApplyAccessFromYAMLFile() error = %v", err)
	}
	if grantCalls != 1 || sessionCalls != 1 {
		t.Fatalf("calls = grant:%d session:%d, want 1/1", grantCalls, sessionCalls)
	}
}

func TestRecordImagePublish(t *testing.T) {
	var seenBody string
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		apiPrefix: "/api/v1",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/user/activity/image-publish" {
				t.Fatalf("path = %q, want image publish endpoint", r.URL.Path)
			}
			if r.Header.Get("x-mcp-source") != "cli" {
				t.Fatalf("x-mcp-source = %q, want cli", r.Header.Get("x-mcp-source"))
			}
			body, _ := io.ReadAll(r.Body)
			seenBody = string(body)
			return &http.Response{StatusCode: http.StatusAccepted, Body: io.NopCloser(strings.NewReader(`{"ok":true}`))}, nil
		})},
	}
	err := client.RecordImagePublish(context.Background(), ImagePublishRecord{
		ImageRef:    "registry.example.com/team/demo:v1",
		SourceImage: "demo:v1",
		Mode:        "direct",
	})
	if err != nil {
		t.Fatalf("RecordImagePublish() error = %v", err)
	}
	if !strings.Contains(seenBody, `"image_ref":"registry.example.com/team/demo:v1"`) {
		t.Fatalf("body = %s", seenBody)
	}
}

func TestPushRegistryImageStreamsMultipartUpload(t *testing.T) {
	tmpDir := t.TempDir()
	tarPath := filepath.Join(tmpDir, "demo.tar")
	if err := os.WriteFile(tarPath, []byte("fake-image-tar"), 0o600); err != nil {
		t.Fatalf("write temp tar: %v", err)
	}

	var seenTarget string
	var seenScope string
	var seenFileBody string
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		apiPrefix: "/api/v1",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/runtime/registry/push" {
				t.Fatalf("path = %q, want registry push endpoint", r.URL.Path)
			}
			if got := r.Header.Get("authorization"); got != "Bearer token-1" {
				t.Fatalf("authorization = %q", got)
			}
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			seenTarget = r.FormValue("target")
			seenScope = r.FormValue("scope")
			file, _, err := r.FormFile("image_tar")
			if err != nil {
				t.Fatalf("FormFile(image_tar) error = %v", err)
			}
			defer file.Close()
			body, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("read image_tar: %v", err)
			}
			seenFileBody = string(body)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`))}, nil
		})},
	}

	if err := client.PushRegistryImage(context.Background(), tarPath, "registry.example.com/acme/demo:v1", "tenant"); err != nil {
		t.Fatalf("PushRegistryImage() error = %v", err)
	}
	if seenTarget != "registry.example.com/acme/demo:v1" {
		t.Fatalf("target = %q", seenTarget)
	}
	if seenScope != "tenant" {
		t.Fatalf("scope = %q", seenScope)
	}
	if seenFileBody != "fake-image-tar" {
		t.Fatalf("image_tar = %q", seenFileBody)
	}
}

func TestValidateCredentials(t *testing.T) {
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		apiPrefix: "/api/v1",
		http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.URL.Path != "/api/v1/auth/me" {
				t.Fatalf("path = %q, want auth/me endpoint", r.URL.Path)
			}
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"principal":{"role":"user"}}`))}, nil
		})},
	}
	if err := client.ValidateCredentials(context.Background()); err != nil {
		t.Fatalf("ValidateCredentials() error = %v", err)
	}
}

func TestHTTPAPIErrorPrefersMessageField(t *testing.T) {
	err := httpAPIError(http.StatusForbidden, []byte(`{"error":"forbidden_namespace","message":"forbidden namespace"}`))
	if got, want := err.Error(), "API 403: forbidden namespace"; got != want {
		t.Fatalf("httpAPIError() = %q, want %q", got, want)
	}
}

func TestResolvePlatformOrKubeModeSelection(t *testing.T) {
	t.Run("uses platform by default when auth is configured", func(t *testing.T) {
		t.Setenv(authfile.EnvAPIToken, "token-1")
		t.Setenv(authfile.EnvAPIURL, "https://platform.example.com")
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		client, useKube, err := ResolvePlatformOrKube(false)
		if err != nil {
			t.Fatalf("ResolvePlatformOrKube(false) error = %v", err)
		}
		if useKube {
			t.Fatal("ResolvePlatformOrKube(false) selected kube mode despite platform auth")
		}
		if client == nil {
			t.Fatal("ResolvePlatformOrKube(false) returned nil platform client")
		}
	})

	t.Run("explicit kube never requires platform auth", func(t *testing.T) {
		t.Setenv(authfile.EnvAPIToken, "")
		t.Setenv(authfile.EnvAPIURL, "")
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		client, useKube, err := ResolvePlatformOrKube(true)
		if err != nil {
			t.Fatalf("ResolvePlatformOrKube(true) error = %v", err)
		}
		if !useKube {
			t.Fatal("ResolvePlatformOrKube(true) did not select kube mode")
		}
		if client != nil {
			t.Fatal("ResolvePlatformOrKube(true) returned a platform client")
		}
	})

	t.Run("missing platform auth does not fall back to kube", func(t *testing.T) {
		t.Setenv(authfile.EnvAPIToken, "")
		t.Setenv(authfile.EnvAPIURL, "")
		t.Setenv("MCP_RUNTIME_CONFIG_DIR", t.TempDir())

		client, useKube, err := ResolvePlatformOrKube(false)
		if err == nil {
			t.Fatal("expected missing platform auth error")
		}
		if client != nil {
			t.Fatal("expected nil platform client")
		}
		if useKube {
			t.Fatal("missing platform auth fell back to kube mode")
		}
		if !errors.Is(err, authfile.ErrNotFound) {
			t.Fatalf("error should wrap authfile.ErrNotFound, got %v", err)
		}
		if !strings.Contains(err.Error(), "mcp-runtime auth login --api-url <platform-url>") {
			t.Fatalf("error missing login guidance: %v", err)
		}
		if !strings.Contains(err.Error(), "normal platform access") {
			t.Fatalf("error missing normal platform guidance: %v", err)
		}
		if !strings.Contains(err.Error(), "--use-kube") || !strings.Contains(err.Error(), "admin/operator Kubernetes access only") {
			t.Fatalf("error missing kube-mode boundary guidance: %v", err)
		}
	})
}

func TestPlatformClientTeamAndServerRoutes(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			if r.Header.Get("x-api-key") != "token-1" {
				t.Fatalf("x-api-key = %q, want token-1", r.Header.Get("x-api-key"))
			}
			switch {
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"teams":[{"slug":"core","name":"Core","namespace":"mcp-team-core"}]}`)),
				}, nil
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams/core":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"team":{"slug":"core","name":"Core","namespace":"mcp-team-core"}}`)),
				}, nil
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/teams/core/members":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"members":[{"team_slug":"core","team_namespace":"mcp-team-core","user_id":"user-1","email":"member@example.com","role":"member"}]}`)),
				}, nil
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/teams":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]string
				_ = json.Unmarshal(body, &payload)
				if payload["slug"] != "core" || payload["name"] != "Core Team" {
					t.Fatalf("create team payload = %#v", payload)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"team":{"slug":"core","name":"Core Team","namespace":"mcp-team-core"}}`)),
				}, nil
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/teams/core/users":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]string
				_ = json.Unmarshal(body, &payload)
				if payload["email"] != "member@example.com" || payload["password"] != "password123" {
					t.Fatalf("create user payload = %#v", payload)
				}
				return &http.Response{
					StatusCode: http.StatusCreated,
					Body:       io.NopCloser(strings.NewReader(`{"user":{"id":"user-1","email":"member@example.com","role":"user"},"membership":{"team_slug":"core","user_id":"user-1","role":"member"}}`)),
				}, nil
			case r.Method == http.MethodPut && r.URL.Path == "/api/v1/runtime/teams/core/members/user-1":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]string
				_ = json.Unmarshal(body, &payload)
				if payload["role"] != "member" {
					t.Fatalf("upsert team member payload = %#v", payload)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"membership":{"team_slug":"core","team_namespace":"mcp-team-core","user_id":"user-1","email":"member@example.com","role":"member"}}`)),
				}, nil
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/runtime/servers":
				if got := r.URL.Query().Get("namespace"); got != "" {
					t.Fatalf("list runtime servers namespace query = %q, want empty", got)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"servers":[]}`)),
				}, nil
			case r.Method == http.MethodGet && r.URL.Path == "/api/v1/auth/me":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"authenticated":true,"principal":{"role":"user","namespace":"mcp-team-core","teams":[{"slug":"core","namespace":"mcp-team-core"}]}}`)),
				}, nil
			case r.Method == http.MethodPost && r.URL.Path == "/api/v1/runtime/servers":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				if payload["name"] != "demo" {
					t.Fatalf("server name payload = %#v", payload)
				}
				if payload["scope"] != "tenant" {
					t.Fatalf("server scope payload = %#v", payload)
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"server":{"name":"demo","namespace":"mcp-team-core"}}`)),
				}, nil
			case r.Method == http.MethodDelete && r.URL.Path == "/api/v1/runtime/servers/mcp-team-core/demo":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"success":true}`)),
				}, nil
			default:
				t.Fatalf("unexpected route %s %s", r.Method, r.URL.Path)
				return nil, nil
			}
		}),
	}
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		http:      httpClient,
		apiPrefix: "/api/v1",
	}
	if _, err := client.ListTeams(context.Background()); err != nil {
		t.Fatalf("ListTeams() error = %v", err)
	}
	if _, err := client.GetTeam(context.Background(), "core"); err != nil {
		t.Fatalf("GetTeam() error = %v", err)
	}
	if _, err := client.CreateTeam(context.Background(), "core", "Core Team"); err != nil {
		t.Fatalf("CreateTeam() error = %v", err)
	}
	members, err := client.ListTeamMembers(context.Background(), "core")
	if err != nil {
		t.Fatalf("ListTeamMembers() error = %v", err)
	}
	if len(members) != 1 || members[0].Email != "member@example.com" {
		t.Fatalf("members = %#v", members)
	}
	created, err := client.CreateTeamUser(context.Background(), "core", "member@example.com", "password123", "member")
	if err != nil {
		t.Fatalf("CreateTeamUser() error = %v", err)
	}
	if created.Email != "member@example.com" || created.TeamSlug != "core" {
		t.Fatalf("created membership = %#v", created)
	}
	upserted, err := client.UpsertTeamMember(context.Background(), "core", "user-1", "member")
	if err != nil {
		t.Fatalf("UpsertTeamMember() error = %v", err)
	}
	if upserted.UserID != "user-1" || upserted.Role != "member" {
		t.Fatalf("upserted membership = %#v", upserted)
	}
	if _, err := client.ListRuntimeServers(context.Background(), ""); err != nil {
		t.Fatalf("ListRuntimeServers() error = %v", err)
	}
	principal, err := client.CurrentPrincipal(context.Background())
	if err != nil {
		t.Fatalf("CurrentPrincipal() error = %v", err)
	}
	if principal.Namespace != "mcp-team-core" || len(principal.Teams) != 1 || principal.Teams[0].Slug != "core" {
		t.Fatalf("principal = %#v", principal)
	}
	if _, err := client.ApplyRuntimeServerWithScope(context.Background(), "demo", "mcp-team-core", "tenant", mcpv1alpha1.MCPServerSpec{Image: "registry.example/core/demo", ImageTag: "latest"}); err != nil {
		t.Fatalf("ApplyRuntimeServer() error = %v", err)
	}
	if err := client.DeleteRuntimeServer(context.Background(), "mcp-team-core", "demo"); err != nil {
		t.Fatalf("DeleteRuntimeServer() error = %v", err)
	}
}

func TestPlatformClientAccessPatchRoutes(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			switch {
			case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/runtime/grants/mcp-servers/grant-a":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				if payload["disabled"] != true {
					t.Fatalf("grant patch payload = %#v", payload)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`))}, nil
			case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/runtime/sessions/mcp-servers/session-a":
				body, _ := io.ReadAll(r.Body)
				var payload map[string]any
				_ = json.Unmarshal(body, &payload)
				if payload["revoked"] != true {
					t.Fatalf("session patch payload = %#v", payload)
				}
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(`{"success":true}`))}, nil
			default:
				t.Fatalf("unexpected route %s %s", r.Method, r.URL.Path)
				return nil, nil
			}
		}),
	}
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		http:      httpClient,
		apiPrefix: "/api/v1",
	}
	if err := client.PatchGrant(context.Background(), "mcp-servers", "grant-a", true); err != nil {
		t.Fatalf("PatchGrant() error = %v", err)
	}
	if err := client.PatchSession(context.Background(), "mcp-servers", "session-a", true); err != nil {
		t.Fatalf("PatchSession() error = %v", err)
	}
}

func TestPlatformClientAccessPatchRoutesRejectNon2xx(t *testing.T) {
	httpClient := &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid grant state"}`)),
			}, nil
		}),
	}
	client := &PlatformClient{
		baseURL:   "https://platform.example.com",
		token:     "token-1",
		http:      httpClient,
		apiPrefix: "/api/v1",
	}
	if err := client.PatchGrant(context.Background(), "mcp-servers", "grant-a", true); err == nil {
		t.Fatal("PatchGrant() error = nil, want non-2xx failure")
	}
	if err := client.PatchSession(context.Background(), "mcp-servers", "session-a", true); err == nil {
		t.Fatal("PatchSession() error = nil, want non-2xx failure")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}
