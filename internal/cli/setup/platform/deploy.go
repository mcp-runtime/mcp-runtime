package platform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/internal/cli/certmanager"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/registry/config"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/manifest"
)

func deployAnalyticsStepCmd(logger *zap.Logger, images AnalyticsImageSet, storageMode, platformMode string, deps SetupDeps) error {
	core.Info("Deploying mcp-sentinel manifests")
	if err := deps.DeployAnalyticsManifests(logger, images, storageMode, platformMode); err != nil {
		core.Error("Analytics deployment failed")
		core.LogStructuredError(logger, err, "Analytics deployment failed")
		return err
	}
	return nil
}

func deployOperatorStep(logger *zap.Logger, operatorImage, gatewayProxyImage string, extRegistry *config.ExternalRegistryConfig, registrySecretName string, usingExternalRegistry bool, operatorArgs []string, deps SetupDeps) error {
	core.Info("Deploying operator manifests")
	imagePullSecretName, err := ensureOperatorImagePullSecret(extRegistry, registrySecretName, deps)
	if err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyImagePullSecretFailed,
			err,
			fmt.Sprintf("failed to prepare operator imagePullSecret: %v", err),
			map[string]any{
				"namespace": core.NamespaceMCPRuntime,
				"component": "operator",
			},
		)
		core.Error("Failed to prepare operator imagePullSecret")
		core.LogStructuredError(logger, wrappedErr, "Failed to prepare operator imagePullSecret")
		return wrappedErr
	}
	if err := deps.DeployOperatorManifests(logger, operatorImage, gatewayProxyImage, operatorArgs, imagePullSecretName); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrOperatorDeploymentFailed,
			err,
			fmt.Sprintf("operator deployment failed for image %q: %v", operatorImage, err),
			map[string]any{
				"image":     operatorImage,
				"namespace": core.NamespaceMCPRuntime,
				"component": "operator",
			},
		)
		core.Error("Operator deployment failed")
		core.LogStructuredError(logger, wrappedErr, "Operator deployment failed")
		return wrappedErr
	}

	if usingExternalRegistry {
		if err := deps.ConfigureProvisionedRegistryEnv(extRegistry, registrySecretName); err != nil {
			wrappedErr := core.WrapWithSentinelAndContext(
				core.ErrConfigureExternalRegistryEnvFailed,
				err,
				fmt.Sprintf("failed to configure external registry env on operator (registry: %q, secret: %q): %v", extRegistry.URL, registrySecretName, err),
				map[string]any{
					"registry_url": extRegistry.URL,
					"secret_name":  registrySecretName,
					"namespace":    core.NamespaceMCPRuntime,
					"component":    "operator",
				},
			)
			core.Error("Failed to configure external registry environment")
			core.LogStructuredError(logger, wrappedErr, "Failed to configure external registry environment")
			return wrappedErr
		}
	}

	if err := deps.RestartDeployment("mcp-runtime-operator-controller-manager", "mcp-runtime"); err != nil {
		if usingExternalRegistry {
			wrappedErr := core.WrapWithSentinel(core.ErrRestartOperatorDeploymentFailed, err, fmt.Sprintf("failed to restart operator deployment after registry env update: %v", err))
			core.Error("Failed to restart operator deployment")
			core.LogStructuredError(logger, wrappedErr, "Failed to restart operator deployment")
			return wrappedErr
		}
		core.Warn(fmt.Sprintf("Could not restart operator deployment: %v", err))
	}
	return nil
}

func ensureOperatorImagePullSecret(extRegistry *config.ExternalRegistryConfig, registrySecretName string, deps SetupDeps) (string, error) {
	if explicit := platformImagePullSecretOverride(); explicit != "" {
		return explicit, nil
	}
	if extRegistry == nil || strings.TrimSpace(extRegistry.URL) == "" || (extRegistry.Username == "" && extRegistry.Password == "") {
		return "", nil
	}
	secretName := defaultPlatformRegistryPullSecretName
	// Keep the operator Deployment pull secret separate from the provisioned
	// registry env Secret in mcp-runtime. The same Kubernetes Secret cannot be
	// both a generic env source and a dockerconfigjson pull secret.
	if custom := strings.TrimSpace(registrySecretName); custom != "" && custom != defaultRegistrySecretName {
		secretName = custom + "-pull"
	}
	if err := deps.EnsureImagePullSecret(core.NamespaceMCPRuntime, secretName, extRegistry.URL, extRegistry.Username, extRegistry.Password); err != nil {
		return "", err
	}
	return secretName, nil
}

