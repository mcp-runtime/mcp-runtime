package platform

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"go.uber.org/zap"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/cluster/registrycompat"
	"mcp-runtime/internal/cli/core"
	"mcp-runtime/pkg/k8sclient"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
)

const (
	defaultRegistryDeploymentImage  = "registry:2.8.3"
	registryImageOverrideEnvForPlan = "MCP_RUNTIME_REGISTRY_IMAGE_OVERRIDE"
)

func deployRegistryClientGo(logger *zap.Logger, namespace string, port int, registryType, registryStorageSize, manifestPath string) error {
	if err := validateRegistryType(registryType); err != nil {
		return err
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.EnsureNamespace(context.Background(), clients, namespace, nil); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrEnsureNamespaceFailed,
			err,
			fmt.Sprintf("failed to ensure namespace: %v", err),
			map[string]any{"namespace": namespace, "component": "registry"},
		)
		core.Error("Failed to ensure namespace")
		core.LogStructuredError(logger, wrappedErr, "Failed to ensure namespace")
		return wrappedErr
	}

	if logger != nil {
		logger.Info("Applying registry manifests")
	}
	manifest, err := renderRegistryKustomizeManifest(manifestPath)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to render registry manifest %q: %v", manifestPath, err),
			map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
		)
		core.Error("Failed to render registry manifest")
		core.LogStructuredError(logger, wrappedErr, "Failed to render registry manifest")
		return wrappedErr
	}
	overrideImage := strings.TrimSpace(os.Getenv(registryImageOverrideEnvForPlan))
	if overrideImage != "" && logger != nil {
		logger.Info("Applying registry image override", zap.String("image", overrideImage))
	}
	// Derive the public host from cluster state when the caller's env only
	// yields the registry.local placeholder, so an env-less rerun never
	// downgrades a public registry Ingress.
	registryHost, registryHostSource := k8sclient.ResolveRegistryPublicHost(context.Background(), clients, core.GetRegistryIngressHost())
	if logger != nil {
		logger.Info("Resolved registry ingress host", zap.String("host", registryHost), zap.String("source", registryHostSource))
	}
	manifest, err = mutateRegistryManifest(manifest, registryHost, overrideImage)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			err.Error(),
			map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
		)
		core.Error("Failed to rewrite registry manifest")
		core.LogStructuredError(logger, wrappedErr, "Failed to rewrite registry manifest")
		return wrappedErr
	}
	results, err := k8sclient.ApplyManifestYAML(context.Background(), clients, []byte(manifest), namespace)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to deploy registry: %v", err),
			map[string]any{"namespace": namespace, "manifest_path": manifestPath, "registry_type": registryType, "component": "registry"},
		)
		core.Error("Failed to deploy registry")
		core.LogStructuredError(logger, wrappedErr, "Failed to deploy registry")
		return wrappedErr
	}
	writeApplyResults(os.Stdout, results)

	if err := applyRegistryCompatibilityOverlay(logger, clients, namespace, manifestPath); err != nil {
		return err
	}

	if err := ensureRegistryStorageSizeClientGo(logger, clients, namespace, registryStorageSize); err != nil {
		return err
	}

	if logger != nil {
		logger.Info("Waiting for registry to be ready")
	}
	if err := k8sclient.WaitForDeploymentAvailable(context.Background(), clients, namespace, "registry", 5*time.Minute); err != nil {
		if logger != nil {
			logger.Warn("Registry deployment may still be in progress", zap.Error(err))
		}
	}
	if logger != nil {
		logger.Info("Registry deployed successfully")
	}
	_ = port
	return nil
}

func applyRegistryCompatibilityOverlay(logger *zap.Logger, clients *k8sclient.Clients, namespace, manifestPath string) error {
	overlaySubPath := registrycompat.OverlayPath(core.DefaultKubectlClient())
	if overlaySubPath == "" {
		return nil
	}
	compatPath := registrycompat.ResolveOverlayPath(manifestPath, overlaySubPath)
	if logger != nil {
		logger.Info("Applying registry compatibility overlay", zap.String("path", compatPath))
	}
	manifest, err := renderRegistryKustomizeManifest(compatPath)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to render registry compatibility overlay %q: %v", compatPath, err),
			map[string]any{"namespace": namespace, "manifest_path": compatPath, "component": "registry"},
		)
		core.Error("Failed to render registry compatibility overlay")
		core.LogStructuredError(logger, wrappedErr, "Failed to render registry compatibility overlay")
		return wrappedErr
	}
	results, err := k8sclient.ApplyManifestYAML(context.Background(), clients, []byte(manifest), namespace)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrDeployRegistryFailed,
			err,
			fmt.Sprintf("failed to apply registry compatibility overlay: %v", err),
			map[string]any{"namespace": namespace, "manifest_path": compatPath, "component": "registry"},
		)
		core.Error("Failed to apply registry compatibility overlay")
		core.LogStructuredError(logger, wrappedErr, "Failed to apply registry compatibility overlay")
		return wrappedErr
	}
	writeApplyResults(os.Stdout, results)
	return nil
}

