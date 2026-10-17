package manifest_test

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"mcp-runtime/internal/cli/setup/ingressmanifest"
)

type exchangeManifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Rules []struct {
			HTTP struct {
				Paths []struct {
					Path    string `yaml:"path"`
					Backend struct {
						Service struct {
							Name string `yaml:"name"`
						} `yaml:"service"`
					} `yaml:"backend"`
				} `yaml:"paths"`
			} `yaml:"http"`
		} `yaml:"rules"`
	} `yaml:"spec"`
}

func decodeExchangeManifests(t *testing.T, raw string) []exchangeManifest {
	t.Helper()
	decoder := yaml.NewDecoder(strings.NewReader(raw))
	var out []exchangeManifest
	for {
		var doc exchangeManifest
		err := decoder.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, doc)
	}
	return out
}

func TestAudienceExchangeDynamicConfiguration(t *testing.T) {
	for _, path := range []string{"k8s/10-gateway.yaml", "config/ingress/base/dynamic-config.yaml", "config/ingress/overlays/http/dynamic-config.yaml"} {
		t.Run(path, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("..", "..", path))
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, doc := range decodeExchangeManifests(t, string(raw)) {
				if doc.Kind != "ConfigMap" || doc.Metadata.Name != "traefik-dynamic" {
					continue
				}
				found = true
				var dynamic struct {
					HTTP struct {
						Middlewares map[string]struct {
							ForwardAuth struct {
								Address  string   `yaml:"address"`
								Trust    bool     `yaml:"trustForwardHeader"`
								Body     bool     `yaml:"forwardBody"`
								Request  []string `yaml:"authRequestHeaders"`
								Response []string `yaml:"authResponseHeaders"`
							} `yaml:"forwardAuth"`
						} `yaml:"middlewares"`
					} `yaml:"http"`
				}
				if err := yaml.Unmarshal([]byte(doc.Data["dynamic.yml"]), &dynamic); err != nil {
					t.Fatal(err)
				}
				for _, target := range []string{"runtime", "analytics"} {
					auth := dynamic.HTTP.Middlewares[target+"-token-exchange"].ForwardAuth
					if auth.Address != "http://mcp-platform-api.mcp-platform.svc.cluster.local:8080/internal/auth/exchange/"+target || auth.Trust || auth.Body {
						t.Fatalf("unsafe %s exchange configuration: %+v", target, auth)
					}
					if !reflect.DeepEqual(auth.Request, []string{"Authorization", "X-Api-Key"}) || !reflect.DeepEqual(auth.Response, []string{"Authorization", "X-Api-Key"}) {
						t.Fatalf("unexpected headers: %+v", auth)
					}
				}
			}
			if !found {
				t.Fatal("no Traefik dynamic configuration")
			}
		})
	}
}

func TestAudienceExchangeIngressOwnership(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", "10-gateway.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]string{
		"bundled":   string(raw),
		"host-http": ingressmanifest.RenderPlatformUIIngress("platform.example.com", "", false, "mcp-platform", "mcp-observability"),
		"host-tls":  ingressmanifest.RenderPlatformUIIngress("platform.example.com", "issuer", true, "mcp-platform", "mcp-observability"),
	} {
		t.Run(name, func(t *testing.T) {
			seen := map[string]bool{}
			for _, doc := range decodeExchangeManifests(t, manifest) {
				if doc.Kind != "Ingress" {
					continue
				}
				middleware := doc.Metadata.Annotations["traefik.ingress.kubernetes.io/router.middlewares"]
				for _, rule := range doc.Spec.Rules {
					for _, path := range rule.HTTP.Paths {
						service := path.Backend.Service.Name
						if strings.HasPrefix(path.Path, "/internal") {
							t.Fatalf("internal exchange publicly routed by %s", doc.Metadata.Name)
						}
						expected := ""
						switch service {
						case "mcp-runtime-api":
							expected = "runtime-token-exchange@file"
						case "mcp-analytics-api":
							expected = "analytics-token-exchange@file"
						case "mcp-platform-api":
						default:
							continue
						}
						if middleware != expected {
							t.Fatalf("%s %s routes %s with %q want %q", doc.Metadata.Name, path.Path, service, middleware, expected)
						}
						seen[path.Path] = true
					}
				}
			}
			for _, route := range append(ingressmanifest.PlatformAPIPaths(), ingressmanifest.ObservabilityAPIPaths()...) {
				if !seen[route.Path] {
					t.Fatalf("lost public route %s", route.Path)
				}
			}
		})
	}
}
