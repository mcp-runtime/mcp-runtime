// Package workloadidentity resolves the public configuration used by workload
// certificate diagnostics and authenticated adapter enrollment preflight.
package workloadidentity

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type ConfigMapGetter func(context.Context, string, string) (*corev1.ConfigMap, error)

// DeploymentConfig resolves only the selected container and requested keys,
// honoring explicit env over envFrom and the last envFrom source over earlier
// sources. It never fetches Secrets; an unresolved source is an error, not off.
func DeploymentConfig(ctx context.Context, deployment *appsv1.Deployment, containerName string, keys []string, getConfigMap ConfigMapGetter) (map[string]string, error) {
	if deployment == nil {
		return nil, fmt.Errorf("deployment is unavailable")
	}
	var container *corev1.Container
	for i := range deployment.Spec.Template.Spec.Containers {
		if deployment.Spec.Template.Spec.Containers[i].Name == containerName {
			container = &deployment.Spec.Template.Spec.Containers[i]
			break
		}
	}
	if container == nil {
		return nil, fmt.Errorf("container %q is unavailable", containerName)
	}
	values := make(map[string]string, len(keys))
	for _, key := range keys {
		value, err := containerValue(ctx, deployment.Namespace, container, key, getConfigMap)
		if err != nil {
			return nil, fmt.Errorf("cannot resolve %s: %w", key, err)
		}
		values[key] = strings.TrimSpace(value)
	}
	return values, nil
}

func configMapValue(ctx context.Context, namespace, name, key string, optionalMap, optionalKey bool, get ConfigMapGetter) (string, bool, error) {
	if get == nil {
		return "", false, fmt.Errorf("ConfigMap reader is unavailable")
	}
	cm, err := get(ctx, namespace, name)
	if err != nil {
		if optionalMap && apierrors.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("ConfigMap %s/%s is unavailable", namespace, name)
	}
	if cm == nil {
		return "", false, fmt.Errorf("ConfigMap %s/%s is unavailable", namespace, name)
	}
	value, found := cm.Data[key]
	if !found && !optionalKey {
		return "", false, fmt.Errorf("ConfigMap %s/%s has no key %q", namespace, name, key)
	}
	return value, found, nil
}

func containerValue(ctx context.Context, namespace string, container *corev1.Container, key string, get ConfigMapGetter) (string, error) {
	for i := len(container.Env) - 1; i >= 0; i-- {
		env := container.Env[i]
		if env.Name != key {
			continue
		}
		if env.ValueFrom == nil {
			if strings.Contains(env.Value, "$(") {
				return "", fmt.Errorf("environment expansion is unsupported")
			}
			return env.Value, nil
		}
		ref := env.ValueFrom.ConfigMapKeyRef
		if ref == nil {
			return "", fmt.Errorf("Secret or unsupported environment source cannot be inspected")
		}
		optional := ref.Optional != nil && *ref.Optional
		value, found, err := configMapValue(ctx, namespace, ref.Name, ref.Key, optional, optional, get)
		if err != nil || found {
			return value, err
		}
		break // Optional missing value leaves any envFrom value intact.
	}
	for i := len(container.EnvFrom) - 1; i >= 0; i-- {
		source := container.EnvFrom[i]
		if !strings.HasPrefix(key, source.Prefix) {
			continue
		}
		if source.ConfigMapRef == nil {
			return "", fmt.Errorf("Secret or unsupported envFrom source cannot be inspected")
		}
		ref := source.ConfigMapRef
		value, found, err := configMapValue(ctx, namespace, ref.Name, strings.TrimPrefix(key, source.Prefix), ref.Optional != nil && *ref.Optional, true, get)
		if err != nil {
			return "", err
		}
		if found {
			return value, nil
		}
	}
	return "", nil
}
