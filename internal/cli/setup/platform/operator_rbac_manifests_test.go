package platform

import (
	"os"
	"strings"
	"testing"
)

// Setup applies operator RBAC from an explicit list rather than the kustomization.
// A resource in the kustomization that setup skips leaves RoleBindings pointing at
// a ClusterRole that does not exist, so the operator silently loses permissions.
func TestOperatorRBACManifestsCoverKustomization(t *testing.T) {
	content, err := os.ReadFile("../../../../config/rbac/kustomization.yaml")
	if err != nil {
		t.Fatal(err)
	}
	applied := map[string]bool{"role.yaml": true} // reapplied separately after migration
	for _, path := range operatorRBACManifests {
		applied[strings.TrimPrefix(path, "config/rbac/")] = true
	}
	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		resource := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if !applied[resource] {
			t.Errorf("config/rbac/%s is in the kustomization but not applied by setup", resource)
		}
	}
}
