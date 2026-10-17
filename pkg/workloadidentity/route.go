package workloadidentity

import (
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// SelectedTLSOption follows the route's actual TLS configuration. An implicit
// default is resolvable only when the operator declares its platform namespace.
func SelectedTLSOption(route *unstructured.Unstructured, defaultNamespace string) (namespace, name string, err error) {
	if route == nil {
		return "", "", fmt.Errorf("selected IngressRoute is unavailable")
	}
	if _, found, err := unstructured.NestedMap(route.Object, "spec", "tls"); err != nil || !found {
		return "", "", fmt.Errorf("selected route has no TLS configuration")
	}
	name, _, _ = unstructured.NestedString(route.Object, "spec", "tls", "options", "name")
	namespace, _, _ = unstructured.NestedString(route.Object, "spec", "tls", "options", "namespace")
	name, namespace = strings.TrimSpace(name), strings.TrimSpace(namespace)
	if name == "" {
		if strings.TrimSpace(defaultNamespace) == "" {
			return "", "", fmt.Errorf("route uses an unconfigured default TLSOption")
		}
		return strings.TrimSpace(defaultNamespace), "default", nil
	}
	if namespace == "" {
		namespace = route.GetNamespace()
	}
	return namespace, name, nil
}

// ClientAuthCASecrets returns public trust-bundle references only when the
// selected option requests and verifies client certificates.
func ClientAuthCASecrets(option *unstructured.Unstructured) ([]string, error) {
	if option == nil {
		return nil, fmt.Errorf("selected TLSOption is unavailable")
	}
	mode, _, _ := unstructured.NestedString(option.Object, "spec", "clientAuth", "clientAuthType")
	if mode != "VerifyClientCertIfGiven" && mode != "RequireAndVerifyClientCert" {
		return nil, fmt.Errorf("selected route TLSOption does not request and verify client certificates")
	}
	names, _, err := unstructured.NestedStringSlice(option.Object, "spec", "clientAuth", "secretNames")
	if err != nil || len(names) == 0 {
		return nil, fmt.Errorf("selected route TLSOption has no client CA bundle")
	}
	return names, nil
}