func verifySetup(logger *zap.Logger, usingExternalRegistry bool, deps SetupDeps) error {
	core.Step("Step 6: Verify platform components")

	if usingExternalRegistry {
		core.Info("Skipping internal registry availability check (using external registry)")
	} else {
		core.Info("Waiting for registry deployment to be available")
		if err := deps.WaitForDeploymentAvailable(logger, "registry", "registry", "app=registry", deps.GetDeploymentTimeout()); err != nil {
			deps.PrintDeploymentDiagnostics("registry", "registry", "app=registry")
			regCtx := map[string]any{
				"deployment": "registry",
				"namespace":  "registry",
				"selector":   "app=registry",
				"component":  "registry",
			}
			mergeDeploymentDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), regCtx, "registry", "registry", "app=registry")
			wrappedErr := core.WrapWithSentinelAndContext(
				core.ErrRegistryNotReady,
				err,
				fmt.Sprintf("registry not ready: %v", err),
				regCtx,
			)
			core.Error("Registry not ready")
			core.LogStructuredError(logger, wrappedErr, "Registry not ready")
			return wrappedErr
		}
	}

	core.Info("Waiting for operator deployment to be available")
	if err := deps.WaitForDeploymentAvailable(logger, "mcp-runtime-operator-controller-manager", "mcp-runtime", "control-plane=controller-manager", deps.GetDeploymentTimeout()); err != nil {
		deps.PrintDeploymentDiagnostics("mcp-runtime-operator-controller-manager", "mcp-runtime", "control-plane=controller-manager")
		opCtx := map[string]any{
			"deployment": "mcp-runtime-operator-controller-manager",
			"namespace":  "mcp-runtime",
			"selector":   "control-plane=controller-manager",
			"component":  "operator",
		}
		mergeDeploymentDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), opCtx, "mcp-runtime-operator-controller-manager", "mcp-runtime", "control-plane=controller-manager")
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrOperatorNotReady,
			err,
			fmt.Sprintf("operator not ready: %v", err),
			opCtx,
		)
		core.Error("Operator not ready")
		core.LogStructuredError(logger, wrappedErr, "Operator not ready")
		return wrappedErr
	}

	core.Info("Checking MCPServer CRD presence")
	if err := deps.CheckCRDInstalled("mcpservers.mcpruntime.org"); err != nil {
		crdName := "mcpservers.mcpruntime.org"
		crdCtx := map[string]any{"crd": crdName, "component": "crd-check"}
		mergeCRDCheckDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), crdCtx, crdName)
		wrappedErr := core.WrapWithSentinelAndContext(core.ErrCRDCheckFailed, err, fmt.Sprintf("CRD check failed: %v", err), crdCtx)
		core.Error("CRD check failed")
		core.LogStructuredError(logger, wrappedErr, "CRD check failed")
		return wrappedErr
	}

	core.Success("Verification complete")
	return nil
}

func restartDeployment(name, namespace string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.RestartDeployment(context.Background(), clients, namespace, name, time.Now())
}

//lint:ignore U1000 retained as the legacy kubectl implementation for focused tests and fallback patches.
func restartDeploymentWithKubectl(kubectl core.KubectlRunner, name, namespace string) error {
	// #nosec G204 -- command arguments are built from trusted inputs and fixed verbs.
	return kubectl.RunWithOutput([]string{"rollout", "restart", "deployment/" + name, "-n", namespace}, os.Stdout, os.Stderr)
}

func checkCRDInstalled(name string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.CheckCRDExists(context.Background(), clients, name)
}

func checkCRDInstalledWithKubectl(kubectl core.KubectlRunner, name string) error {
	// #nosec G204 -- name is hardcoded CRD identifier from internal code.
	return kubectl.RunWithOutput([]string{"get", "crd", name}, os.Stdout, os.Stderr)
}

// waitForDeploymentAvailable polls a deployment until it has at least one available replica or times out.
func waitForDeploymentAvailable(logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, namespace, name, timeout); err != nil {
		msg := fmt.Sprintf("timed out waiting for deployment %s in namespace %s", name, namespace)
		cause := core.NewWithSentinel(core.ErrSetupDeploymentReadinessDeadlineExceeded, "deployment readiness deadline exceeded")
		ctx := map[string]any{
			"deployment": name,
			"namespace":  namespace,
			"selector":   selector,
			"component":  "deployment-wait",
		}
		mergeDeploymentDebugDiagnosticsIfNeeded(core.DefaultKubectlClient(), ctx, name, namespace, selector)
		wrappedErr := core.WrapWithSentinelAndContext(core.ErrDeploymentTimeout, cause, msg, ctx)
		core.Error("Deployment timeout")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Deployment timeout")
		}
		return wrappedErr
	}
	return nil
}

// waitForDeploymentAvailableWithKubectl polls a deployment until it has at least one available replica or times out.
func waitForDeploymentAvailableWithKubectl(kubectl core.KubectlRunner, logger *zap.Logger, name, namespace, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	lastLog := time.Time{}
	for {
		// #nosec G204 -- name/namespace from internal setup logic, not direct user input.
		cmd, err := kubectl.CommandArgs([]string{"get", "deployment", name, "-n", namespace, "-o", "jsonpath={.status.availableReplicas}"})
		if err == nil {
			out, execErr := cmd.Output()
			if execErr == nil {
				val := strings.TrimSpace(string(out))
				if val == "" {
					val = "0"
				}
				if n, convErr := strconv.Atoi(val); convErr == nil && n > 0 {
					return nil
				}
			}
		}
		if time.Since(lastLog) > 10*time.Second {
			core.Info(fmt.Sprintf("Still waiting for deployment/%s in %s (selector %s, timeout %s)", name, namespace, selector, timeout.Round(time.Second)))
			lastLog = time.Now()
		}
		if time.Now().After(deadline) {
			msg := fmt.Sprintf("timed out waiting for deployment %s in namespace %s", name, namespace)
			cause := core.NewWithSentinel(core.ErrSetupDeploymentReadinessDeadlineExceeded, "deployment readiness deadline exceeded")
			ctx := map[string]any{
				"deployment": name,
				"namespace":  namespace,
				"selector":   selector,
				"component":  "deployment-wait",
			}
			mergeDeploymentDebugDiagnosticsIfNeeded(kubectl, ctx, name, namespace, selector)
			wrappedErr := core.WrapWithSentinelAndContext(core.ErrDeploymentTimeout, cause, msg, ctx)
			core.Error("Deployment timeout")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Deployment timeout")
			}
			return wrappedErr
		}
		time.Sleep(5 * time.Second)
	}
}

