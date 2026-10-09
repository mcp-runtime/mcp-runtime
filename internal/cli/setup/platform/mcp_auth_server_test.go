package platform

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"

	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
)

// mcpAuthManifestTemplate reads the shipped manifest. The path is derived from
// this file's compile-time location rather than the working directory, because
// other tests in this package change directories.
func mcpAuthManifestTemplate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file location")
	}
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..")
	raw, err := os.ReadFile(filepath.Join(repoRoot, "k8s", "23-mcp-auth-server.yaml"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	return string(raw)
}

func TestMCPAuthCustomIssuerRoutes(t *testing.T) {
	for _, issuerPath := range []string{"/auth", "/identity/mcp"} {
		manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), mcpAuthServerOptions{
			Image: "registry.example.com/mcp-auth-server:custom", IssuerURL: "https://customer.example.com" + issuerPath,
			TLSSecret: "auth-tls", SigningKeySecret: "auth-signing", ConnectorsFile: "connectors.json", Connector: "oidc",
		})
		if err != nil {
			t.Fatal(err)
		}
		for _, expected := range []string{"prefixes: [" + issuerPath + "]", "- path: " + issuerPath + "\n", "/.well-known/oauth-authorization-server" + issuerPath + "\n", "/.well-known/openid-configuration" + issuerPath + "\n", "image: registry.example.com/mcp-auth-server:custom"} {
			if !strings.Contains(manifest, expected) {
				t.Errorf("%s missing %q", issuerPath, expected)
			}
		}
		if strings.Contains(manifest, "path: /mcp-auth\n") {
			t.Fatal("hardcoded issuer route survived")
		}
		decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
		for {
			var object unstructured.Unstructured
			err := decoder.Decode(&object)
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("custom issuer produced invalid YAML: %v", err)
			}
		}
	}
}

func TestMCPAuthOperatorRBACIsLimitedToNamedDeployment(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file location")
	}
	path := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "k8s", "23-mcp-auth-rbac.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var role rbacv1.Role
	if err := decoder.Decode(&role); err != nil {
		t.Fatal(err)
	}
	if role.Kind != "Role" || role.Namespace != "mcp-platform" || role.Name != "mcp-runtime-operator-bundled-auth" || len(role.Rules) != 1 {
		t.Fatalf("unexpected bundled Auth Role: %+v", role)
	}
	rule := role.Rules[0]
	if len(rule.APIGroups) != 1 || rule.APIGroups[0] != "apps" ||
		len(rule.Resources) != 1 || rule.Resources[0] != "deployments" ||
		len(rule.ResourceNames) != 1 || rule.ResourceNames[0] != "mcp-auth-server" ||
		len(rule.Verbs) != 1 || rule.Verbs[0] != "patch" {
		t.Fatalf("bundled Auth Role is broader than the one Deployment patch: %+v", rule)
	}
	var binding rbacv1.RoleBinding
	if err := decoder.Decode(&binding); err != nil {
		t.Fatal(err)
	}
	if binding.Kind != "RoleBinding" || binding.Namespace != role.Namespace || binding.Name != role.Name ||
		binding.RoleRef.Kind != "Role" || binding.RoleRef.Name != role.Name || len(binding.Subjects) != 1 ||
		binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != "mcp-runtime-operator-controller-manager" ||
		binding.Subjects[0].Namespace != "mcp-runtime" {
		t.Fatalf("unexpected bundled Auth RoleBinding: %+v", binding)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		t.Fatalf("unexpected extra RBAC document: %v", err)
	}
}

func TestRenderMCPAuthServerManifestTestModeDefaults(t *testing.T) {
	manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), mcpAuthServerOptions{
		Image:    "registry.local/mcp-auth-server:dev",
		TestMode: true,
	})
	if err != nil {
		t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
	}
	for _, want := range []string{
		"image: registry.local/mcp-auth-server:dev",
		`{name: MCP_AUTH_ISSUER, value: "http://localhost:18080/mcp-auth"}`,
		`{name: MCP_AUTH_LOCAL_DEVELOPMENT, value: "true"}`,
		`{name: MCP_AUTH_LOCAL_TOKEN_EXCHANGE, value: "true"}`,
		`{name: MCP_AUTH_REQUIRE_HTTPS, value: "false"}`,
		`{name: MCP_AUTH_STORE, value: memory}`,
		"- host: localhost:18080",
		"kind: NetworkPolicy",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
	if strings.Contains(manifest, "tls:") {
		t.Error("test-mode manifest should not carry an ingress TLS block")
	}
}

