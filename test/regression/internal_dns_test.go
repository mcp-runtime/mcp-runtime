package regression

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

// Internal services address peers by four-dot *.svc.cluster.local names. With
// the default ndots:5 every lookup first walks node-inherited search domains,
// which made cold authenticated status calls exceed their deadline (#706).
func TestInternalServiceDeploymentsLowerNdots(t *testing.T) {
	root := repoRoot(t)
	for _, path := range []string{
		"k8s/06-ingest.yaml",
		"k8s/08-platform-api.yaml",
		"k8s/08-runtime-api.yaml",
		"k8s/08-analytics-api.yaml",
		"k8s/09-ui.yaml",
	} {
		t.Run(path, func(t *testing.T) {
			file, err := os.Open(filepath.Join(root, path))
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			decoder := yaml.NewDecoder(file)
			found := false
			for {
				var doc struct {
					Kind string `yaml:"kind"`
					Spec struct {
						Template struct {
							Spec struct {
								DNSConfig struct {
									Options []struct {
										Name  string `yaml:"name"`
										Value string `yaml:"value"`
									} `yaml:"options"`
								} `yaml:"dnsConfig"`
							} `yaml:"spec"`
						} `yaml:"template"`
					} `yaml:"spec"`
				}
				if err := decoder.Decode(&doc); err == io.EOF {
					break
				} else if err != nil {
					t.Fatal(err)
				}
				if doc.Kind != "Deployment" {
					continue
				}
				found = true
				ndots := ""
				for _, option := range doc.Spec.Template.Spec.DNSConfig.Options {
					if option.Name == "ndots" {
						ndots = option.Value
					}
				}
				if ndots != "3" {
					t.Fatalf("ndots = %q, want \"3\" so cluster FQDNs skip search-domain expansion", ndots)
				}
			}
			if !found {
				t.Fatal("no Deployment found")
			}
		})
	}
}