// printDeploymentDiagnostics prints a quick status of pods for a deployment selector to help users triage readiness issues.
func printDeploymentDiagnostics(deploy, namespace, selector string) {
	printDeploymentDiagnosticsWithKubectl(core.DefaultKubectlClient(), deploy, namespace, selector)
}

// printDeploymentDiagnosticsWithKubectl prints a quick status of pods for a deployment selector.
func printDeploymentDiagnosticsWithKubectl(kubectl core.KubectlRunner, deploy, namespace, selector string) {
	core.Warn(fmt.Sprintf("Deployment %s in %s is not ready. Showing pod statuses:", deploy, namespace))
	// #nosec G204 -- namespace/selector from internal diagnostics, not user input.
	_ = kubectl.RunWithOutput([]string{"get", "pods", "-n", namespace, "-l", selector, "-o", "wide"}, os.Stdout, os.Stderr)
}

// mergeDeploymentDebugDiagnosticsIfNeeded fetches describe/events/pods from the API when --debug is set
// and attaches a bounded blob to the errx context (cluster-backed failures, not local validation).
func mergeDeploymentDebugDiagnosticsIfNeeded(kubectl core.KubectlRunner, m map[string]any, deployName, namespace, selector string) {
	if !core.IsDebugMode() {
		return
	}
	if d := buildDeploymentWaitDebugDetail(kubectl, deployName, namespace, selector); d != "" {
		m["diagnostics"] = trimDiagnosticsString(d)
	}
}

// buildDeploymentWaitDebugDetail returns kubectl text for a stuck or timed-out deployment wait.
func buildDeploymentWaitDebugDetail(kubectl core.KubectlRunner, deployName, namespace, selector string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("---- describe deployment %s\n", deployName))
	// #nosec G204 -- deploy/namespace/selector are internal setup identifiers, not user shell input.
	if out, err := kubectlText(kubectl, []string{
		"describe", "deployment", deployName, "-n", namespace, "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get pods (selector)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "pods", "-n", namespace, "-l", selector, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get events (sorted)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "events", "-n", namespace, "--sort-by", ".lastTimestamp", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

// buildNamespacedResourceDebugDetail returns describe, pods, and events for a namespaced object (e.g. StatefulSet, Job).
func buildNamespacedResourceDebugDetail(kubectl core.KubectlRunner, kind, name, namespace string) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("---- describe %s %s\n", kind, name))
	// #nosec G204 -- kind/name/namespace are internal resource identifiers, not user shell input.
	if out, err := kubectlText(kubectl, []string{
		"describe", kind, name, "-n", namespace, "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get pods (namespace)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "pods", "-n", namespace, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- get events (sorted)\n")
	if out, err := kubectlText(kubectl, []string{
		"get", "events", "-n", namespace, "--sort-by", ".lastTimestamp", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

// buildCRDCheckDebugDetail returns CRD and api-resources text when a CRD presence check fails.
func buildCRDCheckDebugDetail(kubectl core.KubectlRunner, crdName string) string {
	var b strings.Builder
	b.WriteString("---- get crd\n")
	// #nosec G204 -- crdName is a hardcoded internal API identity.
	if out, err := kubectlText(kubectl, []string{
		"get", "crd", crdName, "-o", "wide", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("get crd: %v\n", err))
	} else {
		b.WriteString(out)
	}
	b.WriteString("---- api-resources (group mcpruntime.org)\n")
	if out, err := kubectlText(kubectl, []string{
		"api-resources", "--api-group=mcpruntime.org", "--request-timeout=30s",
	}); err != nil {
		b.WriteString(fmt.Sprintf("error: %v\n", err))
	} else {
		b.WriteString(out)
	}
	return b.String()
}

func mergeCRDCheckDebugDiagnosticsIfNeeded(kubectl core.KubectlRunner, m map[string]any, crdName string) {
	if !core.IsDebugMode() {
		return
	}
	if d := buildCRDCheckDebugDetail(kubectl, crdName); d != "" {
		m["diagnostics"] = trimDiagnosticsString(d)
	}
}

// deployOperatorManifests deploys operator manifests without requiring kustomize or controller-gen.
// It applies CRD, RBAC, and manager manifests directly, replacing the image name in the process.
func deployOperatorManifests(logger *zap.Logger, operatorImage, gatewayProxyImage string, operatorArgs []string, imagePullSecretName string) error {
	return deployOperatorManifestsWithClientGo(logger, operatorImage, gatewayProxyImage, operatorArgs, imagePullSecretName)
}

func deployOperatorManifestsWithClientGo(logger *zap.Logger, operatorImage, gatewayProxyImage string, operatorArgs []string, imagePullSecretName string) error {
	if err := validateAdapterCertificateIngressIdentity(); err != nil {
		return err
	}
	if err := ensureRepoManagedTraefikMiddlewareResourcesClientGo(logger); err != nil {
		return err
	}

	core.Info("Applying CRD manifests")
	if err := applyManifestDir("config/crd/bases", "", os.Stdout); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplyCRDFailed, err, fmt.Sprintf("failed to apply CRD: %v", err))
		core.Error("Failed to apply CRD")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply CRD")
		}
		return wrappedErr
	}

	core.Info("Applying RBAC manifests")
	if err := ensureNamespaceWithLabels(core.NamespaceMCPRuntime, nil); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrEnsureOperatorNamespaceFailed,
			err,
			fmt.Sprintf("failed to ensure operator namespace: %v", err),
			map[string]any{"namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to ensure operator namespace")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to ensure operator namespace")
		}
		return wrappedErr
	}
	for _, path := range []string{
		"config/rbac/service_account.yaml",
		"config/rbac/role.yaml",
		"config/rbac/role_binding.yaml",
	} {
		if err := applyManifestFile(path, "", os.Stdout); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrApplyRBACFailed, err, fmt.Sprintf("failed to apply RBAC: %v", err))
			core.Error("Failed to apply RBAC")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to apply RBAC")
			}
			return wrappedErr
		}
	}
	core.Info("Reapplied operator ClusterRole mcp-runtime-operator-role from config/rbac/role.yaml; run `mcp-runtime cluster doctor` if MCPServer creates ever appear unreconciled")

	core.Info("Preparing operator admission webhook TLS")
	webhookCA, err := ensureOperatorWebhookTLSSecretClientGo()
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplySecretManifestFailed, err, fmt.Sprintf("failed to prepare operator webhook TLS secret: %v", err))
		core.Error("Failed to prepare operator webhook TLS")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to prepare operator webhook TLS")
		}
		return wrappedErr
	}

	core.Info("Applying operator deployment")
	managerYAML, err := renderOperatorManagerManifest(operatorImage, gatewayProxyImage, operatorArgs, existingOperatorEnvValueClientGo, webhookCA)
	if err != nil {
		if logger != nil {
			core.LogStructuredError(logger, err, "Failed to render manager manifest")
		}
		return err
	}
	if strings.TrimSpace(imagePullSecretName) != "" {
		rendered, err := injectImagePullSecretsIntoManifest(string(managerYAML), imagePullSecretName)
		if err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrRenderManagerYAMLFailed, err, fmt.Sprintf("failed to inject operator imagePullSecret: %v", err))
			core.Error("Failed to inject operator imagePullSecret")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to inject operator imagePullSecret")
			}
			return wrappedErr
		}
		managerYAML = []byte(rendered)
	}
	if err := applyManifestYAML(string(managerYAML), core.NamespaceMCPRuntime, os.Stdout); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyManagerDeploymentFailed,
			err,
			fmt.Sprintf("failed to apply manager deployment: %v", err),
			map[string]any{"operator_image": operatorImage, "namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to apply manager deployment")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply manager deployment")
		}
		return wrappedErr
	}

	if err := applyOperatorWebhookManifestsClientGo(webhookCA); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplyManifestFailed, err, fmt.Sprintf("failed to apply operator webhook manifests: %v", err))
		core.Error("Failed to apply operator webhook manifests")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply operator webhook manifests")
		}
		return wrappedErr
	}

	core.Success("Operator manifests deployed successfully")
	return nil
}

