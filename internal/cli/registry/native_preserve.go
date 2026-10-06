package registry

import (
	"context"
	"fmt"
	"io"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
	"strings"
)

// Keep setup and compatibility-overlay reapplication from dropping native auth.
func preserveNativeRegistryManifest(ctx context.Context, cs kubernetes.Interface, namespace, manifest string) (string, error) {
	dep, err := cs.AppsV1().Deployments(namespace).Get(ctx, "registry", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return manifest, nil
	}
	if err != nil {
		return "", err
	}
	var source *corev1.Container
	for i := range dep.Spec.Template.Spec.Containers {
		container := &dep.Spec.Template.Spec.Containers[i]
		for _, env := range container.Env {
			if env.Name == "REGISTRY_AUTH" && env.Value == "token" {
				source = container
			}
		}
	}
	if source == nil {
		return manifest, nil
	}
	decoder := yamlutil.NewYAMLOrJSONDecoder(strings.NewReader(manifest), 4096)
	var output strings.Builder
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err == io.EOF {
			break
		} else if err != nil {
			return "", err
		}
		if len(object) == 0 {
			continue
		}
		kind, _ := object["kind"].(string)
		name, _, _ := unstructured.NestedString(object, "metadata", "name")
		if kind == "Deployment" && name == "registry" {
			containers, found, err := unstructured.NestedSlice(object, "spec", "template", "spec", "containers")
			if err != nil || !found {
				return "", fmt.Errorf("registry manifest has no containers")
			}
			for i, value := range containers {
				container, ok := value.(map[string]any)
				if !ok {
					return "", fmt.Errorf("invalid registry container")
				}
				if container["name"] != "registry" {
					continue
				}
				envs, _, _ := unstructured.NestedSlice(container, "env")
				var kept []any
				for _, value := range envs {
					env, ok := value.(map[string]any)
					if !ok {
						return "", fmt.Errorf("invalid registry env")
					}
					key, _ := env["name"].(string)
					if key != "REGISTRY_AUTH" && !strings.HasPrefix(key, "REGISTRY_AUTH_TOKEN_") {
						kept = append(kept, value)
					}
				}
				for _, env := range source.Env {
					if env.Name == "REGISTRY_AUTH" || strings.HasPrefix(env.Name, "REGISTRY_AUTH_TOKEN_") {
						item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&env)
						if err != nil {
							return "", err
						}
						kept = append(kept, item)
					}
				}
				container["env"] = kept
				mounts, _, _ := unstructured.NestedSlice(container, "volumeMounts")
				for _, mount := range source.VolumeMounts {
					if mount.Name == "registry-token-root" {
						item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&mount)
						if err != nil {
							return "", err
						}
						mounts = replaceNativeNamed(mounts, item)
					}
				}
				container["volumeMounts"] = mounts
				containers[i] = container
			}
			_ = unstructured.SetNestedSlice(object, containers, "spec", "template", "spec", "containers")
			volumes, _, _ := unstructured.NestedSlice(object, "spec", "template", "spec", "volumes")
			for _, volume := range dep.Spec.Template.Spec.Volumes {
				if volume.Name == "registry-token-root" {
					item, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&volume)
					if err != nil {
						return "", err
					}
					volumes = replaceNativeNamed(volumes, item)
				}
			}
			_ = unstructured.SetNestedSlice(object, volumes, "spec", "template", "spec", "volumes")
			if dep.Spec.Template.Spec.SecurityContext != nil && dep.Spec.Template.Spec.SecurityContext.FSGroup != nil {
				_ = unstructured.SetNestedField(object, *dep.Spec.Template.Spec.SecurityContext.FSGroup, "spec", "template", "spec", "securityContext", "fsGroup")
			}
		}
		if kind == "Service" && name == "registry" {
			_ = unstructured.SetNestedField(object, "ClusterIP", "spec", "type")
			ports, _, _ := unstructured.NestedSlice(object, "spec", "ports")
			for _, value := range ports {
				if port, ok := value.(map[string]any); ok {
					delete(port, "nodePort")
				}
			}
			_ = unstructured.SetNestedSlice(object, ports, "spec", "ports")
		}
		data, err := yaml.Marshal(object)
		if err != nil {
			return "", err
		}
		output.WriteString("---\n")
		output.Write(data)
	}
	return output.String(), nil
}
func replaceNativeNamed(items []any, item map[string]any) []any {
	for i, value := range items {
		if old, ok := value.(map[string]any); ok && old["name"] == item["name"] {
			items[i] = item
			return items
		}
	}
	return append(items, item)
}
