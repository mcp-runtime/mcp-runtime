package runtimeapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"mcp-runtime/pkg/registrypush"
	"mcp-runtime/pkg/sentinel"
)

const (
	registryPushTransferPath         = "/internal/registry-push/tar"
	registryPushTransferLabelKey     = "mcp-runtime.org/registry-push-transfer"
	registryPushTransferSecretPrefix = "registry-push-transfer-"
	defaultTransferNamespace         = sentinel.PlatformNamespace
)

type registryPushTransferRecord struct {
	path      string
	podIP     string
	expiresAt time.Time
}

func registryPushTransferNamespace() string {
	ns := strings.TrimSpace(os.Getenv("MCP_REGISTRY_PUSH_TRANSFER_NAMESPACE"))
	if ns == "" {
		ns = strings.TrimSpace(os.Getenv("MCP_REGISTRY_PUSH_HELPER_NAMESPACE"))
	}
	if ns == "" {
		return defaultTransferNamespace
	}
	return ns
}

func ownerPodIP() string {
	return strings.TrimSpace(os.Getenv("POD_IP"))
}

func apiListenPort() string {
	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		return "8080"
	}
	return port
}

func newRegistryPushTransferToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func registryPushTransferSecretName(token string) string {
	return registryPushTransferSecretPrefix + token
}

func (s *RegistryPushService) registerRegistryPushTransfer(ctx context.Context, path string, ttl time.Duration) (token, fetchURL string, err error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", "", fmt.Errorf("transfer path is required")
	}
	if s.k8sClients == nil || s.k8sClients.Clientset == nil {
		return "", "", fmt.Errorf("kubernetes client is required for registry push transfer")
	}
	podIP := ownerPodIP()
	if podIP == "" {
		return "", "", fmt.Errorf("POD_IP is required for registry push transfer")
	}
	if ttl <= 0 {
		ttl = 15 * time.Minute
	}
	token, err = newRegistryPushTransferToken()
	if err != nil {
		return "", "", err
	}

	s.purgeExpiredRegistryPushTransfers(ctx)

	ns := registryPushTransferNamespace()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      registryPushTransferSecretName(token),
			Namespace: ns,
			Labels: map[string]string{
				registryPushTransferLabelKey: "true",
			},
		},
		Type: corev1.SecretTypeOpaque,
		StringData: map[string]string{
			"path":      path,
			"podIP":     podIP,
			"expiresAt": time.Now().Add(ttl).UTC().Format(time.RFC3339),
		},
	}
	if _, err := s.k8sClients.Clientset.CoreV1().Secrets(ns).Create(ctx, secret, metav1.CreateOptions{}); err != nil {
		return "", "", fmt.Errorf("register registry push transfer: %w", err)
	}
	fetchURL = fmt.Sprintf("http://%s:%s%s", podIP, apiListenPort(), registryPushTransferPath)
	return token, fetchURL, nil
}

func parseRegistryPushTransferSecret(secret *corev1.Secret) (registryPushTransferRecord, bool) {
	if secret == nil {
		return registryPushTransferRecord{}, false
	}
	path := secretField(secret, "path")
	podIP := secretField(secret, "podIP")
	expiresRaw := secretField(secret, "expiresAt")
	if path == "" {
		return registryPushTransferRecord{}, false
	}
	expiresAt, err := time.Parse(time.RFC3339, expiresRaw)
	if err != nil {
		expiresAt = time.Time{}
	}
	return registryPushTransferRecord{
		path:      path,
		podIP:     podIP,
		expiresAt: expiresAt,
	}, true
}

func secretField(secret *corev1.Secret, key string) string {
	if secret.Data != nil {
		if value, ok := secret.Data[key]; ok {
			return strings.TrimSpace(string(value))
		}
	}
	if secret.StringData != nil {
		return strings.TrimSpace(secret.StringData[key])
	}
	return ""
}

func (s *RegistryPushService) lookupRegistryPushTransfer(ctx context.Context, token string) (registryPushTransferRecord, bool) {
	token = strings.TrimSpace(token)
	if token == "" || s.k8sClients == nil || s.k8sClients.Clientset == nil {
		return registryPushTransferRecord{}, false
	}
	ns := registryPushTransferNamespace()
	secret, err := s.k8sClients.Clientset.CoreV1().Secrets(ns).Get(ctx, registryPushTransferSecretName(token), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return registryPushTransferRecord{}, false
	}
	if err != nil {
		return registryPushTransferRecord{}, false
	}
	record, ok := parseRegistryPushTransferSecret(secret)
	if !ok {
		return registryPushTransferRecord{}, false
	}
	if !record.expiresAt.IsZero() && time.Now().After(record.expiresAt) {
		s.deleteRegistryPushTransfer(ctx, token, record.path)
		return registryPushTransferRecord{}, false
	}
	return record, true
}

func (s *RegistryPushService) deleteRegistryPushTransfer(ctx context.Context, token, path string) {
	if s.k8sClients == nil || s.k8sClients.Clientset == nil {
		return
	}
	ns := registryPushTransferNamespace()
	_ = s.k8sClients.Clientset.CoreV1().Secrets(ns).Delete(ctx, registryPushTransferSecretName(token), metav1.DeleteOptions{})
	if path != "" && ownerPodIP() != "" {
		_ = os.Remove(path)
	}
}

func (s *RegistryPushService) revokeRegistryPushTransfer(ctx context.Context, token string) {
	record, ok := s.lookupRegistryPushTransfer(ctx, token)
	if !ok {
		return
	}
	s.deleteRegistryPushTransfer(ctx, token, record.path)
}

func (s *RegistryPushService) purgeExpiredRegistryPushTransfers(ctx context.Context) {
	if s == nil || s.k8sClients == nil || s.k8sClients.Clientset == nil {
		return
	}
	ns := registryPushTransferNamespace()
	secrets, err := s.k8sClients.Clientset.CoreV1().Secrets(ns).List(ctx, metav1.ListOptions{
		LabelSelector: registryPushTransferLabelKey + "=true",
	})
	if err != nil {
		return
	}
	now := time.Now()
	for _, secret := range secrets.Items {
		record, ok := parseRegistryPushTransferSecret(&secret)
		if !ok {
			continue
		}
		if record.expiresAt.IsZero() || now.After(record.expiresAt) {
			token := strings.TrimPrefix(secret.Name, registryPushTransferSecretPrefix)
			s.deleteRegistryPushTransfer(ctx, token, record.path)
		}
	}
}

// HandleRegistryPushTransfer serves a one-time docker save tar to in-cluster helper pods.
func (s *RegistryPushService) HandleRegistryPushTransfer(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", "GET")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if r.URL.Path != registryPushTransferPath {
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}
	token := strings.TrimSpace(r.Header.Get(registrypush.TransferTokenHeader))
	if token == "" {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	record, ok := s.lookupRegistryPushTransfer(r.Context(), token)
	if !ok {
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}
	if record.podIP != "" && ownerPodIP() != "" && record.podIP != ownerPodIP() {
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}

	file, err := os.Open(record.path)
	if err != nil {
		writeAPIError(w, http.StatusNotFound, "not_found")
		return
	}
	defer file.Close()

	s.deleteRegistryPushTransfer(r.Context(), token, record.path)

	w.Header().Set("content-type", "application/x-tar")
	http.ServeContent(w, r, "image.tar", time.Time{}, file)
}
