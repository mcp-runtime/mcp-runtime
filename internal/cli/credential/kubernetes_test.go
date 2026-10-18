package credential

import (
	"bytes"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	cb "mcp-runtime/pkg/credentialbroker"
)

func TestSupportedSecretAuthorizationIsExactAndPreservesForeignRBAC(t *testing.T) {
	source := cb.Source{Namespace: "team-one", Name: "source", Provider: "kubernetes", Resource: "upstream-token", Key: "token"}
	kube := fake.NewSimpleClientset()
	if err := authorizeSecret(t.Context(), kube, source, false); err != nil {
		t.Fatal(err)
	}
	role, err := kube.RbacV1().Roles(source.Namespace).Get(t.Context(), "mcp-credential-source", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 || len(role.Rules[0].Verbs) != 1 || role.Rules[0].Verbs[0] != "get" || len(role.Rules[0].ResourceNames) != 1 || role.Rules[0].ResourceNames[0] != source.Resource {
		t.Fatal("provider received broader permissions")
	}
	binding, err := kube.RbacV1().RoleBindings(source.Namespace).Get(t.Context(), role.Name, metav1.GetOptions{})
	if err != nil || len(binding.Subjects) != 1 || binding.Subjects[0].Namespace != "mcp-platform" || binding.Subjects[0].Name != "mcp-platform-api" {
		t.Fatal("provider permission assigned to wrong service")
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("permission command read a secret value")
		}
	}
	if err = authorizeSecret(t.Context(), kube, source, false); err != nil {
		t.Fatal("idempotent authorization failed")
	}
	if err = authorizeSecret(t.Context(), kube, source, true); err != nil {
		t.Fatal(err)
	}
	if roles, _ := kube.RbacV1().Roles(source.Namespace).List(t.Context(), metav1.ListOptions{}); len(roles.Items) != 0 {
		t.Fatal("provider read grant survived revoke")
	}
	role.Rules[0].ResourceNames = nil
	role.ResourceVersion = ""
	if _, err = kube.RbacV1().Roles(source.Namespace).Create(t.Context(), role, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	before := len(kube.Actions())
	if err = authorizeSecret(t.Context(), kube, source, false); err == nil {
		t.Fatal("wider existing Role adopted")
	}
	for _, action := range kube.Actions()[before:] {
		if action.GetVerb() != "get" {
			t.Fatal("foreign Role modified")
		}
	}
}

func configuredObjects() []runtime.Object {
	exact, prefix := networkingv1.PathTypeExact, networkingv1.PathTypePrefix
	backend := networkingv1.IngressBackend{Service: &networkingv1.IngressServiceBackend{Name: "mcp-platform-api", Port: networkingv1.ServiceBackendPort{Number: 8080}}}
	return []runtime.Object{
		&networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-gateway-api", Namespace: "mcp-platform"}, Spec: networkingv1.IngressSpec{Rules: []networkingv1.IngressRule{{IngressRuleValue: networkingv1.IngressRuleValue{HTTP: &networkingv1.HTTPIngressRuleValue{Paths: []networkingv1.HTTPIngressPath{{Path: cb.ResolvePath, PathType: &exact, Backend: backend}, {Path: cb.ManagementPath, PathType: &prefix, Backend: backend}}}}}}}},
		&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api-gateway-identity"}, Rules: []rbacv1.PolicyRule{{APIGroups: []string{"mcpruntime.org"}, Resources: []string{"mcpservers", "mcpaccessgrants"}, Verbs: []string{"get"}}}},
		&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-operator-controller-manager", Namespace: "mcp-runtime"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "manager", Env: []corev1.EnvVar{{Name: "PRESERVE", Value: "yes"}}}}}}}},
	}
}

func TestBrokerEnablementPreflightNeverMutatesAnUnmigratedInstall(t *testing.T) {
	for _, change := range []func([]runtime.Object){func(objects []runtime.Object) {
		objects[0].(*networkingv1.Ingress).Spec.Rules[0].HTTP.Paths = objects[0].(*networkingv1.Ingress).Spec.Rules[0].HTTP.Paths[:1]
	}, func(objects []runtime.Object) {
		objects[0].(*networkingv1.Ingress).Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "mcp-runtime-api"
	}, func(objects []runtime.Object) {
		objects[1].(*rbacv1.ClusterRole).Rules[0].Resources = []string{"mcpservers"}
	}} {
		objects := configuredObjects()
		change(objects)
		kube := fake.NewSimpleClientset(objects...)
		if err := configureOperator(t.Context(), kube, "https://platform.example.test"); err == nil || !strings.Contains(err.Error(), "refresh setup") {
			t.Fatal("old install enabled delivery")
		}
		for _, action := range kube.Actions() {
			if action.GetVerb() != "get" {
				t.Fatal("failed preflight modified cluster")
			}
		}
	}
	kube := fake.NewSimpleClientset(configuredObjects()...)
	if err := configureOperator(t.Context(), kube, "https://platform.example.test"); err != nil {
		t.Fatal(err)
	}
	deployment, err := kube.AppsV1().Deployments("mcp-runtime").Get(t.Context(), "mcp-runtime-operator-controller-manager", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	env := deployment.Spec.Template.Spec.Containers[0].Env
	if len(env) != 2 || env[0].Name != "PRESERVE" || env[1].Name != "MCP_GATEWAY_CREDENTIAL_BROKER_URL" {
		t.Fatal("configuration changed unrelated operator settings")
	}
}

func TestMetadataCLIRejectsRawCredentialsBeforeAuthentication(t *testing.T) {
	cmd := New()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetIn(strings.NewReader(`{"value":"sensitive-cli-marker"}`))
	cmd.SetArgs([]string{"source", "apply", "--file", "-"})
	if err := cmd.Execute(); err == nil || strings.Contains(err.Error(), "sensitive-cli-marker") || strings.Contains(output.String(), "sensitive-cli-marker") {
		t.Fatal("CLI accepted/printed raw credentials")
	}
	cmd = New()
	cmd.SetArgs([]string{"configure", "--broker-url", "http://platform.test", "--yes"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("CLI accepted plaintext delivery")
	}
}

func TestSupportedCloudAndVaultConfigurationRetainsOnlyWorkloadIdentity(t *testing.T) {
	kube := fake.NewSimpleClientset(&corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api", Namespace: "mcp-platform", Annotations: map[string]string{"preserve": "yes"}}}, &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "mcp-platform-api", Namespace: "mcp-platform"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "platform-api", Env: []corev1.EnvVar{{Name: "PRESERVE", Value: "yes"}}}}}}}})
	if err := configureWorkloadIdentity(t.Context(), kube, "fixture@fixture-project.iam.gserviceaccount.com", "arn:aws:iam::123456789012:role/broker", "11111111-1111-1111-1111-111111111111"); err != nil {
		t.Fatal(err)
	}
	sa, err := kube.CoreV1().ServiceAccounts("mcp-platform").Get(t.Context(), "mcp-platform-api", metav1.GetOptions{})
	if err != nil || sa.Annotations["preserve"] != "yes" || sa.Annotations["iam.gke.io/gcp-service-account"] == "" || sa.Annotations["eks.amazonaws.com/role-arn"] == "" {
		t.Fatal("workload identity annotations missing or unrelated setting changed")
	}
	if err = configureVault(t.Context(), kube, "https://vault.example.test", "broker"); err != nil {
		t.Fatal(err)
	}
	deployment, err := kube.AppsV1().Deployments("mcp-platform").Get(t.Context(), "mcp-platform-api", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if deployment.Spec.Template.Labels["azure.workload.identity/use"] != "true" || len(deployment.Spec.Template.Spec.Volumes) != 1 {
		t.Fatal("provider workload wiring missing")
	}
	projection := deployment.Spec.Template.Spec.Volumes[0].Projected
	if projection == nil || len(projection.Sources) != 1 || projection.Sources[0].ServiceAccountToken.Audience != "mcp-runtime-vault" || *projection.Sources[0].ServiceAccountToken.ExpirationSeconds != 600 {
		t.Fatal("Vault token audience/lifetime is too broad")
	}
	for _, action := range kube.Actions() {
		if action.GetResource().Resource == "secrets" {
			t.Fatal("cloud configuration handled raw credentials")
		}
	}
}
