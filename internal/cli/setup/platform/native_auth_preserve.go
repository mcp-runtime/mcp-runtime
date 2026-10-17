package platform

import (
	"context"
	"fmt"
	"io"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	yamlutil "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/yaml"
)

// Retain the broker signer and native pull mode before rolling platform pods.
// Restoring these only after setup cannot work if the broker's own image pull
// already needs the running native token service.
func preserveNativePlatformManifest(ctx context.Context, cs kubernetes.Interface, manifest string) (string, error) {
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
		obj := &unstructured.Unstructured{Object: object}
		if obj.GetKind() == "Deployment" && (obj.GetName() == "mcp-platform-api" || obj.GetName() == "mcp-runtime-api") {
			live, err := cs.AppsV1().Deployments(obj.GetNamespace()).Get(ctx, obj.GetName(), metav1.GetOptions{})
			if err != nil && !apierrors.IsNotFound(err) {
				return "", fmt.Errorf("inspect native authentication for %s: %w", obj.GetName(), err)
			}
			if err == nil {
				var desired appsv1.Deployment
				if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object, &desired); err != nil {
					return "", err
				}
				preserveNativePlatformPod(&desired.Spec.Template.Spec, live.Spec.Template.Spec)
				if err := preserveCredentialBrokerDeployment(&desired, live); err != nil {
					return "", err
				}
				spec, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&desired.Spec.Template)
				if err != nil {
					return "", err
				}
				if err := unstructured.SetNestedMap(object, spec, "spec", "template"); err != nil {
					return "", err
				}
			}
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

func preserveNativePlatformPod(desired *corev1.PodSpec, live corev1.PodSpec) {
	signer := false
	for i := range desired.Containers {
		dst := &desired.Containers[i]
		for _, source := range live.Containers {
			if source.Name != dst.Name {
				continue
			}
			for _, env := range source.Env {
				if (dst.Name == "platform-api" && strings.HasPrefix(env.Name, "REGISTRY_TOKEN_")) ||
					(dst.Name == "runtime-api" && env.Name == "MCP_REGISTRY_NATIVE_AUTH") {
					setNativePreservedEnv(dst, env)
					if dst.Name == "platform-api" {
						signer = true
					}
				}
			}
			if signer && dst.Name == "platform-api" {
				for _, mount := range source.VolumeMounts {
					if mount.Name != "registry-token-signer" {
						continue
					}
					found := false
					for j := range dst.VolumeMounts {
						if dst.VolumeMounts[j].Name == mount.Name {
							dst.VolumeMounts[j] = mount
							found = true
							break
						}
					}
					if !found {
						dst.VolumeMounts = append(dst.VolumeMounts, mount)
					}
				}
			}
		}
	}
	if signer {
		for _, volume := range live.Volumes {
			if volume.Name != "registry-token-signer" {
				continue
			}
			found := false
			for j := range desired.Volumes {
				if desired.Volumes[j].Name == volume.Name {
					desired.Volumes[j] = volume
					found = true
					break
				}
			}
			if !found {
				desired.Volumes = append(desired.Volumes, volume)
			}
		}
		if live.SecurityContext != nil && live.SecurityContext.FSGroup != nil {
			if desired.SecurityContext == nil {
				desired.SecurityContext = &corev1.PodSecurityContext{}
			}
			group := *live.SecurityContext.FSGroup
			desired.SecurityContext.FSGroup = &group
		}
	}
}

func setNativePreservedEnv(container *corev1.Container, env corev1.EnvVar) {
	for i := range container.Env {
		if container.Env[i].Name == env.Name {
			container.Env[i] = env
			return
		}
	}
	container.Env = append(container.Env, env)
}