func renderRegistryKustomizeManifest(manifestPath string) (string, error) {
	kubectl := core.DefaultKubectlClient()
	renderCmd, err := kubectl.CommandArgs([]string{"kustomize", manifestPath})
	if err != nil {
		return "", err
	}
	var stdout, stderr bytes.Buffer
	renderCmd.SetStdout(&stdout)
	renderCmd.SetStderr(&stderr)
	if err := renderCmd.Run(); err != nil {
		return "", fmt.Errorf("kubectl kustomize %s failed: %w (%s)", manifestPath, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func validateRegistryType(registryType string) error {
	switch strings.ToLower(strings.TrimSpace(registryType)) {
	case "", "docker":
		return nil
	default:
		return core.NewWithBase(core.ErrUnsupportedRegistryType, fmt.Sprintf("unsupported registry type %q; only docker is supported today", registryType))
	}
}

func mutateRegistryManifest(manifest, host, overrideImage string) (string, error) {
	host = strings.TrimSpace(host)
	overrideImage = strings.TrimSpace(overrideImage)
	replaceHost := host != "" && host != "registry.local"
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var out strings.Builder
	imageReplaced := false
	wroteObject := false
	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("decode registry manifest: %w", err)
		}
		if len(obj.Object) == 0 {
			continue
		}
		if replaceHost {
			obj.Object = replaceStringValues(obj.Object, "registry.local", host).(map[string]any)
		}
		removeRegistryClusterIssuerAnnotation(obj)
		if overrideImage != "" && setRegistryDeploymentImage(obj, overrideImage) {
			imageReplaced = true
		}
		rendered, err := yaml.Marshal(obj.Object)
		if err != nil {
			return "", fmt.Errorf("encode registry manifest: %w", err)
		}
		if wroteObject {
			out.WriteString("---\n")
		}
		out.Write(rendered)
		if !strings.HasSuffix(string(rendered), "\n") {
			out.WriteByte('\n')
		}
		wroteObject = true
	}
	if overrideImage != "" && !imageReplaced {
		return "", fmt.Errorf("registry image reference %q not found in manifest", defaultRegistryDeploymentImage)
	}
	return out.String(), nil
}

func replaceStringValues(value any, oldValue, newValue string) any {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			typed[key] = replaceStringValues(nested, oldValue, newValue)
		}
		return typed
	case []any:
		for i, nested := range typed {
			typed[i] = replaceStringValues(nested, oldValue, newValue)
		}
		return typed
	case string:
		return strings.ReplaceAll(typed, oldValue, newValue)
	default:
		return typed
	}
}

func removeRegistryClusterIssuerAnnotation(obj *unstructured.Unstructured) {
	annotations := obj.GetAnnotations()
	if len(annotations) == 0 {
		return
	}
	delete(annotations, "cert-manager.io/cluster-issuer")
	obj.SetAnnotations(annotations)
}

func setRegistryDeploymentImage(obj *unstructured.Unstructured, image string) bool {
	if obj.GetKind() != "Deployment" || obj.GetName() != "registry" {
		return false
	}
	containers, found, err := unstructured.NestedSlice(obj.Object, "spec", "template", "spec", "containers")
	if err != nil || !found {
		return false
	}
	replaced := false
	for i, raw := range containers {
		container, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if currentImage, _ := container["image"].(string); currentImage == defaultRegistryDeploymentImage {
			container["image"] = image
			containers[i] = container
			replaced = true
		}
	}
	if replaced {
		_ = unstructured.SetNestedSlice(obj.Object, containers, "spec", "template", "spec", "containers")
	}
	return replaced
}

func ensureRegistryStorageSizeClientGo(logger *zap.Logger, clients *k8sclient.Clients, namespace, registryStorageSize string) error {
	storageSize := strings.TrimSpace(registryStorageSize)
	if storageSize == "" {
		return nil
	}
	currentSize, err := k8sclient.PersistentVolumeClaimStorage(context.Background(), clients, namespace, core.RegistryPVCName)
	if err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrReadRegistryStorageFailed,
			err,
			fmt.Sprintf("failed to read current registry storage size: %v", err),
			map[string]any{"namespace": namespace, "pvc": core.RegistryPVCName, "component": "registry"},
		)
		core.Error("Failed to read registry storage size")
		core.LogStructuredError(logger, wrappedErr, "Failed to read registry storage size")
		return wrappedErr
	}
	if currentSize == storageSize {
		if logger != nil {
			logger.Info("Registry storage size already matches requested value", zap.String("size", storageSize))
		}
		return nil
	}
	if logger != nil {
		logger.Info("Updating registry storage size", zap.String("from", currentSize), zap.String("to", storageSize))
	}
	if err := k8sclient.UpdatePersistentVolumeClaimStorage(context.Background(), clients, namespace, core.RegistryPVCName, storageSize); err != nil {
		wrappedErr := core.WrapWithBaseAndContext(
			core.ErrUpdateRegistryStorageFailed,
			err,
			fmt.Sprintf("failed to update registry storage size to %s: %v", storageSize, err),
			map[string]any{"namespace": namespace, "pvc": core.RegistryPVCName, "storage_size": storageSize, "component": "registry"},
		)
		core.Error("Failed to update registry storage size")
		core.LogStructuredError(logger, wrappedErr, "Failed to update registry storage size")
		return wrappedErr
	}
	return nil
}
