package runtimeapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/util/retry"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/registryauth"
)

func (s *DeploymentService) ensureNativeRegistryPullSecret(ctx context.Context, client kubernetes.Interface, namespace string) error {
	existing, err := client.CoreV1().Secrets(namespace).Get(ctx, registryPullSecretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	create := apierrors.IsNotFound(err)
	reusable := false
	if err == nil && existing.Labels["mcpruntime.org/registry-auth"] == "pull" {
		expiry, _ := time.Parse(time.RFC3339, existing.Annotations["mcpruntime.org/registry-credential-expires"])
		var config struct {
			Auths map[string]struct {
				Password string `json:"password"`
			} `json:"auths"`
		}
		if json.Unmarshal(existing.Data[corev1.DockerConfigJsonKey], &config) == nil && len(config.Auths) > 0 && expiry.After(time.Now().Add(7*24*time.Hour)) {
			reusable = true
			for _, auth := range config.Auths {
				if !strings.HasPrefix(auth.Password, "mcpp_") {
					reusable = false
				}
			}
		}
	}
	if !reusable {
		base := strings.TrimSpace(os.Getenv("PLATFORM_API_URL"))
		if base == "" {
			base = "http://mcp-platform-api.mcp-platform.svc:8080"
		}
		u, err := url.Parse(base)
		if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
			return fmt.Errorf("invalid platform API registry credential endpoint")
		}
		u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/registry/pull-credentials"
		u.RawQuery = ""
		u.Fragment = ""
		key := registryPullSecretAPIKey()
		if key == "" {
			return fmt.Errorf("native registry credential provisioning requires a platform service key")
		}
		body, _ := json.Marshal(registryauth.PullScope{Namespace: namespace, Repositories: []string{"mcp-gateway"}})
		callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		request, err := http.NewRequestWithContext(callCtx, http.MethodPost, u.String(), bytes.NewReader(body))
		if err != nil {
			return err
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("x-api-key", key)
		httpClient := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		// #nosec G704 -- URL comes only from operator PLATFORM_API_URL, never request input.
		response, err := httpClient.Do(request)
		if err != nil {
			return fmt.Errorf("native registry credential broker unavailable")
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusCreated {
			return fmt.Errorf("native registry credential broker returned HTTP %d", response.StatusCode)
		}
		var credential struct {
			ID, Username, Password string
			ExpiresAt              time.Time `json:"expires_at"`
		}
		if json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&credential) != nil || !strings.HasPrefix(credential.Password, "mcpp_") || credential.Username != "mcp-pull-"+namespace || credential.ID == "" || !credential.ExpiresAt.After(time.Now()) {
			return fmt.Errorf("invalid native registry credential response")
		}
		hosts := []string{"registry.registry.svc:5000", "registry.registry:5000"}
		domain := strings.Trim(strings.TrimSpace(os.Getenv("MCP_CLUSTER_DOMAIN")), ".")
		if domain == "" {
			domain = "cluster.local"
		}
		hosts = append(hosts, "registry.registry.svc."+domain+":5000")
		if host := registryPullSecretHost(); host != "" {
			hosts = append(hosts, host)
		}
		auths := map[string]any{}
		for _, host := range hosts {
			auths[host] = map[string]string{"username": credential.Username, "password": credential.Password, "auth": base64.StdEncoding.EncodeToString([]byte(credential.Username + ":" + credential.Password))}
		}
		config, _ := json.Marshal(map[string]any{"auths": auths})
		if create {
			existing = &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: registryPullSecretName, Namespace: namespace}}
		}
		if existing.Labels == nil {
			existing.Labels = map[string]string{}
		}
		existing.Labels["mcpruntime.org/registry-auth"] = "pull"
		if existing.Annotations == nil {
			existing.Annotations = map[string]string{}
		}
		existing.Annotations["mcpruntime.org/registry-credential-id"] = credential.ID
		existing.Annotations["mcpruntime.org/registry-credential-expires"] = credential.ExpiresAt.Format(time.RFC3339)
		existing.Type = corev1.SecretTypeDockerConfigJson
		existing.Data = map[string][]byte{corev1.DockerConfigJsonKey: config}
		if create {
			_, err = client.CoreV1().Secrets(namespace).Create(ctx, existing, metav1.CreateOptions{})
		} else {
			_, err = client.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{})
		}
		if err != nil {
			return err
		}
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		account, err := client.CoreV1().ServiceAccounts(namespace).Get(ctx, kubeworkload.DefaultServiceAccountName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, ref := range account.ImagePullSecrets {
			if ref.Name == registryPullSecretName {
				return nil
			}
		}
		account.ImagePullSecrets = append(account.ImagePullSecrets, corev1.LocalObjectReference{Name: registryPullSecretName})
		_, err = client.CoreV1().ServiceAccounts(namespace).Update(ctx, account, metav1.UpdateOptions{})
		return err
	})
}
