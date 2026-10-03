package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/strategicpatch"
	"sigs.k8s.io/yaml"
)

// Exercise Kubernetes strategic-merge semantics, including deleting the
// inherited nodePort. Checking only type would leave a forbidden stale port.
func TestHTTPSRegistryOverlaysRemoveBackendNodePort(t *testing.T) {
	root := "../../../../config/registry"
	read := func(path string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(root, path))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	base, err := yaml.YAMLToJSON(read("base/service.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, overlay := range []string{"tls", "hostpath-tls"} {
		patch, err := yaml.YAMLToJSON(read("overlays/" + overlay + "/service-clusterip.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		merged, err := strategicpatch.StrategicMergePatch(base, patch, corev1.Service{})
		if err != nil {
			t.Fatal(err)
		}
		var service corev1.Service
		if err := yaml.Unmarshal(merged, &service); err != nil {
			t.Fatal(err)
		}
		if service.Spec.Type != corev1.ServiceTypeClusterIP || len(service.Spec.Ports) != 1 || service.Spec.Ports[0].NodePort != 0 || service.Spec.Ports[0].Port != 5000 || service.Spec.Ports[0].TargetPort.IntVal != 5000 {
			t.Fatalf("%s: HTTPS backend exposure or pull listener changed: %+v", overlay, service.Spec)
		}
	}
	for _, overlay := range []string{"tls", "hostpath-tls"} {
		if !strings.Contains(string(read("overlays/"+overlay+"/kustomization.yaml")), "service-clusterip.yaml") {
			t.Errorf("%s omits NodePort removal", overlay)
		}
	}
	for overlay, parent := range map[string]string{"internal-tls": "../tls", "hostpath-internal-tls": "../hostpath-tls"} {
		if !strings.Contains(string(read("overlays/"+overlay+"/kustomization.yaml")), parent) {
			t.Errorf("%s no longer inherits protected HTTPS Service", overlay)
		}
	}
	var lab corev1.Service
	if err := yaml.Unmarshal(read("base/service.yaml"), &lab); err != nil {
		t.Fatal(err)
	}
	if lab.Spec.Type != corev1.ServiceTypeNodePort || lab.Spec.Ports[0].NodePort != 32000 {
		t.Fatal("HTTP lab pull path changed")
	}
}
