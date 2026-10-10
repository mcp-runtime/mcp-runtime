package platform

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/registry/config"
	"mcp-runtime/internal/cli/registry/ref"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

func ValidateRegistryMode(mode string) error {
	if _, ok := setupplan.NormalizeRegistryMode(mode); ok {
		return nil
	}
	cause := core.NewWithBase(core.ErrSetupInvalidRegistryMode, fmt.Sprintf("invalid registry mode %q", mode))
	return core.WrapWithBase(
		core.ErrFieldRequired,
		cause,
		"invalid --registry-mode; expected auto, bundled-http, bundled-https, or external",
	)
}

func ValidateRegistryTLSMode(mode string, tlsEnabled bool, acmeEmail string) error {
	normalized, ok := setupplan.NormalizeRegistryMode(mode)
	if !ok {
		return nil
	}
	if normalized == setupplan.RegistryModeBundledHTTPS && !tlsEnabled {
		return core.NewWithBase(
			core.ErrFieldRequired,
			"--registry-mode bundled-https requires --with-tls so setup can provision registry-tls for the registry pod",
		)
	}
	return nil
}

func setupRegistryStep(logger *zap.Logger, extRegistry *config.ExternalRegistryConfig, usingExternalRegistry bool, registryType, registryStorageSize, registryManifest, registryMode string, tlsEnabled bool, deps SetupDeps) error {
	// Step 4: Deploy internal container registry
	core.Step("Step 4: Configure registry")
	if usingExternalRegistry {
		core.Info(fmt.Sprintf("Using external registry: %s", extRegistry.URL))
		if extRegistry.Username != "" || extRegistry.Password != "" {
			core.Info("Logging into external registry")
			if err := deps.LoginRegistry(logger, extRegistry.URL, extRegistry.Username, extRegistry.Password); err != nil {
				wrappedErr := core.WrapWithBase(core.ErrRegistryLoginFailed, err, fmt.Sprintf("failed to login to registry %q: %v", extRegistry.URL, err))
				core.Error("Registry login failed")
				core.LogStructuredError(logger, wrappedErr, "Registry login failed")
				return wrappedErr
			}
		}
		return nil
	}

	core.Info(fmt.Sprintf("Type: %s", registryType))
	if registryMode == setupplan.RegistryModeBundledHTTPS {
		core.Info("TLS: enabled for registry pod and ingress (bundled HTTPS mode)")
	} else if tlsEnabled {
		core.Info("TLS: enabled for registry ingress; registry pod remains HTTP for internal pulls")
	} else {
		core.Info("TLS: disabled (dev HTTP mode)")
	}
	if err := deps.DeployRegistry(logger, "registry", deps.GetRegistryPort(), registryType, registryStorageSize, registryManifest); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to deploy registry (type: %s, manifest: %s): %v", registryType, registryManifest, err),
			map[string]any{
				"namespace":     "registry",
				"registry_type": registryType,
				"manifest_path": registryManifest,
				"storage_size":  registryStorageSize,
				"registry_port": deps.GetRegistryPort(),
			},
		)
		core.Error("Registry deployment failed")
		core.LogStructuredError(logger, wrappedErr, "Registry deployment failed")
		return wrappedErr
	}

	core.Info("Waiting for registry to be ready...")
	if err := deps.WaitForDeploymentAvailable(logger, "registry", "registry", "app=registry", deps.GetDeploymentTimeout()); err != nil {
		deps.PrintDeploymentDiagnostics("registry", "registry", "app=registry")
		regCtx := map[string]any{
			"deployment": "registry",
			"namespace":  "registry",
			"selector":   "app=registry",
			"component":  "registry",
		}
		mergeDeploymentDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), regCtx, "registry", "registry", "app=registry")
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrRegistryNotReady,
			err,
			fmt.Sprintf("registry deployment not ready in namespace %q: %v", "registry", err),
			regCtx,
		)
		core.Error("Registry failed to become ready")
		core.LogStructuredError(logger, wrappedErr, "Registry failed to become ready")
		return wrappedErr
	}

	if err := deps.RegistryManager.ShowRegistryInfo(); err != nil {
		core.Warn(fmt.Sprintf("Failed to show registry info: %v", err))
	}
	return nil
}

func configureProvisionedRegistryEnv(ext *config.ExternalRegistryConfig, secretName string) error {
	return configureProvisionedRegistryEnvWithClientGo(ext, secretName)
}