func renderOperatorManagerManifest(operatorImage, gatewayProxyImage string, operatorArgs []string, existingEnvValue func(string) string, webhookCA []byte) ([]byte, error) {
	managerYAML, err := os.ReadFile("config/manager/manager.yaml")
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to read manager.yaml: %v", err))
		core.Error("Failed to read manager.yaml")
		return nil, wrappedErr
	}

	mutator, err := manifest.NewMutator(managerYAML)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrParseManagerYAMLFailed, err, fmt.Sprintf("failed to parse manager.yaml: %v", err))
		core.Error("Failed to parse manager.yaml")
		return nil, wrappedErr
	}

	if err := mutator.SetDeploymentImage(core.OperatorDeploymentName, core.OperatorManagerContainerName, operatorImage); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrSetOperatorImageFailed, err, fmt.Sprintf("failed to set operator image: %v", err))
		core.Error("Failed to set operator image")
		return nil, wrappedErr
	}

	pullPolicy := operatorImagePullPolicy(operatorImage)
	if pullPolicy != "" {
		if err := mutator.SetDeploymentImagePullPolicy(core.OperatorDeploymentName, core.OperatorManagerContainerName, pullPolicy); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to set operator image pull policy: %v", err))
			core.Error("Failed to set operator image pull policy")
			return nil, wrappedErr
		}
	}

	if len(operatorArgs) > 0 {
		if err := mutator.MergeDeploymentArgs(core.OperatorDeploymentName, core.OperatorManagerContainerName, operatorArgs); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to merge operator args: %v", err))
			core.Error("Failed to merge operator args")
			return nil, wrappedErr
		}
	}

	existingGatewayOTLPEndpoint := ""
	if existingEnvValue != nil {
		existingGatewayOTLPEndpoint = existingEnvValue(gatewayOTELExporterOTLPEndpointEnv)
	}
	if envVars := operatorEnvOverrides(gatewayProxyImage, existingGatewayOTLPEndpoint); len(envVars) > 0 {
		envMap := make(map[string]string, len(envVars))
		for _, ev := range envVars {
			envMap[ev.Name] = ev.Value
		}
		if err := mutator.MergeDeploymentEnv(core.OperatorDeploymentName, core.OperatorManagerContainerName, envMap); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to merge operator env vars: %v", err))
			core.Error("Failed to merge operator env vars")
			return nil, wrappedErr
		}
	}

	if err := configureOperatorWebhookDeployment(mutator, webhookCA); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to configure operator webhooks: %v", err))
		core.Error("Failed to configure operator webhooks")
		return nil, wrappedErr
	}

	mutatedYAML, err := mutator.ToYAML()
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrRenderManagerYAMLFailed, err, fmt.Sprintf("failed to render mutated manifest: %v", err))
		core.Error("Failed to render mutated manifest")
		return nil, wrappedErr
	}
	return mutatedYAML, nil
}

