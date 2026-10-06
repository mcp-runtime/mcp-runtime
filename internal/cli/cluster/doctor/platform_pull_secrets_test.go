package doctor

import (
	"errors"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func pullSecretDeployments(items ...string) string {
	return `{"items":[` + strings.Join(items, ",") + `]}`
}

func pullSecretDeployment(name, image, serviceAccount string, pullSecrets bool) string {
	spec := `"containers":[{"name":"main","image":"` + image + `"}]`
	if serviceAccount != "" {
		spec += `,"serviceAccountName":"` + serviceAccount + `"`
	}
	if pullSecrets {
		spec += `,"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]`
	}
	return `{"metadata":{"name":"` + name + `"},"spec":{"template":{"spec":{` + spec + `}}}}`
}

func TestCheckPlatformPullSecrets(t *testing.T) {
	const host = "registry.example.org"
	for _, tc := range []struct {
		name         string
		configHost   string
		configDomain string
		deployments  map[string]string
		accounts     map[string]string
		listErr      error
		wantOK       bool
		wantDetail   string
	}{
		{name: "no public registry configured", configHost: "registry.local", wantOK: true, wantDetail: "skipping"},
		{
			name:       "mcp-auth from platform registry without pull secret",
			configHost: host,
			deployments: map[string]string{
				"mcp-platform": pullSecretDeployments(
					pullSecretDeployment("mcp-platform-api", host+"/mcp-platform-api:v1", "", true),
					pullSecretDeployment("mcp-auth-server", host+"/mcp-auth-server:v1", "", false),
				),
			},
			accounts:   map[string]string{"mcp-platform": `{"items":[{"metadata":{"name":"default"}}]}`},
			wantDetail: "mcp-platform/mcp-auth-server",
		},
		{
			name:         "platform domain derives registry host",
			configDomain: "example.org",
			deployments: map[string]string{
				"mcp-platform": pullSecretDeployments(pullSecretDeployment("mcp-auth-server", host+"/mcp-auth-server:v1", "", false)),
			},
			wantDetail: "mcp-platform/mcp-auth-server",
		},
		{
			name:       "service account supplies pull secret",
			configHost: host,
			deployments: map[string]string{
				"mcp-platform": pullSecretDeployments(pullSecretDeployment("mcp-auth-server", host+"/mcp-auth-server:v1", "auth", false)),
			},
			accounts: map[string]string{"mcp-platform": `{"items":[{"metadata":{"name":"default"}},{"metadata":{"name":"auth"},"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]}]}`},
			wantOK:   true,
		},
		{
			name:       "public image needs no pull secret",
			configHost: host,
			deployments: map[string]string{
				"mcp-platform": pullSecretDeployments(pullSecretDeployment("mcp-auth-server", "docker.io/example/mcp-auth-server:v1", "", false)),
			},
			wantOK: true,
		},
		{
			name:       "all platform registry deployments have pull secrets",
			configHost: host,
			deployments: map[string]string{
				"mcp-runtime":  pullSecretDeployments(pullSecretDeployment("mcp-runtime-operator-controller-manager", host+"/operator:v1", "", true)),
				"mcp-platform": pullSecretDeployments(pullSecretDeployment("mcp-auth-server", host+"/mcp-auth-server:v1", "", true)),
			},
			wantOK:     true,
			wantDetail: "2 platform deployment(s)",
		},
		{name: "deployment list fails", configHost: host, listErr: errors.New("forbidden"), wantDetail: "forbidden"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mock := &core.MockExecutor{CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
				args := strings.Join(spec.Args, " ")
				namespace := ""
				for i, arg := range spec.Args {
					if arg == "-n" && i+1 < len(spec.Args) {
						namespace = spec.Args[i+1]
					}
				}
				switch {
				case strings.Contains(args, "MCP_REGISTRY_INGRESS_HOST"):
					return &core.MockCommand{OutputData: []byte(tc.configHost)}
				case strings.Contains(args, "MCP_PLATFORM_DOMAIN"):
					return &core.MockCommand{OutputData: []byte(tc.configDomain)}
				case strings.HasPrefix(args, "get deployments"):
					if tc.listErr != nil {
						return &core.MockCommand{OutputErr: tc.listErr}
					}
					out := tc.deployments[namespace]
					if out == "" {
						out = `{"items":[]}`
					}
					return &core.MockCommand{OutputData: []byte(out)}
				case strings.HasPrefix(args, "get serviceaccounts"):
					out := tc.accounts[namespace]
					if out == "" {
						out = `{"items":[]}`
					}
					return &core.MockCommand{OutputData: []byte(out)}
				}
				t.Fatalf("unexpected command: %v", spec.Args)
				return nil
			}}
			check := checkPlatformPullSecrets(core.NewTestKubectlClient(mock))
			if check.OK != tc.wantOK {
				t.Fatalf("OK = %v, want %v; detail: %s", check.OK, tc.wantOK, check.Detail)
			}
			if tc.wantDetail != "" && !strings.Contains(check.Detail, tc.wantDetail) {
				t.Fatalf("detail %q does not contain %q", check.Detail, tc.wantDetail)
			}
			if !check.OK && check.Remedy == "" {
				t.Fatal("failing check must carry a remedy")
			}
		})
	}
}
