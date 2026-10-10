package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"go.uber.org/zap"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/kube"
	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/pkg/k8sclient"
)

func ensureRepoManagedTraefikMiddlewareResources(kubectl core.KubectlRunner, logger *zap.Logger) error {
	namespaces, err := activeNamedTraefikDeploymentNamespacesWithKubectl(kubectl)
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		if logger != nil {
			logger.Info("Reconciling Traefik file-provider resources", zap.String("namespace", namespace))
		}
		if err := applyTraefikSupportManifest(kubectl, "config/ingress/overlays/http/dynamic-config.yaml", namespace); err != nil {
			return err
		}
		for _, plugin := range localTraefikPlugins {
			if err := applyTraefikSupportManifest(kubectl, plugin.manifest, namespace); err != nil {
				return err
			}
		}
		if err := patchTraefikDeploymentForFileMiddlewareSupport(kubectl, namespace); err != nil {
			return err
		}
	}
	return nil
}

func ensureRepoManagedTraefikMiddlewareResourcesClientGo(logger *zap.Logger) error {
	namespaces, err := activeNamedTraefikDeploymentNamespacesClientGo()
	if err != nil {
		return err
	}
	for _, namespace := range namespaces {
		if logger != nil {
			logger.Info("Reconciling Traefik file-provider resources", zap.String("namespace", namespace))
		}
		if err := applyTraefikSupportManifestClientGo("config/ingress/overlays/http/dynamic-config.yaml", namespace); err != nil {
			return err
		}
		for _, plugin := range localTraefikPlugins {
			if err := applyTraefikSupportManifestClientGo(plugin.manifest, namespace); err != nil {
				return err
			}
		}
		if err := patchTraefikDeploymentForFileMiddlewareSupportClientGo(namespace); err != nil {
			return err
		}
	}
	return nil
}

func activeNamedTraefikDeploymentNamespacesClientGo() ([]string, error) {
	clients, err := platformKubernetesClients()
	if err != nil {
		return nil, err
	}
	namespaces, err := k8sclient.ListDeploymentNamespacesByName(context.Background(), clients, "traefik")
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupListTraefikDeploymentsFailed, err, fmt.Sprintf("list traefik deployments: %v", err))
	}
	return namespaces, nil
}

func activeNamedTraefikDeploymentNamespacesWithKubectl(kubectl core.KubectlRunner) ([]string, error) {
	cmd, err := kubectl.CommandArgs([]string{
		"get", "deployment", "-A", "--no-headers",
		"-o", "custom-columns=NS:.metadata.namespace,NAME:.metadata.name",
	})
	if err != nil {
		return nil, err
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupListTraefikDeploymentsFailed, err, fmt.Sprintf("list traefik deployments: %v", err))
	}
	seen := map[string]struct{}{}
	var namespaces []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != "traefik" {
			continue
		}
		ns := strings.TrimSpace(fields[0])
		if ns == "" {
			continue
		}
		if _, ok := seen[ns]; ok {
			continue
		}
		seen[ns] = struct{}{}
		namespaces = append(namespaces, ns)
	}
	slices.Sort(namespaces)
	return namespaces, nil
}

func applyTraefikSupportManifest(kubectl core.KubectlRunner, relPath, namespace string) error {
	resolvedPath, err := assetpath.ResolveRepoAssetPath(relPath)
	if err != nil {
		return core.WrapWithBase(core.ErrReadIngressManifestFailed, err, fmt.Sprintf("failed to resolve Traefik manifest %s: %v", relPath, err))
	}
	manifestBytes, err := kube.ReadFileAtPath(resolvedPath)
	if err != nil {
		return core.WrapWithBase(core.ErrReadIngressManifestFailed, err, fmt.Sprintf("failed to read Traefik manifest %s: %v", relPath, err))
	}
	manifestContent := strings.ReplaceAll(string(manifestBytes), "namespace: traefik", "namespace: "+namespace)
	if err := kube.ApplyManifestContent(kubectl.CommandArgs, manifestContent); err != nil {
		return core.WrapWithBase(
			core.ErrInstallIngressControllerFailed,
			err,
			fmt.Sprintf("failed to reconcile Traefik manifest %s in namespace %s: %v", relPath, namespace, err),
		)
	}
	return nil
}

