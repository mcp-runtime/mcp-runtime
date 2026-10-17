package workloadidentity

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestDeploymentConfigUsesSelectedContainerAndKubernetesPrecedence(t *testing.T) {
	optional := true
	for _, tc := range []struct {
		name      string
		env       []corev1.EnvVar
		from      []corev1.EnvFromSource
		want      string
		wantError bool
	}{
		{name: "literal empty overrides envFrom", env: []corev1.EnvVar{{Name: "MCP_FLAG", Value: ""}}, from: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "first"}}}}},
		{name: "explicit ConfigMap", env: []corev1.EnvVar{{Name: "MCP_FLAG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "first"}, Key: "MCP_FLAG"}}}}, want: "true"},
		{name: "last prefixed envFrom wins", from: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "first"}}}, {Prefix: "MCP_", ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "second"}}}}, want: "false"},
		{name: "optional missing key keeps envFrom", env: []corev1.EnvVar{{Name: "MCP_FLAG", ValueFrom: &corev1.EnvVarSource{ConfigMapKeyRef: &corev1.ConfigMapKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "missing"}, Key: "MCP_FLAG", Optional: &optional}}}}, from: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "first"}}}}, want: "true"},
		{name: "required envFrom unavailable", from: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "missing"}}}}, wantError: true},
		{name: "Secret reference unreadable", env: []corev1.EnvVar{{Name: "MCP_FLAG", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "private"}, Key: "flag"}}}}, wantError: true},
		{name: "Secret envFrom unreadable", from: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "private"}}}}, wantError: true},
		{name: "explicit env overrides Secret envFrom", env: []corev1.EnvVar{{Name: "MCP_FLAG", Value: "true"}}, from: []corev1.EnvFromSource{{SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "private"}}}}, want: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "operator"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "sidecar", Env: []corev1.EnvVar{{Name: "MCP_FLAG", Value: "sidecar"}}}, {Name: "manager", Env: tc.env, EnvFrom: tc.from}}}}}}
			values, err := DeploymentConfig(context.Background(), deployment, "manager", []string{"MCP_FLAG"}, func(_ context.Context, namespace, name string) (*corev1.ConfigMap, error) {
				if namespace != "operator" {
					t.Fatalf("unexpected namespace %s", namespace)
				}
				switch name {
				case "first":
					return &corev1.ConfigMap{Data: map[string]string{"MCP_FLAG": "true"}}, nil
				case "second":
					return &corev1.ConfigMap{Data: map[string]string{"FLAG": "false"}}, nil
				}
				return nil, apierrors.NewNotFound(schema.GroupResource{Resource: "configmaps"}, name)
			})
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v", err)
			}
			if err == nil && values["MCP_FLAG"] != tc.want {
				t.Fatalf("value=%q want %q", values["MCP_FLAG"], tc.want)
			}
		})
	}
}