// Outside --test-mode the local-development and local-token-exchange shortcuts
// must be off and HTTPS must be required, otherwise a public install would run
// a non-conforming authorization server.
func TestRenderMCPAuthServerManifestProductionHardensDevSwitches(t *testing.T) {
	manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), mcpAuthServerOptions{
		Image:            "registry.example.com/mcp-auth-server:1.0.0",
		IssuerURL:        "https://auth.example.com/mcp-auth",
		ResourceURLs:     []string{"https://mcp.example.com/demo/mcp"},
		TLSSecret:        "mcp-auth-tls",
		SigningKeySecret: "mcp-auth-signing-key",
		ConnectorsFile:   "connectors.json",
		Connector:        "keycloak",
	})
	if err != nil {
		t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
	}
	for _, want := range []string{
		`{name: MCP_AUTH_LOCAL_DEVELOPMENT, value: "false"}`,
		`{name: MCP_AUTH_LOCAL_TOKEN_EXCHANGE, value: "false"}`,
		`{name: MCP_AUTH_REQUIRE_HTTPS, value: "true"}`,
		`{name: MCP_AUTH_RESOURCE, value: "https://mcp.example.com/demo/mcp"}`,
		"- host: auth.example.com",
		`secretName: mcp-auth-tls`,
		`{name: MCP_AUTH_CONNECTOR, value: "keycloak"}`,
		`{name: MCP_AUTH_PRIVATE_KEY_FILE, value: /etc/mcp-auth-key/private-key.pem}`,
		`fsGroup: 65532`,
		`secret: {secretName: "mcp-auth-signing-key", defaultMode: 0440}`,
		`name: X-Forwarded-Proto`,
		`value: https`,
		`{name: MCP_AUTH_STORE, value: sqlite}`,
		"- secretRef: {name: mcp-auth-connector-secrets}",
		// Traefik must still route these; the authorization server answers
		// them itself now, so no rewrite middleware sits in front.
		"/.well-known/oauth-authorization-server/mcp-auth",
		"/.well-known/openid-configuration/mcp-auth",
		"mcp-platform-mcp-auth-server-strip-prefix@kubernetescrd",
	} {
		if !strings.Contains(manifest, want) {
			t.Errorf("manifest missing %q", want)
		}
	}
}

func TestRenderMCPAuthServerManifestProductionGuards(t *testing.T) {
	base := mcpAuthServerOptions{
		Image:            "img",
		IssuerURL:        "https://auth.example.com/mcp-auth",
		ResourceURLs:     []string{"https://mcp.example.com/demo/mcp"},
		TLSSecret:        "tls",
		SigningKeySecret: "mcp-auth-signing-key",
		ConnectorsFile:   "connectors.json",
		Connector:        "keycloak",
	}
	tests := []struct {
		name    string
		mutate  func(*mcpAuthServerOptions)
		wantErr string
	}{
		{"plaintext issuer", func(o *mcpAuthServerOptions) { o.IssuerURL = "http://auth.example.com/mcp-auth" }, "HTTPS issuer"},
		{"no connector", func(o *mcpAuthServerOptions) { o.ConnectorsFile = "" }, "connector file"},
		{"no tls secret", func(o *mcpAuthServerOptions) { o.TLSSecret = "" }, "TLS Secret"},
		{"no signing key", func(o *mcpAuthServerOptions) { o.SigningKeySecret = "" }, "signing key Secret"},
		{"plaintext resource", func(o *mcpAuthServerOptions) { o.ResourceURLs = []string{"http://mcp.example.com/demo/mcp"} }, "must use https"},
		{"relative resource", func(o *mcpAuthServerOptions) { o.ResourceURLs = []string{"/demo/mcp"} }, "absolute URL"},
		{"duplicate resource", func(o *mcpAuthServerOptions) {
			o.ResourceURLs = []string{"https://mcp.example.com/a/mcp", "https://mcp.example.com/a/mcp"}
		}, "listed twice"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			opts := base
			tc.mutate(&opts)
			_, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), opts)
			if err == nil {
				t.Fatalf("renderMCPAuthServerManifest() error = nil, want %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
			}
		})
	}
	base.ResourceURLs = nil
	if _, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), base); err != nil {
		t.Fatalf("renderMCPAuthServerManifest() with operator-managed resources failed: %v", err)
	}
}

