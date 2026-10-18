package platform

import (
	"context"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"
)

func TestSetupRefreshPreservesConfiguredBrokerAndCanonicalVaultIdentity(t *testing.T) {
	origin := "https://platform.example.test"
	get := func(name string) string {
		if name == credentialBrokerOriginEnv {
			return origin
		}
		return ""
	}
	t.Chdir("../../../..")
	rendered, err := renderOperatorManagerManifest("fixture/operator:new", "fixture/gateway:new", nil, get, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rendered), credentialBrokerOriginEnv) || !strings.Contains(string(rendered), origin) {
		t.Fatal("setup discarded configured broker origin")
	}
	if _, err = preservedCredentialBrokerOrigin(func(string) string { return "http://platform.internal" }); err == nil {
		t.Fatal("unsafe configured broker silently retained/dropped")
	}
	expiry := int64(600)
	live := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api", Namespace: "mcp-platform"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{"azure.workload.identity/use": "true"}, Annotations: map[string]string{"mcpruntime.org/credential-workload-identity": "fixture-public-revision"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "platform-api", Image: "old-image", Env: []corev1.EnvVar{{Name: "CREDENTIAL_VAULT_ADDR", Value: "https://vault.example.test"}, {Name: "CREDENTIAL_VAULT_ROLE", Value: "broker"}, {Name: "CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE", Value: "/var/run/mcp-runtime/vault/token"}, {Name: "UNRELATED", Value: "drop"}}, VolumeMounts: []corev1.VolumeMount{{Name: "credential-vault-identity", MountPath: "/var/run/mcp-runtime/vault", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "credential-vault-identity", VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "mcp-runtime-vault", ExpirationSeconds: &expiry, Path: "token"}}}}}}, {Name: "unrelated-volume", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "drop"}}}}}}}}
	input, err := os.ReadFile("k8s/08-platform-api.yaml")
	if err != nil {
		t.Fatal(err)
	}
	output, err := preserveNativePlatformManifest(context.Background(), fake.NewSimpleClientset(live), string(input))
	if err != nil {
		t.Fatal(err)
	}
	var desired appsv1.Deployment
	for _, doc := range strings.Split(output, "---\n") {
		var candidate appsv1.Deployment
		if yaml.Unmarshal([]byte(doc), &candidate) == nil && candidate.Kind == "Deployment" && candidate.Name == live.Name {
			desired = candidate
		}
	}
	if desired.Spec.Template.Labels["azure.workload.identity/use"] != "true" || desired.Spec.Template.Annotations["mcpruntime.org/credential-workload-identity"] != "fixture-public-revision" {
		t.Fatal("cloud identity pod wiring lost")
	}
	vaultVolumes, tmpVolumes := 0, 0
	for _, volume := range desired.Spec.Template.Spec.Volumes {
		switch volume.Name {
		case "credential-vault-identity":
			vaultVolumes++
		case "registry-push-tmp":
			tmpVolumes++
		case "unrelated-volume":
			t.Fatal("unrelated old volume restored")
		}
	}
	if vaultVolumes != 1 || tmpVolumes != 1 {
		t.Fatal("Vault identity or shipped temporary volume lost")
	}
	for _, container := range desired.Spec.Template.Spec.Containers {
		if container.Name != "platform-api" {
			continue
		}
		if container.Image == "old-image" {
			t.Fatal("preservation undid target image")
		}
		found := 0
		for _, env := range container.Env {
			if strings.HasPrefix(env.Name, "CREDENTIAL_VAULT_") {
				found++
			}
			if env.Name == "UNRELATED" {
				t.Fatal("stale environment copied")
			}
		}
		if found != 3 {
			t.Fatal("Vault settings lost")
		}
		vaultMounts, tmpMounts := 0, 0
		for _, mount := range container.VolumeMounts {
			if mount.Name == "credential-vault-identity" && mount.ReadOnly {
				vaultMounts++
			}
			if mount.Name == "registry-push-tmp" {
				tmpMounts++
			}
		}
		if vaultMounts != 1 || tmpMounts != 1 {
			t.Fatal("Vault or shipped temporary mount lost")
		}
	}
	live.Spec.Template.Spec.Volumes[0].Projected.Sources = append(live.Spec.Template.Spec.Volumes[0].Projected.Sources, corev1.VolumeProjection{Secret: &corev1.SecretProjection{LocalObjectReference: corev1.LocalObjectReference{Name: "foreign"}}})
	if err = preserveCredentialBrokerDeployment(&desired, live); err == nil {
		t.Fatal("modified credential volume accepted")
	}
}
