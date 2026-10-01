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

// Deployments, ServiceAccounts and Certificates can mount or issue into a
// Secret, so the cluster-bound role may only read them; mutation comes from
// namespace-local bindings of the managed-workloads role.
func TestOperatorClusterRoleCannotMutateSecretRoutes(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/role.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var role rbacv1.ClusterRole
	if err = yaml.Unmarshal(data, &role); err != nil {
		t.Fatal(err)
	}
	guarded := map[string]bool{"deployments": true, "serviceaccounts": true, "certificates": true}
	seen := map[string]bool{}
	for _, rule := range role.Rules {
		for _, resource := range rule.Resources {
			if !guarded[resource] {
				continue
			}
			seen[resource] = true
			for _, verb := range rule.Verbs {
				if verb != "get" && verb != "list" && verb != "watch" {
					t.Fatalf("cluster-bound role can %s %s", verb, resource)
				}
			}
		}
	}
	for resource := range guarded {
		if !seen[resource] {
			t.Fatalf("cluster role lost read access to %s needed by informers", resource)
		}
	}
	source, err := os.ReadFile("controller.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(source), "\n") {
		if !strings.Contains(line, "kubebuilder:rbac:") {
			continue
		}
		for resource := range guarded {
			if strings.Contains(line, "resources="+resource+",") {
				for _, verb := range []string{"create", "update", "patch", "delete"} {
					if strings.Contains(afterVerbs(line), verb) {
						t.Fatalf("generator would restore %s %s: %s", verb, resource, line)
					}
				}
			}
		}
	}
}

func afterVerbs(line string) string {
	_, verbs, _ := strings.Cut(line, "verbs=")
	return verbs
}

func TestManagedWorkloadsRoleIsNeverClusterBound(t *testing.T) {
	data, err := os.ReadFile("../../config/rbac/operator_workload_access.yaml")
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
				t.Fatalf("workload role touches Secrets: %+v", rule)
			}
		}
		for _, verb := range rule.Verbs {
			if verb == "*" {
				t.Fatalf("wildcard verb: %+v", rule)
			}
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
			t.Fatalf("managed workloads role cluster-bound in %s", file.Name())
		}
	}
}