// Test mode configures the OAuth route for the Go protocol example.
func TestMCPAuthResourceURLsTestModeCoversGoExample(t *testing.T) {
	got, err := mcpAuthResourceURLs(nil, "http://localhost:18080/mcp-auth", true)
	if err != nil {
		t.Fatalf("mcpAuthResourceURLs() error = %v", err)
	}
	want := []string{"http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp"}
	if len(got) != len(want) {
		t.Fatalf("mcpAuthResourceURLs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mcpAuthResourceURLs()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// The bundled local example's OAuth resource, derived from its route when no
// audience is pinned, must appear in the test-mode resource set.
func TestTestModeResourcesMatchBundledExampleAudiences(t *testing.T) {
	resources, err := mcpAuthResourceURLs(nil, "http://localhost:18080/mcp-auth", true)
	if err != nil {
		t.Fatalf("mcpAuthResourceURLs() error = %v", err)
	}
	indexed := map[string]bool{}
	for _, resource := range resources {
		indexed[resource] = true
	}
	_, thisFile, _, _ := runtime.Caller(0)
	examples := filepath.Join(filepath.Dir(thisFile), "..", "..", "..", "..", "examples")
	for _, name := range []string{filepath.Join("oauth-example-go-2025-11-25", ".mcp", "servers.yaml")} {
		raw, err := os.ReadFile(filepath.Join(examples, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		var document struct {
			Servers []struct {
				Route string `json:"route"`
				Auth  struct {
					Audience string `json:"audience"`
				} `json:"auth"`
			} `json:"servers"`
		}
		if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&document); err != nil {
			t.Fatalf("decode %s: %v", name, err)
		}
		if len(document.Servers) == 0 {
			t.Fatalf("%s has no servers", name)
		}
		for _, server := range document.Servers {
			audience := server.Auth.Audience
			if audience == "" {
				if server.Route == "" || !strings.HasPrefix(server.Route, "/") {
					t.Fatalf("%s must have a route for its derived audience", name)
				}
				audience = "http://localhost:18080" + server.Route
			}
			if !indexed[audience] {
				t.Errorf("%s audience %q is not in the test-mode resource set %v", name, audience, resources)
			}
		}
	}
}

func TestRenderMCPAuthConnectorSecretRejectsInvalidKey(t *testing.T) {
	if _, err := renderMCPAuthConnectorSecret(map[string]string{"BAD NAME": "x"}); err == nil {
		t.Fatal("renderMCPAuthConnectorSecret() error = nil, want invalid key error")
	}
	secret, err := renderMCPAuthConnectorSecret(map[string]string{"KEYCLOAK_CLIENT_SECRET": "s3cr3t\nvalue"})
	if err != nil {
		t.Fatalf("renderMCPAuthConnectorSecret() error = %v", err)
	}
	if !strings.Contains(secret, `KEYCLOAK_CLIENT_SECRET: "s3cr3t\nvalue"`) {
		t.Fatalf("secret did not quote the credential value: %s", secret)
	}
}

func TestReadMCPAuthConnectorRequiresExchangeClientID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "connectors.json")
	withoutExchange, err := json.Marshal(map[string]any{"keycloak": map[string]any{
		"client_id": "mcp-auth", "token_endpoint_auth_method": "none",
	}})
	if err != nil {
		t.Fatalf("marshal connector: %v", err)
	}
	if err := os.WriteFile(path, withoutExchange, 0600); err != nil {
		t.Fatalf("write connector: %v", err)
	}
	if _, _, err := readMCPAuthConnector(path, "keycloak"); err == nil || !strings.Contains(err.Error(), "exchange_client_id") {
		t.Fatalf("readMCPAuthConnector() error = %v, want exchange_client_id validation", err)
	}
}

// A placeholder that survives rendering would be applied to the cluster as a
// literal container env value.
func TestUnresolvedManifestPlaceholders(t *testing.T) {
	if remaining := unresolvedManifestPlaceholders("env: MCP_AUTH_ISSUER_VALUE"); len(remaining) != 1 {
		t.Fatalf("unresolvedManifestPlaceholders() = %v, want one entry", remaining)
	}
	if remaining := unresolvedManifestPlaceholders("env: resolved"); len(remaining) != 0 {
		t.Fatalf("unresolvedManifestPlaceholders() = %v, want none", remaining)
	}
}

// Decode the rendered manifest exactly as the apply path does. This catches
// type errors the string assertions cannot see: an unquoted true/false renders
// as a YAML boolean, and the API server rejects it on EnvVar.Value, which is a
// string.
func TestRenderMCPAuthServerManifestDecodesAsKubernetesObjects(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts mcpAuthServerOptions
	}{
		{"test mode", mcpAuthServerOptions{Image: "img", TestMode: true}},
		{"production", mcpAuthServerOptions{
			Image:            "img",
			IssuerURL:        "https://auth.example.com/mcp-auth",
			ResourceURLs:     []string{"https://mcp.example.com/demo/mcp"},
			TLSSecret:        "tls",
			SigningKeySecret: "mcp-auth-signing-key",
			ConnectorsFile:   "connectors.json",
			Connector:        "keycloak",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), tc.opts)
			if err != nil {
				t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
			}
			decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
			kinds := map[string]bool{}
			for {
				obj := &unstructured.Unstructured{}
				err := decoder.Decode(obj)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatalf("decode manifest: %v", err)
				}
				if obj.Object == nil {
					continue
				}
				kinds[obj.GetKind()] = true
				if obj.GetKind() != "Deployment" {
					continue
				}
				containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
				if err != nil || !found {
					t.Fatalf("containers not found: %v", err)
				}
				for _, container := range containers {
					envs, _ := container.(map[string]any)["env"].([]any)
					for _, entry := range envs {
						env, _ := entry.(map[string]any)
						if _, ok := env["value"].(string); !ok {
							t.Errorf("env %v value is %T, want string; Kubernetes rejects non-string EnvVar.Value", env["name"], env["value"])
						}
					}
				}
			}
			for _, want := range []string{"Deployment", "Service", "Ingress", "NetworkPolicy", "Middleware"} {
				if !kinds[want] {
					t.Errorf("manifest is missing a %s", want)
				}
			}
		})
	}
}

