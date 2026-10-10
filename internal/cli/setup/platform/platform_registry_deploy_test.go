package platform

import (
	"context"
	"fmt"
	"go.uber.org/zap"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	clienttesting "k8s.io/client-go/testing"
	"strings"
	"testing"
)

func TestValidateRegistryTypeRejectsUnsupportedValues(t *testing.T) {
	if err := validateRegistryType("docker"); err != nil {
		t.Fatalf("validateRegistryType(docker) error = %v", err)
	}
	if err := validateRegistryType("harbor"); err == nil {
		t.Fatal("validateRegistryType(harbor) error = nil, want unsupported registry type")
	}
}

func TestMutateRegistryManifestUsesStructuredRegistryUpdates(t *testing.T) {
	manifest := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: registry
  namespace: registry
spec:
  template:
    spec:
      containers:
      - name: registry
        # image: registry:2.8.3 in a comment should not be rewritten
        image: registry:2.8.3
---
apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: registry
  namespace: registry
  annotations:
    cert-manager.io/cluster-issuer: letsencrypt
spec:
  rules:
  - host: registry.local
`

	rendered, err := mutateRegistryManifest(manifest, "registry.example.com", "registry.example.com/registry:2.8.3", "", false)
	if err != nil {
		t.Fatalf("mutateRegistryManifest() error = %v", err)
	}
	if strings.Contains(rendered, "cert-manager.io/cluster-issuer") {
		t.Fatalf("expected cluster issuer annotation to be removed, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "registry.example.com") {
		t.Fatalf("expected registry host rewrite, got:\n%s", rendered)
	}
	if !strings.Contains(rendered, "image: registry.example.com/registry:2.8.3") {
		t.Fatalf("expected registry image override, got:\n%s", rendered)
	}
	if strings.Count(rendered, "registry.example.com/registry:2.8.3") != 1 {
		t.Fatalf("expected only the container image to be rewritten, got:\n%s", rendered)
	}
}

func TestRegistryIngressAuthenticationMatchesBackend(t *testing.T) {
	manifest := `apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: registry
  annotations:
    traefik.ingress.kubernetes.io/router.middlewares: registry-admin-auth@file,rate-limit@file
`
	for _, native := range []bool{false, true} {
		rendered, err := mutateRegistryManifest(manifest, "registry.example.com", "", "", native)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rendered, registryAdminAuthMiddleware) == native {
			t.Fatalf("native=%v: unexpected public authentication: %s", native, rendered)
		}
		if !strings.Contains(rendered, "rate-limit@file") {
			t.Fatal("lost unrelated middleware")
		}
	}
}

func TestRegistryBackendAuthReadFailsClosed(t *testing.T) {
	for _, test := range []struct {
		name, auth                   string
		missing, readFailure, source bool
		want                         bool
	}{
		{name: "fresh registry", missing: true},
		{name: "unauthenticated backend"},
		{name: "native authentication", auth: "token", want: true},
		{name: "read failure", readFailure: true},
		{name: "unresolved authentication source", source: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var objects []runtime.Object
			if !test.missing {
				env := corev1.EnvVar{Name: "REGISTRY_AUTH", Value: test.auth}
				if test.source {
					env.ValueFrom = &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "auth"}, Key: "mode"}}
				}
				objects = append(objects, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "registry", Namespace: "registry"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "registry", Env: []corev1.EnvVar{env}}}}}}})
			}
			clients := newPlatformKubernetesTestClients(objects, nil)
			if test.readFailure {
				clients.Clientset.(*fake.Clientset).PrependReactor("get", "deployments", func(clienttesting.Action) (bool, runtime.Object, error) {
					return true, nil, fmt.Errorf("API unavailable")
				})
			}
			if test.readFailure || test.source {
				swapKubernetesClientsForTest(t, clients)
				if err := deployRegistryClientGo(zap.NewNop(), "registry", 5000, "docker", "20Gi", "unused"); err == nil {
					t.Fatal("setup must fail before applying resources when authentication cannot be inspected")
				}
			}
			got, err := registryBackendUsesTokenAuth(context.Background(), clients, "registry")
			if (err != nil) != (test.readFailure || test.source) || got != test.want {
				t.Fatalf("got (%v,%v), want auth=%v", got, err, test.want)
			}
			for _, action := range clients.Clientset.(*fake.Clientset).Actions() {
				if action.GetVerb() != "get" {
					t.Fatalf("authentication inspection mutated the cluster: %v", action)
				}
			}
		})
	}
}