// deployOperatorManifestsWithKubectl deploys operator manifests without requiring kustomize or controller-gen.
// It applies CRD, RBAC, and manager manifests directly, replacing the image name and injecting operator args/env.
func deployOperatorManifestsWithKubectl(kubectl core.KubectlRunner, logger *zap.Logger, operatorImage, gatewayProxyImage string, operatorArgs []string, imagePullSecretName string) error {
	if err := validateAdapterCertificateIngressIdentity(); err != nil {
		return err
	}
	if err := ensureRepoManagedTraefikMiddlewareResources(kubectl, logger); err != nil {
		return err
	}

	// Step 1: Apply CRD
	core.Info("Applying CRD manifests")
	// #nosec G204 -- fixed directory path from repository.
	if err := kubectl.RunWithOutput([]string{"apply", "--validate=false", "-f", "config/crd/bases"}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplyCRDFailed, err, fmt.Sprintf("failed to apply CRD: %v", err))
		core.Error("Failed to apply CRD")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply CRD")
		}
		return wrappedErr
	}

	// Step 2: Apply RBAC (ServiceAccount, Role, RoleBinding)
	core.Info("Applying RBAC manifests")
	if err := kube.EnsureNamespace(core.DefaultKubectlClient().CommandArgs, core.NamespaceMCPRuntime); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrEnsureOperatorNamespaceFailed,
			err,
			fmt.Sprintf("failed to ensure operator namespace: %v", err),
			map[string]any{"namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to ensure operator namespace")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to ensure operator namespace")
		}
		return wrappedErr
	}

	// #nosec G204 -- fixed kustomize path from repository.
	if err := kubectl.RunWithOutput([]string{"apply", "-k", "config/rbac/"}, os.Stdout, os.Stderr); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplyRBACFailed, err, fmt.Sprintf("failed to apply RBAC: %v", err))
		core.Error("Failed to apply RBAC")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply RBAC")
		}
		return wrappedErr
	}
	core.Info("Reapplied operator ClusterRole mcp-runtime-operator-role from config/rbac/role.yaml; run `mcp-runtime cluster doctor` if MCPServer creates ever appear unreconciled")

	core.Info("Preparing operator admission webhook TLS")
	webhookCA, err := ensureOperatorWebhookTLSSecret(kubectl)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplySecretManifestFailed, err, fmt.Sprintf("failed to prepare operator webhook TLS secret: %v", err))
		core.Error("Failed to prepare operator webhook TLS")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to prepare operator webhook TLS")
		}
		return wrappedErr
	}

	// Step 3: Apply manager deployment with structured image replacement
	core.Info("Applying operator deployment")

	// Read manager.yaml and apply structured mutations
	managerYAML, err := os.ReadFile("config/manager/manager.yaml")
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrReadManagerYAMLFailed, err, fmt.Sprintf("failed to read manager.yaml: %v", err))
		core.Error("Failed to read manager.yaml")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to read manager.yaml")
		}
		return wrappedErr
	}

	// Use structured manifest mutation instead of regex
	mutator, err := manifest.NewMutator(managerYAML)
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrParseManagerYAMLFailed, err, fmt.Sprintf("failed to parse manager.yaml: %v", err))
		core.Error("Failed to parse manager.yaml")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to parse manager.yaml")
		}
		return wrappedErr
	}

	// Set the operator image
	if err := mutator.SetDeploymentImage(core.OperatorDeploymentName, core.OperatorManagerContainerName, operatorImage); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrSetOperatorImageFailed, err, fmt.Sprintf("failed to set operator image: %v", err))
		core.Error("Failed to set operator image")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to set operator image")
		}
		return wrappedErr
	}

	// Set image pull policy based on image
	pullPolicy := operatorImagePullPolicy(operatorImage)
	if pullPolicy != "" {
		if err := mutator.SetDeploymentImagePullPolicy(core.OperatorDeploymentName, core.OperatorManagerContainerName, pullPolicy); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to set operator image pull policy: %v", err))
			core.Error("Failed to set operator image pull policy")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to set operator image pull policy")
			}
			return wrappedErr
		}
	}

	// Inject operator args if provided
	if len(operatorArgs) > 0 {
		if err := mutator.MergeDeploymentArgs(core.OperatorDeploymentName, core.OperatorManagerContainerName, operatorArgs); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to merge operator args: %v", err))
			core.Error("Failed to merge operator args")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to merge operator args")
			}
			return wrappedErr
		}
	}

	// Inject environment variables if provided.
	existingGatewayOTLPEndpoint := existingOperatorEnvValue(kubectl, gatewayOTELExporterOTLPEndpointEnv)
	if envVars := operatorEnvOverrides(gatewayProxyImage, existingGatewayOTLPEndpoint); len(envVars) > 0 {
		envMap := make(map[string]string, len(envVars))
		for _, ev := range envVars {
			envMap[ev.Name] = ev.Value
		}
		if err := mutator.MergeDeploymentEnv(core.OperatorDeploymentName, core.OperatorManagerContainerName, envMap); err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to merge operator env vars: %v", err))
			core.Error("Failed to merge operator env vars")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to merge operator env vars")
			}
			return wrappedErr
		}
	}

	if err := configureOperatorWebhookDeployment(mutator, webhookCA); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrMutateManagerYAMLFailed, err, fmt.Sprintf("failed to configure operator webhooks: %v", err))
		core.Error("Failed to configure operator webhooks")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to configure operator webhooks")
		}
		return wrappedErr
	}

	// Render the mutated manifest
	mutatedYAML, err := mutator.ToYAML()
	if err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrRenderManagerYAMLFailed, err, fmt.Sprintf("failed to render mutated manifest: %v", err))
		core.Error("Failed to render mutated manifest")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to render mutated manifest")
		}
		return wrappedErr
	}
	if strings.TrimSpace(imagePullSecretName) != "" {
		rendered, err := injectImagePullSecretsIntoManifest(string(mutatedYAML), imagePullSecretName)
		if err != nil {
			wrappedErr := core.WrapWithSentinel(core.ErrRenderManagerYAMLFailed, err, fmt.Sprintf("failed to inject operator imagePullSecret: %v", err))
			core.Error("Failed to inject operator imagePullSecret")
			if logger != nil {
				core.LogStructuredError(logger, wrappedErr, "Failed to inject operator imagePullSecret")
			}
			return wrappedErr
		}
		mutatedYAML = []byte(rendered)
	}

	if err := applyOperatorManagerManifest(kubectl, mutatedYAML); err != nil {
		wrappedErr := core.WrapWithSentinelAndContext(
			core.ErrApplyManagerDeploymentFailed,
			err,
			fmt.Sprintf("failed to apply manager deployment: %v", err),
			map[string]any{"operator_image": operatorImage, "namespace": core.NamespaceMCPRuntime, "component": "setup"},
		)
		core.Error("Failed to apply manager deployment")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply manager deployment")
		}
		return wrappedErr
	}

	if err := applyOperatorWebhookManifests(kubectl, webhookCA); err != nil {
		wrappedErr := core.WrapWithSentinel(core.ErrApplyManifestFailed, err, fmt.Sprintf("failed to apply operator webhook manifests: %v", err))
		core.Error("Failed to apply operator webhook manifests")
		if logger != nil {
			core.LogStructuredError(logger, wrappedErr, "Failed to apply operator webhook manifests")
		}
		return wrappedErr
	}

	core.Success("Operator manifests deployed successfully")
	return nil
}