// Every resolved resource must reach the authorization server. Rendering only
// the first one made a deployment that fronts two MCP servers mint tokens for
// one of them and answer /authorize with "resource is not recognized" for the
// other - the exact failure an MCP client hits when it sends the RFC 8707
// resource parameter for the second server.
func TestRenderMCPAuthServerManifestCarriesEveryResource(t *testing.T) {
	manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), mcpAuthServerOptions{
		Image:     "registry.example.com/mcp-auth-server:1.0.0",
		IssuerURL: "https://auth.example.com/mcp-auth",
		ResourceURLs: []string{
			"https://mcp.example.com/ping/mcp",
			"https://mcp.example.com/echo/mcp",
		},
		TLSSecret:        "mcp-auth-tls",
		SigningKeySecret: "mcp-auth-signing-key",
		ConnectorsFile:   "connectors.json",
		Connector:        "keycloak",
	})
	if err != nil {
		t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
	}
	want := `{name: MCP_AUTH_RESOURCES, value: "https://mcp.example.com/ping/mcp,https://mcp.example.com/echo/mcp"}`
	if !strings.Contains(manifest, want) {
		t.Fatalf("manifest does not carry every resource.\nwant: %s\ngot:\n%s", want, manifest)
	}
	// The singular stays for an older image that only reads it.
	if !strings.Contains(manifest, `{name: MCP_AUTH_RESOURCE, value: "https://mcp.example.com/ping/mcp"}`) {
		t.Fatalf("manifest dropped the compatibility singular:\n%s", manifest)
	}
}

