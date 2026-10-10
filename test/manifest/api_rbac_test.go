package manifest_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

type rbacDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Rules []struct {
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	} `yaml:"rules"`
	Subjects []struct {
		Kind      string `yaml:"kind"`
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"subjects"`
	RoleRef struct {
		Kind string `yaml:"kind"`
		Name string `yaml:"name"`
	} `yaml:"roleRef"`
}

func TestRegistryPushRBACIsNamespaceScoped(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", "08-runtime-api-rbac.yaml"))
	if err != nil {
		t.Fatalf("read RBAC manifest: %v", err)
	}

	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	var foundClusterPodsExec bool
	var foundPushRole bool
	var foundPushRoleBinding bool
	var foundRegistryPodsExec bool
	var foundNativeRole, foundNativeBinding bool
	for {
		var doc rbacDoc
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			t.Fatalf("decode RBAC manifest: %v", err)
		}
		switch {
		case doc.Kind == "ClusterRole" && doc.Metadata.Name == "mcp-runtime-api":
			for _, rule := range doc.Rules {
				if containsAll(rule.Resources, "pods") && containsAny(rule.Verbs, "create", "delete", "watch") {
					t.Fatalf("cluster role mcp-runtime-api must not grant mutating pod access cluster-wide")
				}
				if containsAll(rule.Resources, "pods/exec") {
					foundClusterPodsExec = true
				}
			}
		case doc.Kind == "Role" && doc.Metadata.Name == "mcp-runtime-api-registry-push" && doc.Metadata.Namespace == "mcp-platform":
			foundPushRole = true
			for _, rule := range doc.Rules {
				if containsAll(rule.Resources, "pods/exec") {
					t.Fatalf("registry push role in mcp-platform must not grant pods/exec")
				}
			}
		case doc.Kind == "RoleBinding" && doc.Metadata.Name == "mcp-runtime-api-registry-push" && doc.Metadata.Namespace == "mcp-platform":
			if doc.RoleRef.Kind == "Role" && doc.RoleRef.Name == "mcp-runtime-api-registry-push" {
				for _, subject := range doc.Subjects {
					if subject.Kind == "ServiceAccount" && subject.Name == "mcp-runtime-api" && subject.Namespace == "mcp-platform" {
						foundPushRoleBinding = true
					}
				}
			}
		case doc.Kind == "Role" && doc.Metadata.Name == "mcp-runtime-api-registry-push" && doc.Metadata.Namespace == "registry":
			foundNativeRole = true
			for _, rule := range doc.Rules {
				if containsAll(rule.Resources, "pods/exec") {
					foundRegistryPodsExec = true
				}
				if containsAny(rule.Resources, "secrets", "*") {
					t.Fatal("native helper role must not expose registry Secrets")
				}
			}
		case doc.Kind == "RoleBinding" && doc.Metadata.Name == "mcp-runtime-api-registry-push" && doc.Metadata.Namespace == "registry":
			if doc.RoleRef.Kind == "Role" && doc.RoleRef.Name == "mcp-runtime-api-registry-push" {
				for _, subject := range doc.Subjects {
					if subject.Kind == "ServiceAccount" && subject.Name == "mcp-runtime-api" && subject.Namespace == "mcp-platform" {
						foundNativeBinding = true
					}
				}
			}
		}
	}
	if !foundNativeRole || !foundNativeBinding {
		t.Fatal("native publishing requires a registry-scoped helper Role and binding")
	}
	if foundClusterPodsExec {
		t.Fatal("cluster role mcp-runtime-api must not grant pods/exec cluster-wide")
	}
	if foundRegistryPodsExec {
		t.Fatal("registry namespace must not grant pods/exec for registry push")
	}
	if !foundPushRole {
		t.Fatal("expected Role for API registry push helper access")
	}
	if !foundPushRoleBinding {
		t.Fatal("expected RoleBinding for API registry push helper access")
	}
}

func containsAll(values []string, want ...string) bool {
	for _, candidate := range want {
		found := false
		for _, value := range values {
			if value == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func containsAny(values []string, want ...string) bool {
	for _, candidate := range want {
		for _, value := range values {
			if value == candidate {
				return true
			}
		}
	}
	return false
}