func applyTraefikSupportManifestClientGo(relPath, namespace string) error {
	resolvedPath, err := assetpath.ResolveRepoAssetPath(relPath)
	if err != nil {
		return core.WrapWithBase(core.ErrReadIngressManifestFailed, err, fmt.Sprintf("failed to resolve Traefik manifest %s: %v", relPath, err))
	}
	manifestBytes, err := kube.ReadFileAtPath(resolvedPath)
	if err != nil {
		return core.WrapWithBase(core.ErrReadIngressManifestFailed, err, fmt.Sprintf("failed to read Traefik manifest %s: %v", relPath, err))
	}
	manifestContent := strings.ReplaceAll(string(manifestBytes), "namespace: traefik", "namespace: "+namespace)
	if err := applyManifestYAML(manifestContent, "", os.Stdout); err != nil {
		return core.WrapWithBase(
			core.ErrInstallIngressControllerFailed,
			err,
			fmt.Sprintf("failed to reconcile Traefik manifest %s in namespace %s: %v", relPath, namespace, err),
		)
	}
	return nil
}

func patchTraefikDeploymentForFileMiddlewareSupport(kubectl core.KubectlRunner, namespace string) error {
	spec, err := readTraefikDeploymentSpec(kubectl, namespace)
	if err != nil {
		return err
	}
	patchBytes, err := traefikMiddlewarePatch(spec, namespace)
	if err != nil || len(patchBytes) == 0 {
		return err
	}
	if err := kubectl.RunWithOutput([]string{
		"patch", "deployment", "traefik", "-n", namespace, "--type=json", "-p", string(patchBytes),
	}, os.Stdout, os.Stderr); err != nil {
		return core.WrapWithBase(
			core.ErrInstallIngressControllerFailed,
			err,
			fmt.Sprintf("failed to patch traefik deployment in namespace %s for file-provider middleware support: %v", namespace, err),
		)
	}
	return nil
}

func patchTraefikDeploymentForFileMiddlewareSupportClientGo(namespace string) error {
	spec, err := readTraefikDeploymentSpecClientGo(namespace)
	if err != nil {
		return err
	}
	patchBytes, err := traefikMiddlewarePatch(spec, namespace)
	if err != nil || len(patchBytes) == 0 {
		return err
	}
	clients, err := platformKubernetesClients()
	if err != nil {
		return err
	}
	if err := k8sclient.PatchDeploymentJSON(context.Background(), clients, namespace, "traefik", patchBytes); err != nil {
		return core.WrapWithBase(
			core.ErrInstallIngressControllerFailed,
			err,
			fmt.Sprintf("failed to patch traefik deployment in namespace %s for file-provider middleware support: %v", namespace, err),
		)
	}
	return nil
}

