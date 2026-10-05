package resolve

import (
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"
)

const registryServiceDNS = "registry.registry.svc.cluster.local"

type Config struct {
	RegistryEndpoint        string
	DefaultRegistryEndpoint string
	RegistryIngressHost     string
	DefaultRegistryHost     string
	RegistryPort            int
}

type OutputCommand interface {
	Output() ([]byte, error)
}

type KubectlCommand func(args []string) (OutputCommand, error)
type CommandFactory func(name string, args []string) (OutputCommand, error)

// PlatformURL resolves the registry host:port used for public/user-facing image
// names.
func PlatformURL(logger *zap.Logger, kubectl KubectlCommand, cfg Config) string {
	if host := strings.TrimSpace(cfg.RegistryIngressHost); host != "" &&
		(host != cfg.DefaultRegistryHost || registryHostExplicitlyConfigured()) {
		return host
	}

	if endpoint := strings.TrimSpace(cfg.RegistryEndpoint); endpoint != "" &&
		(endpoint != cfg.DefaultRegistryEndpoint || registryEndpointExplicitlyConfigured()) {
		return endpoint
	}

	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		portValue, portErr := servicePort(kubectl)
		if portErr == nil && portValue != "" {
			return fmt.Sprintf("%s:%s", registryServiceDNS, portValue)
		}
		if logger != nil {
			logger.Warn("Could not detect registry service port in test mode, using default service DNS:port")
		}
		return fmt.Sprintf("%s:%d", registryServiceDNS, cfg.RegistryPort)
	}

	if ingressHost, ingressErr := registryIngressHost(kubectl); ingressErr == nil && ingressHost != "" &&
		ingressHost != cfg.DefaultRegistryHost {
		return ingressHost
	}

	portValue, portErr := servicePort(kubectl)
	if portErr == nil && portValue != "" {
		return fmt.Sprintf("%s:%s", registryServiceDNS, portValue)
	}

	if logger != nil {
		logger.Warn("Could not detect registry ingress host or service port, using default service DNS:port")
	}
	return fmt.Sprintf("%s:%d", registryServiceDNS, cfg.RegistryPort)
}

// InternalPlatformURL resolves the bundled registry host:port for platform pods
// rendered by setup. It intentionally ignores public ingress hosts derived from
// MCP_PLATFORM_DOMAIN/MCP_REGISTRY_INGRESS_HOST so operator and platform pods do
// not need anonymous or pull-secret access to the public registry route.
func InternalPlatformURL(logger *zap.Logger, kubectl KubectlCommand, cfg Config) string {
	if endpoint := strings.TrimSpace(cfg.RegistryEndpoint); endpoint != "" &&
		(registryEndpointExplicitlyConfigured() ||
			(endpoint != cfg.DefaultRegistryEndpoint && endpoint != strings.TrimSpace(cfg.RegistryIngressHost))) {
		return endpoint
	}

	if os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" {
		portValue, portErr := servicePort(kubectl)
		if portErr == nil && portValue != "" {
			return fmt.Sprintf("%s:%s", registryServiceDNS, portValue)
		}
		if logger != nil {
			logger.Warn("Could not detect registry service port in test mode, using default service DNS:port")
		}
		return fmt.Sprintf("%s:%d", registryServiceDNS, cfg.RegistryPort)
	}

	ip, ipErr := serviceClusterIP(kubectl)
	portValue, portErr := servicePort(kubectl)
	if ipErr == nil && ip != "" && portErr == nil && portValue != "" {
		return fmt.Sprintf("%s:%s", ip, portValue)
	}
	if portErr == nil && portValue != "" {
		return fmt.Sprintf("%s:%s", registryServiceDNS, portValue)
	}

	if logger != nil {
		logger.Warn("Could not detect internal registry service port, using default service DNS:port")
	}
	return fmt.Sprintf("%s:%d", registryServiceDNS, cfg.RegistryPort)
}

// GitTag returns a short git SHA when available, otherwise "latest".
func GitTag(command CommandFactory) string {
	if command == nil {
		return "latest"
	}
	cmd, err := command("git", []string{"rev-parse", "--short", "HEAD"})
	if err == nil {
		sha, execErr := cmd.Output()
		if execErr == nil && len(sha) > 0 {
			return strings.TrimSpace(string(sha))
		}
	}
	return "latest"
}

func registryEndpointExplicitlyConfigured() bool {
	if value, ok := os.LookupEnv("MCP_REGISTRY_ENDPOINT"); ok && strings.TrimSpace(value) != "" {
		return true
	}
	if value, ok := os.LookupEnv("MCP_REGISTRY_HOST"); ok && strings.TrimSpace(value) != "" {
		return true
	}
	return false
}

func registryHostExplicitlyConfigured() bool {
	if value, ok := os.LookupEnv("MCP_REGISTRY_INGRESS_HOST"); ok && strings.TrimSpace(value) != "" {
		return true
	}
	if value, ok := os.LookupEnv("MCP_PLATFORM_DOMAIN"); ok && strings.TrimSpace(value) != "" {
		return true
	}
	return false
}

func serviceClusterIP(kubectl KubectlCommand) (string, error) {
	if kubectl == nil {
		return "", fmt.Errorf("kubectl is nil")
	}
	cmd, err := kubectl([]string{"get", "service", "registry", "-n", "registry", "-o", "jsonpath={.spec.clusterIP}"})
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func registryIngressHost(kubectl KubectlCommand) (string, error) {
	if kubectl == nil {
		return "", fmt.Errorf("kubectl is nil")
	}
	cmd, err := kubectl([]string{"get", "ingress", "registry", "-n", "registry", "-o", "jsonpath={.spec.rules[0].host}"})
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func servicePort(kubectl KubectlCommand) (string, error) {
	if kubectl == nil {
		return "", fmt.Errorf("kubectl is nil")
	}
	cmd, err := kubectl([]string{"get", "service", "registry", "-n", "registry", "-o", "jsonpath={.spec.ports[0].port}"})
	if err != nil {
		return "", err
	}
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
