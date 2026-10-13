package regression

import (
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"gopkg.in/yaml.v3"
)

type discoveryManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Namespace string `yaml:"namespace"`
	} `yaml:"metadata"`
	Rules []struct {
		APIGroups []string `yaml:"apiGroups"`
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	} `yaml:"rules"`
	Spec struct {
		Group string `yaml:"group"`
		Names struct {
			Plural string `yaml:"plural"`
		} `yaml:"names"`
	} `yaml:"spec"`
}

func TestManagedTraefikDiscoveryRBACAndCRDs(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{"config/ingress/base/traefik.yaml", "k8s/10-gateway.yaml"} {
		t.Run(path, func(t *testing.T) {
			file, err := os.Open(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			decoder := yaml.NewDecoder(file)
			roleCount := 0
			nodes := false
			for {
				var doc discoveryManifest
				err := decoder.Decode(&doc)
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				if doc.Kind != "Role" && doc.Kind != "ClusterRole" {
					continue
				}
				required := map[string]bool{"endpointslices": false, "configmaps": false, "pods": false}
				for _, rule := range doc.Rules {
					if slices.Contains(rule.Resources, "nodes") && slices.Contains(rule.Verbs, "list") && slices.Contains(rule.Verbs, "watch") {
						nodes = true
					}
					for resource := range required {
						if !slices.Contains(rule.Resources, resource) {
							continue
						}
						if resource == "endpointslices" && !slices.Contains(rule.APIGroups, "discovery.k8s.io") {
							t.Fatal("EndpointSlice permission has wrong API group")
						}
						for _, verb := range rule.Verbs {
							if verb != "get" && verb != "list" && verb != "watch" {
								t.Fatalf("discovery permission grants mutation: %v", rule)
							}
						}
						required[resource] = slices.Contains(rule.Verbs, "get")
						if resource != "pods" {
							required[resource] = required[resource] && slices.Contains(rule.Verbs, "list") && slices.Contains(rule.Verbs, "watch")
						}
					}
				}
				if doc.Kind == "Role" {
					roleCount++
					for resource, ok := range required {
						if !ok {
							t.Errorf("%s/%s lacks read-only %s discovery", doc.Metadata.Namespace, doc.Metadata.Name, resource)
						}
					}
				}
			}
			if roleCount == 0 || !nodes {
				t.Fatalf("missing discovery roles or cluster-scoped Node read permission")
			}
		})
	}
	file, err := os.Open(filepath.Join(root, "config/ingress/base/traefik-crds.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	foundTCP := false
	for {
		var doc discoveryManifest
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if doc.Spec.Group == "traefik.io" && doc.Spec.Names.Plural == "serverstransporttcps" {
			foundTCP = true
		}
	}
	if !foundTCP {
		t.Fatal("managed v3 CRD provider requires ServersTransportTCP")
	}
}

func TestTraefikDiscoveryDelegationIsNamespaceScoped(t *testing.T) {
	file, err := os.Open(filepath.Join(repoRoot(t), "k8s/08-runtime-api-rbac.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoder := yaml.NewDecoder(file)
	required := map[string]bool{"configmaps": false, "endpointslices": false}
	for {
		var doc struct {
			discoveryManifest `yaml:",inline"`
			RoleRef           struct {
				Name string `yaml:"name"`
			} `yaml:"roleRef"`
		}
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if doc.Kind == "ClusterRoleBinding" && doc.RoleRef.Name == "mcp-runtime-api-team-secrets" {
			t.Fatal("managed-namespace discovery delegate was bound cluster-wide")
		}
		if doc.Kind != "ClusterRole" || doc.Metadata.Name != "mcp-runtime-api-team-secrets" {
			continue
		}
		for _, rule := range doc.Rules {
			for resource := range required {
				if !slices.Contains(rule.Resources, resource) {
					continue
				}
				for _, verb := range rule.Verbs {
					if verb != "get" && verb != "list" && verb != "watch" {
						t.Fatalf("discovery delegate grants mutation: %v", rule)
					}
				}
				required[resource] = slices.Contains(rule.Verbs, "get") && slices.Contains(rule.Verbs, "list") && slices.Contains(rule.Verbs, "watch")
			}
		}
	}
	for resource, found := range required {
		if !found {
			t.Errorf("missing namespace-delegated %s read permission", resource)
		}
	}
}