func traefikMiddlewarePatch(spec traefikDeploymentSpec, namespace string) ([]byte, error) {
	if len(spec.Spec.Template.Spec.Containers) == 0 {
		return nil, core.NewWithBase(core.ErrInstallIngressControllerFailed, fmt.Sprintf("traefik deployment in namespace %s has no containers", namespace))
	}
	containerIndex := -1
	for i, candidate := range spec.Spec.Template.Spec.Containers {
		if candidate.Name == "traefik" {
			containerIndex = i
			break
		}
	}
	if containerIndex == -1 {
		return nil, core.NewWithBase(core.ErrInstallIngressControllerFailed, fmt.Sprintf("traefik deployment in namespace %s has no container named traefik", namespace))
	}
	container := spec.Spec.Template.Spec.Containers[containerIndex]

	var ops []jsonPatchOperation
	if !containsString(container.Args, "--providers.file.filename=/etc/traefik/dynamic/dynamic.yml") {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/args/-", containerIndex), Value: "--providers.file.filename=/etc/traefik/dynamic/dynamic.yml"})
	}
	if !containsString(container.Args, "--providers.file.watch=true") {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/args/-", containerIndex), Value: "--providers.file.watch=true"})
	}
	for _, plugin := range localTraefikPlugins {
		if !containsPluginArg(container.Args, plugin.name) {
			ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/args/-", containerIndex), Value: plugin.arg()})
		}
	}
	addDynamicMount := !hasVolumeMountPath(container.VolumeMounts, "/etc/traefik/dynamic")
	if addDynamicMount {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/volumeMounts/-", containerIndex), Value: map[string]any{"name": "traefik-dynamic", "mountPath": "/etc/traefik/dynamic", "readOnly": true}})
	}
	addPluginSourceMounts := make([]bool, len(localTraefikPlugins))
	for i, plugin := range localTraefikPlugins {
		addPluginSourceMounts[i] = !hasVolumeMountPath(container.VolumeMounts, plugin.mountPath())
		if addPluginSourceMounts[i] {
			ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/volumeMounts/-", containerIndex), Value: map[string]any{"name": plugin.volume, "mountPath": plugin.mountPath(), "readOnly": true}})
		}
	}
	addPluginStorageMount := !hasVolumeMountPath(container.VolumeMounts, "/plugins-storage")
	if addPluginStorageMount {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: fmt.Sprintf("/spec/template/spec/containers/%d/volumeMounts/-", containerIndex), Value: map[string]any{"name": "traefik-plugins", "mountPath": "/plugins-storage"}})
	}
	if addDynamicMount && !hasVolume(spec.Spec.Template.Spec.Volumes, "traefik-dynamic") {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: "/spec/template/spec/volumes/-", Value: map[string]any{"name": "traefik-dynamic", "configMap": map[string]any{"name": "traefik-dynamic", "items": []map[string]any{{"key": "dynamic.yml", "path": "dynamic.yml"}}}}})
	}
	for i, plugin := range localTraefikPlugins {
		if addPluginSourceMounts[i] && !hasVolume(spec.Spec.Template.Spec.Volumes, plugin.volume) {
			ops = append(ops, jsonPatchOperation{Op: "add", Path: "/spec/template/spec/volumes/-", Value: map[string]any{"name": plugin.volume, "configMap": map[string]any{"name": plugin.configMap}}})
		}
	}
	if addPluginStorageMount && !hasVolume(spec.Spec.Template.Spec.Volumes, "traefik-plugins") {
		ops = append(ops, jsonPatchOperation{Op: "add", Path: "/spec/template/spec/volumes/-", Value: map[string]any{"name": "traefik-plugins", "emptyDir": map[string]any{}}})
	}
	if len(ops) == 0 {
		return nil, nil
	}
	patchBytes, err := json.Marshal(ops)
	if err != nil {
		return nil, core.WrapWithBase(core.ErrSetupMarshalTraefikDeploymentPatchFailed, err, fmt.Sprintf("marshal traefik deployment patch: %v", err))
	}
	return patchBytes, nil
}

func readTraefikDeploymentSpec(kubectl core.KubectlRunner, namespace string) (traefikDeploymentSpec, error) {
	var spec traefikDeploymentSpec
	cmd, err := kubectl.CommandArgs([]string{"get", "deployment", "traefik", "-n", namespace, "-o", "json"})
	if err != nil {
		return spec, err
	}
	out, err := cmd.Output()
	if err != nil {
		return spec, core.WrapWithBase(core.ErrSetupReadTraefikDeploymentFailed, err, fmt.Sprintf("read traefik deployment %s/traefik: %v", namespace, err))
	}
	if err := json.Unmarshal(out, &spec); err != nil {
		return spec, core.WrapWithBase(core.ErrSetupDecodeTraefikDeploymentFailed, err, fmt.Sprintf("decode traefik deployment %s/traefik: %v", namespace, err))
	}
	return spec, nil
}

