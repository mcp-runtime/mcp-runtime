package runtimeapi

import (
	"context"
	"fmt"
	"mcp-runtime-api/internal/runtimeapi/k8sprovision"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
	"mcp-runtime/pkg/kubeworkload"
)

// Only the runtime-managed analytics credential is an exclusively generated
// server credential. User-provided credentials and namespace pull secrets are
// never inferred to be owned from their names.
func (s *RuntimeServer) retireServerCredentials(ctx context.Context, namespace, name string, current *mcpv1alpha1.MCPServer) error {
	secretName := publishedServerAnalyticsSecretName(name)
	secrets := s.k8sClients.Clientset.CoreV1().Secrets(namespace)
	secret, err := s.serverRetirementCredential(ctx, namespace, secretName)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if secret.Labels["app.kubernetes.io/managed-by"] != "mcp-runtime" {
		return nil
	}
	if owner := secret.Annotations["mcpruntime.org/credential-server"]; owner != "" && owner != name {
		return nil
	}
	// Preserve pre-existing credentials used by another server (including long
	// server names whose deterministic generated credential names collide).
	manager := mcpaccess.NewManager(s.k8sClients.Dynamic, s.k8sClients.Clientset)
	servers, err := manager.ListMCPServers(ctx, namespace)
	if err != nil {
		return err
	}
	for _, server := range servers.Items {
		if server.Name == name {
			continue
		}
		if serverUsesCredential(server.Spec, secretName) {
			return nil
		}
	}
	if current != nil {
		for _, pull := range current.Spec.ImagePullSecrets {
			if pull == secretName {
				return nil
			}
		}
	}
	// UID preconditions prevent removing a replacement secret created during
	// a retry or concurrent publication.
	uid := secret.UID
	err = secrets.Delete(ctx, secretName, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func serverUsesCredential(spec mcpv1alpha1.MCPServerSpec, name string) bool {
	if spec.Analytics != nil && spec.Analytics.APIKeySecretRef != nil && spec.Analytics.APIKeySecretRef.Name == name {
		return true
	}
	for _, env := range spec.SecretEnvVars {
		if env.SecretKeyRef != nil && env.SecretKeyRef.Name == name {
			return true
		}
	}
	for _, pull := range spec.ImagePullSecrets {
		if pull == name {
			return true
		}
	}
	return false
}

// Called only after the retirement handler has checked namespace and server
// ownership. CLI-applied servers can predate the namespace-local API binding;
// repair that binding without granting access to infrastructure namespaces.
func (s *RuntimeServer) serverRetirementCredential(ctx context.Context, namespace, name string) (*corev1.Secret, error) {
	secrets := s.k8sClients.Clientset.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if !apierrors.IsForbidden(err) || kubeworkload.OperatorSecretNamespaceProtected(namespace) {
		return secret, err
	}
	if err := k8sprovision.EnsurePlatformAPISecretAccess(ctx, s.k8sClients.Clientset, namespace); err != nil {
		return nil, fmt.Errorf("ensure retirement credential access: %w", err)
	}
	// Kubernetes authorizer caches can lag a newly created RoleBinding. Retry
	// only Forbidden; absence and other API errors retain their normal meaning.
	var readErr error
	err = wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		secret, readErr = secrets.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsForbidden(readErr) {
			return false, nil
		}
		return true, readErr
	})
	if err != nil && apierrors.IsForbidden(readErr) {
		return nil, fmt.Errorf("waiting for retirement credential access: %w (last error: %v)", err, readErr)
	}
	return secret, err
}