func applyOperatorManagerManifest(kubectl core.KubectlRunner, managerYAML []byte) error {
	cmd, err := kubectl.CommandArgs([]string{
		"apply",
		"--server-side",
		"--force-conflicts",
		"--field-manager=mcp-runtime-setup",
		"-f",
		"-",
	})
	if err != nil {
		return err
	}
	cmd.SetStdin(bytes.NewReader(managerYAML))
	cmd.SetStdout(os.Stdout)
	cmd.SetStderr(os.Stderr)
	return cmd.Run()
}

// mcpSentinelDependencyRolloutFailed wraps early mcp-sentinel storage/messaging rollouts; diagnostics are attached only in --debug.
func mcpSentinelDependencyRolloutFailed(kubectl core.KubectlRunner, err error, kind, name, namespace, phase string) error {
	ctx := map[string]any{
		"component": "mcp-sentinel",
		"phase":     phase,
		"resource":  fmt.Sprintf("%s/%s", kind, name),
		"namespace": namespace,
	}
	if core.IsDebugMode() {
		if diag := buildNamespacedResourceDebugDetail(kubectl, kind, name, namespace); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithSentinelAndContext(core.ErrOperatorDeploymentFailed, err,
		fmt.Sprintf("mcp-sentinel %s: %s/%s: %v", phase, kind, name, err), ctx)
}

// mcpSentinelDependencyJobFailed wraps the clickhouse init job; diagnostics are attached only in --debug.
func mcpSentinelDependencyJobFailed(kubectl core.KubectlRunner, err error, name, namespace, phase string) error {
	ctx := map[string]any{
		"component": "mcp-sentinel",
		"phase":     phase,
		"resource":  "job/" + name,
		"namespace": namespace,
	}
	if core.IsDebugMode() {
		if diag := buildNamespacedResourceDebugDetail(kubectl, "job", name, namespace); diag != "" {
			ctx["diagnostics"] = trimDiagnosticsString(diag)
		}
	}
	return core.WrapWithSentinelAndContext(core.ErrOperatorDeploymentFailed, err,
		fmt.Sprintf("mcp-sentinel %s: job/%s: %v", phase, name, err), ctx)
}

// runRolloutWithOptionalDebugCapture runs kubectl rollout status, teeing output to a buffer
// in --debug mode so it can be attached to the structured error.
func runRolloutWithOptionalDebugCapture(kubectl core.KubectlRunner, kind, name, namespace, timeout string) (capture string, err error) {
	args := []string{
		"rollout", "status",
		fmt.Sprintf("%s/%s", kind, name),
		"-n", namespace, "--timeout=" + timeout,
	}
	if !core.IsDebugMode() {
		return "", kubectl.RunWithOutput(args, os.Stdout, os.Stderr)
	}
	var buf bytes.Buffer
	w := io.MultiWriter(os.Stdout, &buf)
	err = kubectl.RunWithOutput(args, w, w)
	return buf.String(), err
}

func runRolloutWithOptionalDebugCaptureClientGo(kind, name, namespace string, timeout time.Duration) (capture string, err error) {
	return "", waitForRolloutStatusWithClientGo(kind, name, namespace, timeout)
}

func kubectlText(kubectl core.KubectlRunner, args []string) (string, error) {
	cmd, err := kubectl.CommandArgs(args)
	if err != nil {
		return "", err
	}
	b, err := cmd.CombinedOutput()
	return string(b), err
}

func waitForRolloutStatusWithKubectl(kubectl core.KubectlRunner, kind, name, namespace, timeout string) error {
	return kubectl.RunWithOutput([]string{"rollout", "status", fmt.Sprintf("%s/%s", kind, name), "-n", namespace, "--timeout=" + timeout}, os.Stdout, os.Stderr)
}

func waitForRolloutStatusWithClientGo(kind, name, namespace string, timeout time.Duration) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.WaitForWorkloadRollout(context.Background(), clients, namespace, kind, name, timeout)
}

