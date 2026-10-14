package runtimeapi

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	mcpaccess "mcp-runtime/pkg/access"
)

// Only the runtime-managed analytics credential is an exclusively generated
// server credential. User-provided credentials and namespace pull secrets are
// never inferred to be owned from their names.
func (s *RuntimeServer) retireServerCredentials(ctx context.Context, namespace, name string, current *mcpv1alpha1.MCPServer) error {
	secretName := publishedServerAnalyticsSecretName(name)
	secrets := s.k8sClients.Clientset.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
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