// Test mode resolves all shipped SDK fixtures, so each must be served.
func TestRenderMCPAuthServerManifestTestModeServesGoExample(t *testing.T) {
	manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), mcpAuthServerOptions{
		Image:     "registry.example.com/mcp-auth-server:1.0.0",
		IssuerURL: "http://localhost:18080/mcp-auth",
		TestMode:  true,
	})
	if err != nil {
		t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
	}
	want := `{name: MCP_AUTH_RESOURCES, value: "http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp"}`
	if !strings.Contains(manifest, want) {
		t.Fatalf("test mode must serve every SDK fixture.\nwant: %s\ngot:\n%s", want, manifest)
	}
}

func TestMCPAuthTLSHost(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		plan    setupplan.Plan
		want    string
		wantErr string
	}{
		{
			name: "production issuer host",
			plan: setupplan.Plan{DeployMCPAuthServer: true, MCPAuthIssuerURL: "https://auth.example.com/mcp-auth"},
			want: "auth.example.com",
		},
		{
			name:    "port rejected",
			plan:    setupplan.Plan{DeployMCPAuthServer: true, MCPAuthIssuerURL: "https://auth.example.com:8443/mcp-auth"},
			wantErr: "must not include a port",
		},
		{
			name: "test mode skips public host",
			plan: setupplan.Plan{DeployMCPAuthServer: true, TestMode: true},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mcpAuthTLSHost(tc.plan)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("mcpAuthTLSHost() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("mcpAuthTLSHost() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("mcpAuthTLSHost() = %q, want %q", got, tc.want)
			}
		})
	}
}

// mcpAuthDeploymentPullSecrets decodes the rendered manifest and returns the
// Deployment pod spec's imagePullSecrets names.
func mcpAuthDeploymentPullSecrets(t *testing.T, manifest string) []string {
	t.Helper()
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader([]byte(manifest)), 4096)
	for {
		obj := &unstructured.Unstructured{}
		err := decoder.Decode(obj)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("decode manifest: %v", err)
		}
		if obj.GetKind() != "Deployment" {
			continue
		}
		entries, _, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "imagePullSecrets")
		if err != nil {
			t.Fatalf("read imagePullSecrets: %v", err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.(map[string]any)["name"].(string))
		}
		return names
	}
	t.Fatal("manifest has no Deployment")
	return nil
}

// Issue #623: an mcp-auth image hosted in the platform registry needs the
// platform pull Secret, the same as every other platform workload.
func TestRenderMCPAuthServerManifestInjectsImagePullSecret(t *testing.T) {
	production := mcpAuthServerOptions{
		Image:            "registry.example.com/mcp-auth-server:1.0.0",
		IssuerURL:        "https://auth.example.com/mcp-auth",
		TLSSecret:        "tls",
		SigningKeySecret: "mcp-auth-signing-key",
		ConnectorsFile:   "connectors.json",
		Connector:        "keycloak",
	}
	withSecret := production
	withSecret.ImagePullSecret = " mcp-runtime-registry-pull "
	for _, tc := range []struct {
		name string
		opts mcpAuthServerOptions
		want []string
	}{
		{name: "test mode without secret", opts: mcpAuthServerOptions{Image: "img", TestMode: true}},
		{name: "production without secret", opts: production},
		{name: "test mode with secret", opts: mcpAuthServerOptions{Image: "img", TestMode: true, ImagePullSecret: "mcp-runtime-registry-pull"}, want: []string{"mcp-runtime-registry-pull"}},
		{name: "production with secret", opts: withSecret, want: []string{"mcp-runtime-registry-pull"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest, err := renderMCPAuthServerManifest(mcpAuthManifestTemplate(t), tc.opts)
			if err != nil {
				t.Fatalf("renderMCPAuthServerManifest() error = %v", err)
			}
			got := mcpAuthDeploymentPullSecrets(t, manifest)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("imagePullSecrets = %v, want %v", got, tc.want)
			}
			for _, kind := range []string{"kind: Deployment", "kind: Service", "kind: Ingress", "kind: NetworkPolicy"} {
				if !strings.Contains(manifest, kind) {
					t.Fatalf("rendered manifest lost %q", kind)
				}
			}
		})
	}
}