// analyticsRolloutTimeoutString returns the kubectl --timeout value for mcp-sentinel rollouts.
// Uses MCP_DEPLOYMENT_TIMEOUT (see core.GetDeploymentTimeout); if unset or non-positive, uses the default 5m.
func analyticsRolloutTimeoutString() string {
	return analyticsRolloutTimeoutDuration().String()
}

func analyticsRolloutTimeoutDuration() time.Duration {
	d := core.GetDeploymentTimeout()
	if d <= 0 {
		d = 5 * time.Minute
	}
	return d
}

// printAnalyticsRolloutDiagnostics prints pods and events to help triage stuck mcp-sentinel rollouts.
func printAnalyticsRolloutDiagnostics(kubectl core.KubectlRunner) {
	core.Warn("mcp-sentinel rollouts failed. Namespace snapshot (pods):")
	// #nosec G204 -- fixed namespace for diagnostics.
	_ = kubectl.RunWithOutput([]string{"get", "pods", "-n", core.DefaultAnalyticsNamespace, "-o", "wide"}, os.Stdout, os.Stderr)
	core.Warn("Recent events in mcp-sentinel (newest last):")
	_ = kubectl.RunWithOutput([]string{"get", "events", "-n", core.DefaultAnalyticsNamespace, "--sort-by", ".lastTimestamp"}, os.Stdout, os.Stderr)
}

func waitForJobCompletionWithKubectl(kubectl core.KubectlRunner, name, namespace, timeout string) error {
	return kubectl.RunWithOutput([]string{"wait", "--for=condition=complete", "job/" + name, "-n", namespace, "--timeout=" + timeout}, os.Stdout, os.Stderr)
}

func waitForJobCompletionClientGo(name, namespace string, timeout time.Duration) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.WaitForJobComplete(context.Background(), clients, namespace, name, timeout)
}

func deleteJobIfExistsWithKubectl(kubectl core.KubectlRunner, name, namespace string) error {
	return kubectl.RunWithOutput([]string{"delete", "job/" + name, "-n", namespace, "--ignore-not-found=true", "--wait=true", "--timeout=60s"}, os.Stdout, os.Stderr)
}

func deleteJobIfExistsClientGo(name, namespace string) error {
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	return k8sclient.DeleteJob(context.Background(), clients, namespace, name, 60*time.Second)
}

func operatorImagePullPolicy(operatorImage string) string {
	if strings.TrimSpace(operatorImage) == testModeOperatorImage {
		return "IfNotPresent"
	}
	return "Always"
}

// bundledMCPAuthServicePresent reports whether the bundled mcp-auth Service
// exists in the cluster. It is a variable so tests can stub cluster state.
var bundledMCPAuthServicePresent = func() bool {
	clients, err := platformKubernetesClients()
	if err != nil || clients == nil || clients.Clientset == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err = clients.Clientset.CoreV1().Services(core.DefaultAnalyticsNamespace).Get(ctx, "mcp-auth-server", metav1.GetOptions{})
	return err == nil
}

// operatorInternalOAuthIssuerURL returns the in-cluster issuer URL the
// operator hands to gateway sidecars. An explicit OAUTH_INTERNAL_ISSUER_URL
// wins; otherwise it is derived from cluster state, so a setup rerun that
// skips the mcp-auth step still keeps the value while the bundled mcp-auth
// Service exists instead of dropping it from the re-rendered operator.
func operatorInternalOAuthIssuerURL() string {
	if issuer := strings.TrimSpace(os.Getenv("OAUTH_INTERNAL_ISSUER_URL")); issuer != "" {
		return issuer
	}
	if bundledMCPAuthServicePresent() {
		return mcpAuthInternalIssuerURLForCluster()
	}
	return ""
}

