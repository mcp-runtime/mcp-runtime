package platform

import (
	"context"
	"fmt"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"
	"os"
	"slices"
	"strings"
)

func setupImageTag() string {
	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		return "latest"
	}
	return setupImageTagResolver()
}

func resolveSetupImagePlatform(kubectl core.KubectlRunner) (string, error) {
	explicitPlatform, err := explicitSetupImagePlatform()
	if err != nil {
		return "", err
	}

	cacheKey := fmt.Sprintf("%T:%p:%s", kubectl, kubectl, explicitPlatform)
	setupImagePlatformCache.Lock()
	entry := setupImagePlatformCache.entries[cacheKey]
	if entry == nil {
		entry = &setupImagePlatformCacheEntry{}
		setupImagePlatformCache.entries[cacheKey] = entry
	}
	setupImagePlatformCache.Unlock()

	entry.once.Do(func() {
		entry.platform, entry.err = resolveSetupImagePlatformUncached(kubectl, explicitPlatform)
	})
	return entry.platform, entry.err
}

func resolveSetupImagePlatformClientGo() (string, error) {
	explicitPlatform, err := explicitSetupImagePlatform()
	if err != nil {
		return "", err
	}
	archs, err := clusterNodeArchitecturesClientGo()
	return resolveSetupImagePlatformFromArchitectures(archs, explicitPlatform, err)
}

func explicitSetupImagePlatform() (string, error) {
	if core.DefaultCLIConfig == nil {
		return "", nil
	}
	platform := strings.TrimSpace(core.DefaultCLIConfig.ImagePlatform)
	if platform == "" {
		return "", nil
	}
	return validateSetupImagePlatform(platform)
}

func resetSetupImagePlatformCacheForTest() {
	setupImagePlatformCache.Lock()
	setupImagePlatformCache.entries = map[string]*setupImagePlatformCacheEntry{}
	setupImagePlatformCache.Unlock()
}

func resolveSetupImagePlatformUncached(kubectl core.KubectlRunner, explicitPlatform string) (string, error) {
	archs, err := clusterNodeArchitectures(kubectl)
	return resolveSetupImagePlatformFromArchitectures(archs, explicitPlatform, err)
}

func resolveSetupImagePlatformFromArchitectures(archs []string, explicitPlatform string, err error) (string, error) {
	if err != nil {
		if explicitPlatform != "" {
			return explicitPlatform, nil
		}
		return "", err
	}
	if len(archs) == 0 {
		return "", core.NewWithBase(core.ErrSetupImagePlatformNoNodeArchitectures, "could not resolve setup image platform: no Kubernetes node architectures were reported; set MCP_IMAGE_PLATFORM=linux/amd64 or linux/arm64")
	}
	if len(archs) > 1 {
		return "", core.NewWithBase(core.ErrSetupImagePlatformMixedNodeArchitectures, fmt.Sprintf("mixed Kubernetes node architectures detected (%s); setup-built images are single-platform today, so set up homogeneous nodes or prebuild multi-arch images before running setup", strings.Join(archs, ", ")))
	}
	if explicitPlatform != "" {
		if arch := strings.TrimPrefix(explicitPlatform, "linux/"); arch != archs[0] {
			return "", core.NewWithBase(core.ErrSetupImagePlatformMismatch, fmt.Sprintf("MCP_IMAGE_PLATFORM %q does not match Kubernetes node architecture %q", explicitPlatform, archs[0]))
		}
		return explicitPlatform, nil
	}
	return validateSetupImagePlatform("linux/" + archs[0])
}

func validateSetupImagePlatform(platform string) (string, error) {
	platform = strings.TrimSpace(platform)
	parts := strings.Split(platform, "/")
	if len(parts) != 2 || parts[0] != "linux" {
		return "", core.NewWithBase(core.ErrSetupImagePlatformInvalid, fmt.Sprintf("invalid MCP_IMAGE_PLATFORM %q; expected linux/amd64 or linux/arm64", platform))
	}
	switch parts[1] {
	case "amd64", "arm64":
		return platform, nil
	default:
		return "", core.NewWithBase(core.ErrSetupImagePlatformUnsupported, fmt.Sprintf("unsupported MCP_IMAGE_PLATFORM %q; expected linux/amd64 or linux/arm64", platform))
	}
}

func clusterNodeArchitectures(kubectl core.KubectlRunner) ([]string, error) {
	if kubectl == nil {
		return nil, core.NewWithBase(core.ErrSetupImagePlatformKubectlNil, "could not resolve setup image platform: kubectl runner is nil")
	}
	cmd, err := kubectl.CommandArgs([]string{"get", "nodes", "-o", "jsonpath={range .items[*]}{.status.nodeInfo.architecture}{\"\\n\"}{end}"})
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupInspectNodeArchitecturesFailed, err, fmt.Sprintf("could not inspect Kubernetes node architectures: %v", err))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		if detail := strings.TrimSpace(string(out)); detail != "" {
			return nil, core.WrapWithBase(core.ErrSetupInspectNodeArchitecturesFailed, err, fmt.Sprintf("could not inspect Kubernetes node architectures: %v: %s", err, detail))
		}
		return nil, core.WrapWithBase(core.ErrSetupInspectNodeArchitecturesFailed, err, fmt.Sprintf("could not inspect Kubernetes node architectures: %v", err))
	}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(string(out), "\n") {
		arch := strings.TrimSpace(line)
		if arch == "" {
			continue
		}
		seen[arch] = struct{}{}
	}
	archs := make([]string, 0, len(seen))
	for arch := range seen {
		archs = append(archs, arch)
	}
	slices.Sort(archs)
	return archs, nil
}

func clusterNodeArchitecturesClientGo() ([]string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupInspectNodeArchitecturesFailed, err, fmt.Sprintf("could not create Kubernetes client to inspect node architectures: %v", err))
	}
	archs, err := k8sclient.NodeArchitectures(context.Background(), clients)
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupInspectNodeArchitecturesFailed, err, fmt.Sprintf("could not inspect Kubernetes node architectures: %v", err))
	}
	return archs, nil
}
