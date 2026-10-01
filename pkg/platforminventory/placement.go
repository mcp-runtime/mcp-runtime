package platforminventory

import "fmt"

// ownerNamespace is the only owner-to-namespace map. A component declares an
// owner; Lookup returns that owner's namespace. There is no layout version.
var ownerNamespace = map[Owner]string{
	Operator:      OperatorNamespace,
	Platform:      PlatformNamespace,
	Observability: ObservabilityNamespace,
	LogCollector:  LogCollectorNamespace,
	Certificates:  "cert-manager",
	Registry:      "registry",
	Ingress:       "traefik",
}

// OwnerNamespaces returns a copy of the install placement.
func OwnerNamespaces() map[Owner]string {
	out := make(map[Owner]string, len(ownerNamespace))
	for owner, namespace := range ownerNamespace {
		out[owner] = namespace
	}
	return out
}

// Namespace returns the install namespace for a credential set from its first
// consumer. A set with no consumer stays with the platform owner.
func (set CredentialSet) Namespace() string {
	if len(set.Consumers) == 0 {
		return PlatformNamespace
	}
	component, ok := Lookup(set.Consumers[0])
	if !ok {
		return ""
	}
	return component.Namespace
}

// CredentialPlacement returns the namespace and Secret that own a key.
func CredentialPlacement(key string) (namespace, name string, ok bool) {
	name, ok = CredentialOwner(key)
	if !ok {
		return "", "", false
	}
	for _, set := range credentialSets {
		if set.Name == name {
			return set.Namespace(), set.Name, true
		}
	}
	return "", "", false
}

// ServiceHost is the in-cluster DNS name for a component Service port.
func ServiceHost(key string, port int) (string, error) {
	component, ok := Lookup(key)
	if !ok || component.Resource == "" || component.Namespace == "" {
		return "", fmt.Errorf("component %q has no service placement", key)
	}
	return fmt.Sprintf("%s.%s.svc.cluster.local:%d", component.Resource, component.Namespace, port), nil
}
