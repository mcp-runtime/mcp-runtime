package registry

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"mcp-runtime/pkg/k8sclient"
)

type nativeAdminRotation struct {
	Secrets      []*corev1.Secret
	Replacement  map[string]string
	PublisherKey string
}

// Plan every consumer change before touching credentials. Previously copied
// admin keys are revoked across their owners, not just removed from team pods.
func planRegistryAdminRotation(ctx context.Context, cs kubernetes.Interface, namespaces []string) (nativeAdminRotation, error) {
	plan := nativeAdminRotation{Replacement: map[string]string{}}
	journal, err := cs.CoreV1().Secrets("mcp-platform").Get(ctx, "mcp-registry-admin-rotation", metav1.GetOptions{})
	if err == nil {
		if journal.Labels[nativeManagedLabel] != "rotation" || json.Unmarshal(journal.Data["replacement.json"], &plan.Replacement) != nil || len(plan.Replacement) == 0 {
			return plan, fmt.Errorf("invalid registry rotation journal")
		}
	} else if !apierrors.IsNotFound(err) {
		return plan, err
	}
	type owner struct{ namespace, name string }
	owners := []owner{{"mcp-platform", "mcp-platform-api-credentials"}, {"mcp-platform", "mcp-runtime-api-credentials"}, {"mcp-observability", "mcp-analytics-api-credentials"}, {"mcp-platform", "mcp-ui-credentials"}}
	for _, o := range owners {
		secret, err := cs.CoreV1().Secrets(o.namespace).Get(ctx, o.name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) && o.namespace == "mcp-observability" {
			continue
		}
		if err != nil {
			return plan, fmt.Errorf("read registry credential owner %s/%s: %w", o.namespace, o.name, err)
		}
		plan.Secrets = append(plan.Secrets, secret)
	}
	adminKeys := map[string]bool{}
	for _, secret := range plan.Secrets {
		for _, key := range strings.Split(string(secret.Data["ADMIN_API_KEYS"]), ",") {
			if key = strings.TrimSpace(key); key != "" {
				adminKeys[key] = true
			}
		}
	}
	for _, namespace := range namespaces {
		secret, err := cs.CoreV1().Secrets(namespace).Get(ctx, nativePullSecret, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return plan, err
		}
		for _, password := range dockerCredentialPasswords(secret.Data[corev1.DockerConfigJsonKey]) {
			if adminKeys[password] && plan.Replacement[password] == "" {
				raw := make([]byte, 32)
				if _, err := rand.Read(raw); err != nil {
					return plan, err
				}
				plan.Replacement[password] = hex.EncodeToString(raw)
			}
		}
	}
	for _, secret := range plan.Secrets {
		for _, field := range []string{"API_KEYS", "ADMIN_API_KEYS", "UI_API_KEY"} {
			parts := strings.Split(string(secret.Data[field]), ",")
			for i, key := range parts {
				if replacement, ok := plan.Replacement[strings.TrimSpace(key)]; ok {
					parts[i] = replacement
				}
			}
			if _, exists := secret.Data[field]; exists {
				secret.Data[field] = []byte(strings.Join(parts, ","))
			}
		}
	}
	ownerAPI := plan.Secrets[0]
	allowed := map[string]bool{}
	for _, key := range strings.Split(string(ownerAPI.Data["API_KEYS"]), ",") {
		allowed[strings.TrimSpace(key)] = true
	}
	for _, key := range strings.Split(string(ownerAPI.Data["ADMIN_API_KEYS"]), ",") {
		key = strings.TrimSpace(key)
		if key != "" && allowed[key] {
			plan.PublisherKey = key
			break
		}
	}
	if plan.PublisherKey == "" {
		return plan, fmt.Errorf("no registry publisher service key shared by API_KEYS and ADMIN_API_KEYS")
	}
	return plan, nil
}

func dockerCredentialPasswords(data []byte) []string {
	var cfg struct {
		Auths map[string]struct {
			Password string `json:"password"`
			Auth     string `json:"auth"`
		} `json:"auths"`
	}
	if json.Unmarshal(data, &cfg) != nil {
		return nil
	}
	var values []string
	for _, auth := range cfg.Auths {
		if auth.Password == "" {
			if decoded, err := base64.StdEncoding.DecodeString(auth.Auth); err == nil {
				_, auth.Password, _ = strings.Cut(string(decoded), ":")
			}
		}
		if auth.Password != "" {
			values = append(values, auth.Password)
		}
	}
	return values
}

func applyRegistryAdminRotation(ctx context.Context, clients *k8sclient.Clients, plan nativeAdminRotation) error {
	if len(plan.Replacement) == 0 {
		return nil
	}
	encoded, err := json.Marshal(plan.Replacement)
	if err != nil {
		return err
	}
	journal := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcp-registry-admin-rotation", Namespace: "mcp-platform", Labels: map[string]string{nativeManagedLabel: "rotation"}}, Data: map[string][]byte{"replacement.json": encoded}}
	if _, err = clients.Clientset.CoreV1().Secrets(journal.Namespace).Create(ctx, journal, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	for _, secret := range plan.Secrets {
		current, err := clients.Clientset.CoreV1().Secrets(secret.Namespace).Get(ctx, secret.Name, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if current.ResourceVersion != secret.ResourceVersion {
			return fmt.Errorf("registry credential owner changed during activation; rerun to reconcile")
		}
		if _, err := clients.Clientset.CoreV1().Secrets(secret.Namespace).Update(ctx, secret, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	marker := time.Now().UTC().Format(time.RFC3339Nano)
	for namespace, names := range map[string][]string{"mcp-platform": {"mcp-platform-api", "mcp-runtime-api", "mcp-ui"}, "mcp-observability": {"mcp-analytics-api"}} {
		for _, name := range names {
			workload, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
			if apierrors.IsNotFound(err) {
				continue
			}
			if err != nil {
				return err
			}
			if workload.Spec.Template.Annotations == nil {
				workload.Spec.Template.Annotations = map[string]string{}
			}
			workload.Spec.Template.Annotations["mcpruntime.org/registry-admin-key-rotation"] = marker
			if _, err := clients.Clientset.AppsV1().Deployments(namespace).Update(ctx, workload, metav1.UpdateOptions{}); err != nil {
				return err
			}
			if err := k8sclient.WaitForDeploymentRolledOut(ctx, clients, namespace, name, 5*time.Minute); err != nil {
				return fmt.Errorf("registry admin key rotation rollout failed for %s/%s: %w", namespace, name, err)
			}
		}
	}
	return nil
}