func TestResolveMCPAuthImagePullSecret(t *testing.T) {
	type call struct{ namespace, name, registry, username, password string }
	const image = "registry.example.com/mcp-auth-server:1"
	ext := func(url, user, pass string) *config.ExternalRegistryConfig {
		return &config.ExternalRegistryConfig{URL: url, Username: user, Password: pass}
	}
	for _, tc := range []struct {
		name          string
		src           mcpAuthPullSecretSources
		bundled       string
		bundledErr    error
		externalErr   error
		want          string
		wantErr       bool
		wantExternal  *call
		wantBundledNS string
	}{
		{name: "override wins", src: mcpAuthPullSecretSources{Override: " custom-pull ", ExternalRegistry: ext("ext.example.com", "u", "p")}, want: "custom-pull"},
		{
			name:         "external registry with credentials",
			src:          mcpAuthPullSecretSources{ExternalRegistry: ext("ext.example.com", "u", "p")},
			want:         defaultRegistrySecretName,
			wantExternal: &call{"mcp-platform", defaultRegistrySecretName, "ext.example.com", "u", "p"},
		},
		{name: "external registry error", src: mcpAuthPullSecretSources{ExternalRegistry: ext("ext.example.com", "u", "")}, externalErr: errors.New("boom"), wantErr: true},
		{name: "external registry without credentials uses bundled rules", src: mcpAuthPullSecretSources{ExternalRegistry: ext("ext.example.com", "", "")}, bundled: defaultRegistrySecretName, want: defaultRegistrySecretName, wantBundledNS: "mcp-platform"},
		{name: "image in bundled public registry", bundled: defaultRegistrySecretName, want: defaultRegistrySecretName, wantBundledNS: "mcp-platform"},
		{name: "image not in platform registry", wantBundledNS: "mcp-platform"},
		{name: "bundled error", bundledErr: errors.New("boom"), wantErr: true, wantBundledNS: "mcp-platform"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotExternal *call
			gotBundledNS := ""
			src := tc.src
			src.EnsureExternal = func(namespace, name, registry, username, password string) error {
				gotExternal = &call{namespace, name, registry, username, password}
				return tc.externalErr
			}
			src.EnsureBundled = func(namespace string, images []string) (string, error) {
				gotBundledNS = namespace
				if len(images) != 1 || images[0] != image {
					t.Fatalf("bundled candidates = %v, want [%s]", images, image)
				}
				return tc.bundled, tc.bundledErr
			}
			got, err := resolveMCPAuthImagePullSecret(image, "mcp-platform", src)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Fatalf("secret = %q, want %q", got, tc.want)
			}
			if tc.wantExternal != nil && (gotExternal == nil || *gotExternal != *tc.wantExternal) {
				t.Fatalf("external ensure = %+v, want %+v", gotExternal, tc.wantExternal)
			}
			if gotBundledNS != tc.wantBundledNS {
				t.Fatalf("bundled namespace = %q, want %q", gotBundledNS, tc.wantBundledNS)
			}
		})
	}
}

func TestNewMCPAuthServerOptionsCarriesPullSecret(t *testing.T) {
	opts := newMCPAuthServerOptions(setupplan.Plan{MCPAuthServerImage: "img", TestMode: true}, "pull")
	if opts.Image != "img" || !opts.TestMode || opts.ImagePullSecret != "pull" {
		t.Fatalf("options = %+v", opts)
	}
}
