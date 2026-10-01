package operator

import (
	"os"
	"strings"
	"testing"

	rbacv1 "k8s.io/api/rbac/v1"
	"sigs.k8s.io/yaml"
)

func TestOperatorClusterRoleCannotAccessSecrets(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err = yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if resource == "secrets" || resource == "*" {
				t.Fatalf("cluster-bound role can access Secrets: %+v", rule)
			}
		}
	}
	source, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		if strings.Contains(line, "kubebuilder:rbac:") && strings.Contains(line, "resources=secrets") {
			t.Fatalf("generator would restore Secret access: %s", line)
		}
	}
}

func TestManagedSecretsRoleIsNeverClusterBound(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/operator_secret_access.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err = yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	if len(role.Rules) != 1 {
		t.Fatalf("unexpected permissions: %+v", role.Rules)
	}
	rule := role.Rules[0]
	if len(rule.Resources) != 1 || rule.Resources[0] != "secrets" || len(rule.APIGroups) != 1 || rule.APIGroups[0] != "" {
		t.Fatalf("unexpected role: %+v", rule)
	}
	for _, verb := range rule.Verbs {
		if verb == "list" || verb == "watch" || verb == "*" {
			t.Fatalf("role enables a Secret cache: %+v", rule)
		}
	}
	files, err := os.ReadDir("../../config/rbac")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".yaml") {
			continue
		}
		data, err = os.ReadFile("../../config/rbac/" + file.Name())
		if err != nil {
			t.Fatal(err)
		}
		var binding rbacv1.ClusterRoleBinding
		if err = yaml.Unmarshal(data, &binding); err != nil {
			t.Fatal(err)
		}
		if binding.Kind == "ClusterRoleBinding" && binding.RoleRef.Name == role.Name {
			t.Fatalf("managed Secret role cluster-bound in %s", file.Name())
		}
	}
}
