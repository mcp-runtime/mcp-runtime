package registrycompat

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"mcp-runtime/internal/cli/core"
)

func contains(args []string, needle string) bool {
	for _, arg := range args {
		if strings.Contains(arg, needle) {
			return true
		}
	}
	return false
}

func TestOverlayPath_K3sKubeletVersion(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			if contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}") {
				return &core.MockCommand{OutputData: []byte("v1.35.0+k3s1")}
			}
			return &core.MockCommand{}
		},
	})
	if got := OverlayPath(kubectl); got != K3sOverlaySubPath {
		t.Fatalf("OverlayPath() = %q, want %q", got, K3sOverlaySubPath)
	}
}

func TestOverlayPath_KubeSystemTraefik(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
				return &core.MockCommand{OutputData: []byte("v1.30.0")}
			case contains(spec.Args, "app.kubernetes.io/name=traefik"):
				return &core.MockCommand{OutputData: []byte("traefik")}
			case contains(spec.Args, "jsonpath={.items[*].metadata.name}"):
				return &core.MockCommand{OutputData: []byte("worker-1")}
			case contains(spec.Args, "current-context"):
				return &core.MockCommand{OutputData: []byte("prod")}
			}
			return &core.MockCommand{}
		},
	})
	if got := OverlayPath(kubectl); got != K3sOverlaySubPath {
		t.Fatalf("OverlayPath() = %q, want %q", got, K3sOverlaySubPath)
	}
}

func TestOverlayPath_PortableCluster(t *testing.T) {
	kubectl := core.NewTestKubectlClient(&core.MockExecutor{
		CommandFunc: func(spec core.ExecSpec) *core.MockCommand {
			switch {
			case contains(spec.Args, "jsonpath={.items[*].status.nodeInfo.kubeletVersion}"):
				return &core.MockCommand{OutputData: []byte("v1.30.0")}
			case contains(spec.Args, "app.kubernetes.io/name=traefik"):
				return &core.MockCommand{OutputData: []byte("")}
			case contains(spec.Args, "jsonpath={.items[*].metadata.name}"):
				return &core.MockCommand{OutputData: []byte("worker-1")}
			case contains(spec.Args, "current-context"):
				return &core.MockCommand{OutputData: []byte("prod")}
			}
			return &core.MockCommand{}
		},
	})
	if got := OverlayPath(kubectl); got != "" {
		t.Fatalf("OverlayPath() = %q, want empty", got)
	}
}

func TestBaseNetworkPolicyIsDistroNeutral(t *testing.T) {
	raw := readRepoFile(t, "config/registry/base/networkpolicy.yaml")
	for _, forbidden := range []string{
		"registry-allow-ingress-k3s",
		"app.kubernetes.io/name: traefik",
		"cidr: 10.0.0.0/8",
		"k3s",
	} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("base networkpolicy should not contain %q", forbidden)
		}
	}
}

func TestK3sOverlayContainsCompatRules(t *testing.T) {
	raw := readRepoFile(t, "config/registry/overlays/compatibility/k3s/networkpolicy-k3s-compat.yaml")
	for _, want := range []string{
		"registry-allow-ingress-k3s",
		"app.kubernetes.io/name: traefik",
	} {
		if !strings.Contains(raw, want) {
			t.Fatalf("k3s compat networkpolicy missing %q", want)
		}
	}
	if strings.Contains(raw, "ipBlock:") {
		t.Fatal("k3s registry policy must not allow pod CIDRs that include tenant workloads")
	}
}

func TestResolveOverlayPath(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		overlay  string
		want     string
	}{
		{
			name:     "default config root",
			manifest: "config/registry",
			overlay:  K3sOverlaySubPath,
			want:     filepath.Clean("config/registry/overlays/compatibility/k3s"),
		},
		{
			name:     "tls overlay path",
			manifest: "config/registry/overlays/tls",
			overlay:  K3sOverlaySubPath,
			want:     filepath.Clean("config/registry/overlays/compatibility/k3s"),
		},
		{
			name:     "base path",
			manifest: "config/registry/base",
			overlay:  K3sOverlaySubPath,
			want:     filepath.Clean("config/registry/overlays/compatibility/k3s"),
		},
		{
			name:     "empty manifest path",
			manifest: "",
			overlay:  K3sOverlaySubPath,
			want:     filepath.Clean("config/registry/overlays/compatibility/k3s"),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ResolveOverlayPath(tc.manifest, tc.overlay); got != tc.want {
				t.Fatalf("ResolveOverlayPath(%q, %q) = %q, want %q", tc.manifest, tc.overlay, got, tc.want)
			}
		})
	}
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	root := moduleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(data)
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