func readTraefikDeploymentSpecClientGo(namespace string) (traefikDeploymentSpec, error) {
	var spec traefikDeploymentSpec
	clients, err := platformKubernetesClients()
	if err != nil {
		return spec, err
	}
	deploy, err := k8sclient.GetDeployment(context.Background(), clients, namespace, "traefik")
	if err != nil {
		return spec, core.WrapWithBase(core.ErrSetupReadTraefikDeploymentFailed, err, fmt.Sprintf("read traefik deployment %s/traefik: %v", namespace, err))
	}
	out, err := json.Marshal(deploy)
	if err != nil {
		return spec, core.WrapWithBase(core.ErrSetupDecodeTraefikDeploymentFailed, err, fmt.Sprintf("decode traefik deployment %s/traefik: %v", namespace, err))
	}
	if err := json.Unmarshal(out, &spec); err != nil {
		return spec, core.WrapWithBase(core.ErrSetupDecodeTraefikDeploymentFailed, err, fmt.Sprintf("decode traefik deployment %s/traefik: %v", namespace, err))
	}
	return spec, nil
}

// localTraefikPlugin is a Yaegi local plugin the platform's Traefik routes
// depend on. Setup installs each one into every active Traefik, including an
// external k3s Traefik, because a Middleware that references an unloaded
// plugin makes Traefik drop the whole route.
type localTraefikPlugin struct {
	name      string
	manifest  string
	configMap string
	volume    string
}

const localTraefikPluginModulePrefix = "github.com/Agent-Hellboy/mcp-runtime/traefik-plugins/"

// localTraefikPlugins keeps the pii-redactor volume name existing installs
// already use, so re-running setup does not add a second source volume.
var localTraefikPlugins = []localTraefikPlugin{
	{name: "pii-redactor", manifest: "config/ingress/overlays/http/plugin-source.yaml", configMap: "traefik-plugin-pii-redactor", volume: "traefik-plugin-source"},
	{name: "spiffe-identity", manifest: "config/ingress/base/spiffe-identity-plugin.yaml", configMap: "traefik-plugin-spiffe-identity", volume: "spiffe-identity-plugin"},
}

func (p localTraefikPlugin) arg() string {
	return "--experimental.localplugins." + p.name + ".modulename=" + localTraefikPluginModulePrefix + p.name
}

func (p localTraefikPlugin) mountPath() string {
	return "/plugins-local/src/" + localTraefikPluginModulePrefix + p.name
}

// containsPluginArg matches the plugin flag case-insensitively: the repo
// Traefik manifest spells it --experimental.localPlugins while setup has
// always written --experimental.localplugins, and Traefik accepts both.
func containsPluginArg(args []string, name string) bool {
	prefix := strings.ToLower("--experimental.localplugins." + name + ".modulename=")
	for _, arg := range args {
		if strings.HasPrefix(strings.ToLower(arg), prefix) {
			return true
		}
	}
	return false
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == target {
			return true
		}
	}
	return false
}

func hasVolumeMountPath(mounts []struct {
	Name      string `json:"name"`
	MountPath string `json:"mountPath"`
}, mountPath string) bool {
	for _, mount := range mounts {
		if mount.MountPath == mountPath {
			return true
		}
	}
	return false
}

func hasVolume(volumes []struct {
	Name string `json:"name"`
}, name string) bool {
	for _, volume := range volumes {
		if volume.Name == name {
			return true
		}
	}
	return false
}

func activeTraefikNamespaceForPlatform(kubectl core.KubectlRunner) string {
	return activeTraefikNamespaceForPlatformClientGo()
}

func activeTraefikNamespaceForPlatformClientGo() string {
	namespaces, err := activeNamedTraefikDeploymentNamespacesClientGo()
	if err != nil {
		return ""
	}
	return preferredTraefikNamespace(namespaces)
}

// preferredTraefikNamespace picks the live Traefik namespace: the
// repo-managed traefik namespace first, then k3s's kube-system.
func preferredTraefikNamespace(namespaces []string) string {
	if len(namespaces) == 0 {
		return ""
	}
	for _, preferred := range []string{"traefik", "kube-system"} {
		if slices.Contains(namespaces, preferred) {
			return preferred
		}
	}
	return namespaces[0]
}