func configureProvisionedRegistryEnvWithClientGo(ext *config.ExternalRegistryConfig, secretName string) error {
	if ext == nil || ext.URL == "" {
		return nil
	}
	hasCreds := ext.Username != "" || ext.Password != ""
	if hasCreds && secretName == "" {
		secretName = defaultRegistrySecretName
	}
	literals := map[string]string{"PROVISIONED_REGISTRY_URL": ext.URL}
	secretKeys := []string(nil)
	if hasCreds {
		if err := ensureProvisionedRegistrySecretWithClientGo(secretName, ext.Username, ext.Password); err != nil {
			return err
		}
		platformMode := os.Getenv("MCP_PLATFORM_MODE")
		catalogNamespace := setupplan.CatalogNamespaceForPlatformMode(platformMode)
		if catalogNamespace != "" {
			if err := ensureNamespaceWithLabels(catalogNamespace, catalogNamespaceLabels(platformMode)); err != nil {
				return err
			}
			if err := ensureImagePullSecretWithClientGo(catalogNamespace, secretName, ext.URL, ext.Username, ext.Password); err != nil {
				return err
			}
		}
		literals["PROVISIONED_REGISTRY_SECRET_NAME"] = secretName
		secretKeys = []string{"PROVISIONED_REGISTRY_USERNAME", "PROVISIONED_REGISTRY_PASSWORD"}
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.SetDeploymentEnv(
		context.Background(),
		clients,
		core.NamespaceMCPRuntime,
		core.OperatorDeploymentName,
		literals,
		secretName,
		secretKeys,
	)
}

func configureProvisionedRegistryEnvWithKubectl(kubectl core.KubectlRunner, ext *config.ExternalRegistryConfig, secretName string) error {
	if ext == nil || ext.URL == "" {
		return nil
	}
	hasCreds := ext.Username != "" || ext.Password != ""
	if hasCreds && secretName == "" {
		secretName = defaultRegistrySecretName
	}
	args := []string{
		"set", "env", "deployment/mcp-runtime-operator-controller-manager",
		"-n", "mcp-runtime",
		"PROVISIONED_REGISTRY_URL=" + ext.URL,
	}
	if hasCreds {
		if err := ensureProvisionedRegistrySecretWithKubectl(kubectl, secretName, ext.Username, ext.Password); err != nil {
			return err
		}
		platformMode := os.Getenv("MCP_PLATFORM_MODE")
		catalogNamespace := setupplan.CatalogNamespaceForPlatformMode(platformMode)
		if catalogNamespace != "" {
			if err := kube.EnsureNamespaceWithLabels(kubectl.CommandArgs, catalogNamespace, catalogNamespaceLabels(platformMode)); err != nil {
				return err
			}
			// Create imagePullSecret in the active catalog namespace for pod image pulls.
			if err := ensureImagePullSecretWithKubectl(kubectl, catalogNamespace, secretName, ext.URL, ext.Username, ext.Password); err != nil {
				return err
			}
		}
		args = append(args, "PROVISIONED_REGISTRY_SECRET_NAME="+secretName)
		// Populate env vars from the secret instead of literals to avoid leaking creds in args/history.
		args = append(args, "--from=secret/"+secretName)
	}
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	return kubectl.RunWithOutput(args, os.Stdout, os.Stderr)
}

func ensureProvisionedRegistrySecretWithKubectl(kubectl core.KubectlRunner, name, username, password string) error {
	var envData strings.Builder
	if username != "" {
		envData.WriteString("PROVISIONED_REGISTRY_USERNAME=")
		envData.WriteString(username)
		envData.WriteString("\n")
	}
	if password != "" {
		envData.WriteString("PROVISIONED_REGISTRY_PASSWORD=")
		envData.WriteString(password)
		envData.WriteString("\n")
	}
	if envData.Len() == 0 {
		return nil
	}

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	createCmd, err := kubectl.CommandArgs([]string{
		"create", "secret", "generic", name,
		"--from-env-file=-",
		"-n", core.NamespaceMCPRuntime,
		"--dry-run=client",
		"-o", "yaml",
	})
	if err != nil {
		return err
	}
	createCmd.SetStdin(strings.NewReader(envData.String()))
	var rendered bytes.Buffer
	createCmd.SetStdout(&rendered)
	createCmd.SetStderr(os.Stderr)
	if err := createCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrRenderSecretManifestFailed,
			err,
			fmt.Sprintf("render secret manifest: %v", err),
			map[string]any{"secret_name": name, "namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to render secret manifest")
		// Note: logger not available in this helper, but error will be logged by caller
		return wrappedErr
	}

	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	applyCmd, err := kubectl.CommandArgs([]string{"apply", "-f", "-"})
	if err != nil {
		return err
	}
	applyCmd.SetStdin(&rendered)
	applyCmd.SetStdout(os.Stdout)
	applyCmd.SetStderr(os.Stderr)
	if err := applyCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrApplySecretManifestFailed,
			err,
			fmt.Sprintf("apply secret manifest: %v", err),
			map[string]any{"secret_name": name, "namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to apply secret manifest")
		// Note: logger not available in this helper, but error will be logged by caller
		return wrappedErr
	}

	return nil
}