// operatorEnvOverrides returns the environment variables to set on the operator deployment.
func operatorEnvOverrides(gatewayProxyImage, existingGatewayOTLPEndpoint string) []operatorEnvVar {
	var envVars []operatorEnvVar
	if issuer := operatorInternalOAuthIssuerURL(); issuer != "" {
		envVars = append(envVars, operatorEnvVar{Name: "OAUTH_INTERNAL_ISSUER_URL", Value: issuer})
	}
	image := strings.TrimSpace(gatewayProxyImage)
	if image == "" {
		image = strings.TrimSpace(core.GetGatewayProxyImageOverride())
	}
	if image != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_GATEWAY_PROXY_IMAGE", Value: image})
	}
	gatewayOTLPEndpoint := strings.TrimSpace(core.GetGatewayOTLPEndpointOverride())
	if gatewayOTLPEndpoint == "" {
		gatewayOTLPEndpoint = strings.TrimSpace(existingGatewayOTLPEndpoint)
	}
	if gatewayOTLPEndpoint == "" {
		gatewayOTLPEndpoint = defaultGatewayOTELExporterOTLPEndpointForCluster()
	}
	envVars = append(envVars, operatorEnvVar{Name: gatewayOTELExporterOTLPEndpointEnv, Value: gatewayOTLPEndpoint})
	ingestURL := strings.TrimSpace(core.GetAnalyticsIngestURLOverride())
	if ingestURL == "" {
		ingestURL = defaultAnalyticsIngestURLForCluster()
	}
	if ingestURL != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_SENTINEL_INGEST_URL", Value: ingestURL})
	}
	if mode := strings.TrimSpace(core.DefaultCLIConfig.IngressReadinessMode); mode != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_INGRESS_READINESS_MODE", Value: mode})
	}
	if issuer := strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER")); issuer != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_MTLS_CLUSTER_ISSUER", Value: issuer})
	}
	// Adapter certificates on OAuth routes are opt-in: enabling them moves
	// every OAuth server to a Traefik IngressRoute behind an mTLS gateway hop.
	if enabled := strings.TrimSpace(os.Getenv("MCP_ADAPTER_CERTIFICATES")); enabled != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_ADAPTER_CERTIFICATES", Value: enabled})
	}
	if tlsNamespace := strings.TrimSpace(os.Getenv("MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE")); tlsNamespace != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE", Value: tlsNamespace})
	}
	if trustDomain := strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN")); trustDomain != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_TRUST_DOMAIN", Value: trustDomain})
	}
	registryEndpoint := strings.TrimSpace(resolveInternalPlatformRegistryURLClientGo(nil))
	if registryEndpoint != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_REGISTRY_ENDPOINT", Value: registryEndpoint})
	}
	registryIngressHost := strings.TrimSpace(core.GetRegistryIngressHost())
	if registryIngressHost != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_REGISTRY_INGRESS_HOST", Value: registryIngressHost})
	}
	if mcpHost := strings.TrimSpace(core.GetMcpIngressHost()); mcpHost != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_DEFAULT_INGRESS_HOST", Value: mcpHost})
		if strings.TrimSpace(core.GetRegistryClusterIssuerName()) != "" || core.GetProvidedTLSSecrets() {
			envVars = append(envVars,
				operatorEnvVar{Name: "MCP_DEFAULT_INGRESS_ENTRYPOINTS", Value: "websecure"},
				operatorEnvVar{Name: "MCP_DEFAULT_INGRESS_TLS", Value: "true"},
			)
		}
	}
	clusterName := strings.TrimSpace(core.GetClusterName())
	if clusterName != "" {
		envVars = append(envVars, operatorEnvVar{Name: "MCP_CLUSTER_NAME", Value: clusterName})
	}
	if adapterCertificatesEnabled() {
		envVars = append(envVars, ingressControllerOperatorEnv(detectIngressControllerIdentity())...)
	}
	return envVars
}

func existingOperatorEnvValue(kubectl core.KubectlRunner, name string) string {
	jsonPath := fmt.Sprintf(
		`jsonpath={.spec.template.spec.containers[?(@.name=="%s")].env[?(@.name=="%s")].value}`,
		core.OperatorManagerContainerName,
		name,
	)
	cmd, err := kubectl.CommandArgs([]string{"get", "deployment/" + core.OperatorDeploymentName, "-n", core.NamespaceMCPRuntime, "-o", jsonPath})
	if err != nil {
		return ""
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func existingOperatorEnvValueClientGo(name string) string {
	clients, err := platformKubernetesClients()
	if err != nil {
		return ""
	}
	deploy, err := k8sclient.GetDeployment(context.Background(), clients, core.NamespaceMCPRuntime, core.OperatorDeploymentName)
	if err != nil {
		return ""
	}
	for _, container := range deploy.Spec.Template.Spec.Containers {
		if container.Name != core.OperatorManagerContainerName {
			continue
		}
		for _, env := range container.Env {
			if env.Name == name {
				return strings.TrimSpace(env.Value)
			}
		}
	}
	return ""
}

func applySetupPlanToCLIConfig(plan setupplan.Plan) {
	if issuer := strings.TrimSpace(plan.MTLSClusterIssuer); issuer != "" {
		_ = os.Setenv("MCP_MTLS_CLUSTER_ISSUER", issuer)
	} else {
		_ = os.Unsetenv("MCP_MTLS_CLUSTER_ISSUER")
	}
	if core.DefaultCLIConfig == nil {
		return
	}
	if plan.RegistryMode != setupplan.RegistryModeExternal && !registryEndpointEnvExplicitlyConfigured() {
		// Only overwrite when the endpoint is still the default placeholder.
		// If MCP_PLATFORM_DOMAIN or a prior config step already resolved a real
		// hostname (e.g. "registry.mcpruntime.org"), preserve it so that
		// resolveInternalPlatformRegistryURLClientGo can fall through to ClusterIP
		// discovery and avoid the bootstrap deadlock.
		current := strings.TrimSpace(core.DefaultCLIConfig.RegistryEndpoint)
		if current == "" || current == core.DefaultRegistryEndpoint {
			core.DefaultCLIConfig.RegistryEndpoint = fmt.Sprintf("%s:%d", registryServiceDNS(), core.GetRegistryPort())
		}
	}
	core.DefaultCLIConfig.ProvidedTLSSecrets = plan.TLSEnabled && plan.ProvidedTLSSecrets
	if !plan.TLSEnabled {
		core.DefaultCLIConfig.RegistryClusterIssuerName = ""
		return
	}
	if plan.ProvidedTLSSecrets {
		core.DefaultCLIConfig.RegistryClusterIssuerName = ""
		return
	}
	if strings.TrimSpace(plan.ACMEmail) != "" {
		core.DefaultCLIConfig.RegistryClusterIssuerName = certmanager.ClusterIssuerNameForACME(plan.ACMEStaging)
		return
	}
	if strings.TrimSpace(plan.TLSClusterIssuer) != "" {
		core.DefaultCLIConfig.RegistryClusterIssuerName = strings.TrimSpace(plan.TLSClusterIssuer)
		return
	}
	core.DefaultCLIConfig.RegistryClusterIssuerName = certmanager.CertClusterIssuerName
}
