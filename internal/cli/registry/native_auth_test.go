package registry

import (
	"context"
	"encoding/base64"
	"encoding/json"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	"mcp-runtime/pkg/registryauth"
	"strings"
	"testing"
)

func TestAdminRotationRevokesCopiedKeyAndResumes(t *testing.T) {
	var owners []runtime.Object
	for _, name := range []string{"mcp-platform-api-credentials", "mcp-runtime-api-credentials", "mcp-ui-credentials"} {
		owners = append(owners, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "mcp-platform"}, Data: map[string][]byte{"API_KEYS": []byte("copied-admin,keep"), "ADMIN_API_KEYS": []byte("copied-admin"), "UI_API_KEY": []byte("copied-admin")}})
	}
	data, _ := json.Marshal(map[string]any{"auths": map[string]any{"registry.example": map[string]string{"auth": base64.StdEncoding.EncodeToString([]byte("platform-service:copied-admin"))}}})
	owners = append(owners, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nativePullSecret, Namespace: "mcp-team-acme"}, Data: map[string][]byte{corev1.DockerConfigJsonKey: data}})
	cs := fake.NewSimpleClientset(owners...)
	ctx := context.Background()
	plan, err := planRegistryAdminRotation(ctx, cs, []string{"mcp-team-acme"})
	if err != nil {
		t.Fatal(err)
	}
	replacement := plan.Replacement["copied-admin"]
	if replacement == "" || replacement == "copied-admin" || plan.PublisherKey != replacement {
		t.Fatal("admin key was not rotated")
	}
	for _, secret := range plan.Secrets {
		if strings.Contains(string(secret.Data["ADMIN_API_KEYS"]), "copied-admin") || string(secret.Data["API_KEYS"]) != replacement+",keep" {
			t.Fatal("owner keys not replaced consistently")
		}
	}
	encoded, _ := json.Marshal(plan.Replacement)
	_, err = cs.CoreV1().Secrets("mcp-platform").Create(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "mcp-registry-admin-rotation", Namespace: "mcp-platform", Labels: map[string]string{nativeManagedLabel: "rotation"}}, Data: map[string][]byte{"replacement.json": encoded}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = cs.CoreV1().Secrets("mcp-team-acme").Delete(ctx, nativePullSecret, metav1.DeleteOptions{})
	resumed, err := planRegistryAdminRotation(ctx, cs, []string{"mcp-team-acme"})
	if err != nil {
		t.Fatal(err)
	}
	if resumed.PublisherKey != replacement {
		t.Fatal("retry generated another replacement after node migration")
	}
}

func TestRegistrySigningMaterialPreservedAndMismatchRejected(t *testing.T) {
	ctx := context.Background()
	cs := fake.NewSimpleClientset()
	key, cert, err := ensureRegistrySigningMaterial(ctx, cs)
	if err != nil {
		t.Fatal(err)
	}
	secondKey, secondCert, err := ensureRegistrySigningMaterial(ctx, cs)
	if err != nil || string(key) != string(secondKey) || string(cert) != string(secondCert) {
		t.Fatal("signing material changed on retry")
	}
	root, err := cs.CoreV1().Secrets("registry").Get(ctx, nativeRootSecret, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(root.Data) != 1 || root.Data["signing.key"] != nil {
		t.Fatal("backend received private key")
	}
	_, otherCert, err := registryauth.GenerateMaterial()
	if err != nil {
		t.Fatal(err)
	}
	root.Data["signing.crt"] = otherCert
	_, err = cs.CoreV1().Secrets("registry").Update(ctx, root, metav1.UpdateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := ensureRegistrySigningMaterial(ctx, cs); err == nil {
		t.Fatal("trust mismatch silently rotated")
	}
}

func TestSignerMountedOnlyToBroker(t *testing.T) {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "platform-api"}, {Name: "sidecar"}}}
	if err := mountNativeSecret(&spec, nativeSignerSecret, "signer", "/signer", "platform-api"); err != nil {
		t.Fatal(err)
	}
	if len(spec.Containers[0].VolumeMounts) != 1 || len(spec.Containers[1].VolumeMounts) != 0 {
		t.Fatal("private signer exposed to sidecar")
	}
	spec.Volumes[0].Secret.SecretName = "other"
	if err := mountNativeSecret(&spec, nativeSignerSecret, "signer", "/signer", "platform-api"); err == nil {
		t.Fatal("conflicting volume adopted")
	}
}

func TestSetupPreservesNativeBackendAndClusterIP(t *testing.T) {
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: []corev1.EnvVar{{Name: "REGISTRY_AUTH", Value: "token"}, {Name: "REGISTRY_AUTH_TOKEN_REALM", Value: "https://api.example/token"}}, VolumeMounts: []corev1.VolumeMount{{Name: "registry-token-root", MountPath: "/registry-token-root", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "registry-token-root", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: nativeRootSecret}}}}}}}}
	cs := fake.NewSimpleClientset(dep)
	input := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
spec:
  template:
    spec:
      containers:
        - name: registry
          image: registry:2.8.3
---
apiVersion: v1
kind: Service
metadata:
  name: registry
spec:
  type: NodePort
  ports:
    - port: 5000
      nodePort: 30500
`
	output, err := preserveNativeRegistryManifest(context.Background(), cs, "registry", input)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"REGISTRY_AUTH", "registry-token-root", "ClusterIP"} {
		if !strings.Contains(output, value) {
			t.Fatalf("missing preserved %s", value)
		}
	}
	if strings.Contains(output, "nodePort") || strings.Contains(output, "NodePort") {
		t.Fatal("setup restored NodePort")
	}
}