func ensureProvisionedRegistrySecretWithClientGo(name, username, password string) error {
	data := map[string]string{}
	if username != "" {
		data["PROVISIONED_REGISTRY_USERNAME"] = username
	}
	if password != "" {
		data["PROVISIONED_REGISTRY_PASSWORD"] = password
	}
	if len(data) == 0 {
		return nil
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.UpsertOpaqueSecretStringData(context.Background(), clients, core.NamespaceMCPRuntime, name, data); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrApplySecretManifestFailed,
			err,
			fmt.Sprintf("apply secret manifest: %v", err),
			map[string]any{"secret_name": name, "namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to apply secret manifest")
		return wrappedErr
	}
	return nil
}

func ensureImagePullSecretWithKubectl(kubectl core.KubectlRunner, namespace, name, registry, username, password string) error {
	if username == "" && password == "" {
		return nil
	}

	dockerCfg := map[string]any{
		"auths": map[string]any{
			registry: map[string]string{
				"username": username,
				"password": password,
				"auth":     base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%s:%s", username, password))),
			},
		},
	}
	dockerCfgJSON, err := json.Marshal(dockerCfg)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrMarshalDockerConfigFailed,
			err,
			fmt.Sprintf("marshal docker config: %v", err),
			map[string]any{"registry": registry, "namespace": namespace, "component": "setup"},
		)
		core.Error("Failed to marshal docker config")
		// Note: logger not available in this helper, but error will be logged by caller
		return wrappedErr
	}

	// Build secret manifest
	secretManifest := fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
type: kubernetes.io/dockerconfigjson
data:
  .dockerconfigjson: %s
`, name, namespace, base64.StdEncoding.EncodeToString(dockerCfgJSON))

	// Apply secret manifest
	applyCmd, err := kubectl.CommandArgs([]string{"apply", "-f", "-"})
	if err != nil {
		return err
	}
	applyCmd.SetStdin(strings.NewReader(secretManifest))
	applyCmd.SetStdout(os.Stdout)
	applyCmd.SetStderr(os.Stderr)
	if err := applyCmd.Run(); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrApplyImagePullSecretFailed,
			err,
			fmt.Sprintf("apply imagePullSecret: %v", err),
			map[string]any{"secret_name": name, "namespace": namespace, "registry": registry, "component": "setup"},
		)
		core.Error("Failed to apply image pull secret")
		// Note: logger not available in this helper, but error will be logged by caller
		return wrappedErr
	}

	return nil
}

func ensureImagePullSecretWithClientGo(namespace, name, registry, username, password string) error {
	if username == "" && password == "" {
		return nil
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.UpsertDockerConfigSecret(context.Background(), clients, namespace, name, registry, username, password); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrApplyImagePullSecretFailed,
			err,
			fmt.Sprintf("apply imagePullSecret: %v", err),
			map[string]any{"secret_name": name, "namespace": namespace, "registry": registry, "component": "setup"},
		)
		core.Error("Failed to apply image pull secret")
		return wrappedErr
	}
	return nil
}

func platformRegistryHostForConfig(images AnalyticsImageSet) string {
	if explicit := setupAnalyticsConfigEnvValue("PLATFORM_REGISTRY_URL"); explicit != "" {
		return explicit
	}
	if host := strings.TrimSpace(core.GetRegistryIngressHost()); host != "" &&
		(host != core.DefaultRegistryIngressHost || registryIngressHostExplicitlyConfigured()) {
		return host
	}
	return registryHostFromImage(images.PlatformAPI)
}

func registryIngressHostExplicitlyConfigured() bool {
	for _, key := range []string{"MCP_REGISTRY_INGRESS_HOST", "MCP_PLATFORM_DOMAIN"} {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return true
		}
	}
	return false
}

func registryHostFromImage(image string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return ""
	}
	repo, _ := ref.SplitImage(image)
	first, _, found := strings.Cut(repo, "/")
	if !found {
		return ""
	}
	if strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost" {
		return first
	}
	return ""
}

func registryEndpointEnvExplicitlyConfigured() bool {
	value, ok := os.LookupEnv("MCP_REGISTRY_ENDPOINT")
	return ok && strings.TrimSpace(value) != ""
}
