package manifest_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The operator lists Pods through the manager's cached client
// (internal/operator/deployment_diagnostics.go). A cached client serves a List
// from an informer, which needs both list and watch. With list only, the Pod
// informer fails with "pods is forbidden ... cannot watch resource" and never
// syncs, so MCPServer reconciles stall and servers report ready=0/0.
func TestOperatorClusterRoleCanListAndWatchPods(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "rbac", "role.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var role struct {
		Rules []struct {
			APIGroups []string `yaml:"apiGroups"`
			Resources []string `yaml:"resources"`
			Verbs     []string `yaml:"verbs"`
		} `yaml:"rules"`
	}
	if err := yaml.Unmarshal(raw, &role); err != nil {
		t.Fatalf("parse operator ClusterRole: %v", err)
	}
	verbs := map[string]bool{}
	for _, rule := range role.Rules {
		coreGroup := false
		for _, group := range rule.APIGroups {
			coreGroup = coreGroup || group == ""
		}
		for _, resource := range rule.Resources {
			if coreGroup && resource == "pods" {
				for _, verb := range rule.Verbs {
					verbs[verb] = true
				}
			}
		}
	}
	for _, want := range []string{"list", "watch"} {
		if !verbs[want] {
			t.Errorf("operator ClusterRole must allow %q on pods (cached client informer); has %v", want, verbs)
		}
	}
}

// config/rbac/role.yaml is generated from the kubebuilder markers, so the marker
// must carry the same verbs or the next `make manifests` silently drops them.
func TestOperatorPodsRBACMarkerMatchesClusterRole(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "internal", "operator", "controller.go"))
	if err != nil {
		t.Fatal(err)
	}
	marker := regexp.MustCompile(`(?m)^//\+kubebuilder:rbac:groups="",resources=pods,verbs=([a-z;]+)$`).FindStringSubmatch(string(raw))
	if marker == nil {
		t.Fatal("pods kubebuilder:rbac marker not found in internal/operator/controller.go")
	}
	have := strings.Split(marker[1], ";")
	for _, want := range []string{"list", "watch"} {
		found := false
		for _, verb := range have {
			found = found || verb == want
		}
		if !found {
			t.Errorf("pods kubebuilder:rbac marker must include %q; has %v", want, have)
		}
	}
}
