package platform

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestDependencyRevisionTargetsExactConsumers(t *testing.T) {
	data := map[string][]byte{"JWT_SECRET": []byte("jwt"), "GRAFANA_ADMIN_PASSWORD": []byte("grafana")}
	read := func(ns, kind, name string) (map[string][]byte, error) { return data, nil }
	api := []dependencyReference{{Kind: "Secret", Name: "credentials", Key: "JWT_SECRET"}}
	grafana := []dependencyReference{{Kind: "Secret", Name: "credentials", Key: "GRAFANA_ADMIN_PASSWORD"}}
	a, _ := dependencyRevision("namespace", api, read)
	g, _ := dependencyRevision("namespace", grafana, read)
	data["GRAFANA_ADMIN_PASSWORD"] = []byte("rotated")
	a2, _ := dependencyRevision("namespace", api, read)
	g2, _ := dependencyRevision("namespace", grafana, read)
	if a != a2 || g == g2 {
		t.Fatal("telemetry rotation must leave API revision unchanged")
	}
	a3, _ := dependencyRevision("namespace", api, read)
	if a3 != a2 {
		t.Fatal("unchanged setup changes revision")
	}
	other, _ := dependencyRevision("other-namespace", api, read)
	if other == a {
		t.Fatal("namespace identity omitted")
	}
}

func TestDependencyErrorsFailClosed(t *testing.T) {
	optional := []dependencyReference{{Kind: "Secret", Name: "optional", Key: "key", Optional: true}}
	notfound := func(ns, kind, name string) (map[string][]byte, error) {
		return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "secrets"}, name)
	}
	if _, err := dependencyRevision("namespace", optional, notfound); err != nil {
		t.Fatal(err)
	}
	forbidden := func(ns, kind, name string) (map[string][]byte, error) {
		return nil, apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, name, nil)
	}
	if _, err := dependencyRevision("namespace", optional, forbidden); err == nil {
		t.Fatal("authorization failure ignored")
	}
	optional[0].Optional = false
	if _, err := dependencyRevision("namespace", optional, notfound); err == nil {
		t.Fatal("missing required Secret accepted")
	}
	if _, err := dependencyRevision("namespace", optional, func(ns, kind, name string) (map[string][]byte, error) { return map[string][]byte{}, nil }); err == nil {
		t.Fatal("missing required key accepted")
	}
}

func TestDependencyReferencesCoverInitEnvAndVolumes(t *testing.T) {
	pod := corev1.PodSpec{InitContainers: []corev1.Container{{Env: []corev1.EnvVar{{ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "init"}, Key: "key"}}}}}}, Containers: []corev1.Container{{EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "config"}}}}}}, Volumes: []corev1.Volume{{VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "files"}}}}}
	if refs := podDependencyReferences(pod); len(refs) != 3 {
		t.Fatalf("missed dependency: %+v", refs)
	}
}

func TestManifestLocalDependenciesAndStableRollout(t *testing.T) {
	source := `apiVersion: v1
kind: ConfigMap
metadata:
  name: settings
data:
  MODE: enabled
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: api
spec:
  template:
    spec:
      containers:
        - name: api
          env:
            - name: MODE
              valueFrom:
                configMapKeyRef:
                  name: settings
                  key: MODE
`
	read := func(ns, kind, name string) (map[string][]byte, error) {
		t.Fatal("same-manifest dependency incorrectly read before apply")
		return nil, nil
	}
	first, err := stampDependencyRevisions(source, read)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, dependencyRevisionAnnotation) {
		t.Fatal("revision missing")
	}
	second, err := stampDependencyRevisions(first, read)
	if err != nil || first != second {
		t.Fatalf("rerun is not stable: %v", err)
	}
	changed, err := stampDependencyRevisions(strings.Replace(source, "MODE: enabled", "MODE: disabled", 1), read)
	if err != nil || changed == first {
		t.Fatal("config update did not change template")
	}
}
