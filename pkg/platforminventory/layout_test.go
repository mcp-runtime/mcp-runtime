package platforminventory

import "testing"

func TestOwnerPlacement(t *testing.T) {
	for key, want := range map[string]string{
		"platform-api":            PlatformNamespace,
		"postgres":                PlatformNamespace,
		"gateway":                 PlatformNamespace,
		"ui":                      PlatformNamespace,
		"analytics-api":           ObservabilityNamespace,
		"clickhouse":              ObservabilityNamespace,
		"ingest":                  ObservabilityNamespace,
		"promtail":                LogCollectorNamespace,
		"operator":                OperatorNamespace,
		"registry":                "registry",
		"traefik":                 "traefik",
		"cert-manager-controller": "cert-manager",
	} {
		component, err := Lookup(key)
		if !err || component.Namespace != want {
			t.Fatalf("%s namespace = %q, ok=%v; want %q", key, component.Namespace, err, want)
		}
	}
	smoke, ok := Lookup("doctor-smoke")
	if !ok || smoke.Namespace != "" {
		t.Fatalf("image-only component = %+v", smoke)
	}
	seen := map[string]Owner{}
	for owner, namespace := range OwnerNamespaces() {
		if other, ok := seen[namespace]; ok {
			t.Fatalf("owners %s and %s share %s", owner, other, namespace)
		}
		seen[namespace] = owner
	}
}

func TestCredentialPlacementFollowsOwningSet(t *testing.T) {
	namespace, name, ok := CredentialPlacement("UI_API_KEY")
	if !ok || name != "mcp-ui-credentials" || namespace != PlatformNamespace {
		t.Fatalf("UI_API_KEY placement = %s/%s ok=%v", namespace, name, ok)
	}
	namespace, name, ok = CredentialPlacement("INGEST_API_KEYS")
	if !ok || name != "mcp-ingest-credentials" || namespace != ObservabilityNamespace {
		t.Fatalf("INGEST_API_KEYS placement = %s/%s ok=%v", namespace, name, ok)
	}
	if _, _, ok := CredentialPlacement("NOT_A_KEY"); ok {
		t.Fatal("unknown key reported an owner")
	}
}

func TestCredentialSetNamespaceFollowsConsumer(t *testing.T) {
	for _, set := range CredentialSets() {
		if set.Namespace() == "" {
			t.Fatalf("credential set %s has no namespace", set.Name)
		}
	}
	for _, set := range CredentialSets() {
		if set.Name == "mcp-grafana-credentials" && set.Namespace() != ObservabilityNamespace {
			t.Fatalf("grafana credentials namespace = %s", set.Namespace())
		}
		if set.Name == "mcp-postgres-credentials" && set.Namespace() != PlatformNamespace {
			t.Fatalf("postgres credentials namespace = %s", set.Namespace())
		}
	}
}

func TestServiceHostUsesOwnerPlacement(t *testing.T) {
	host, err := ServiceHost("runtime-api", 8084)
	if err != nil || host != "mcp-runtime-api."+PlatformNamespace+".svc.cluster.local:8084" {
		t.Fatalf("runtime host = %q, %v", host, err)
	}
	host, err = ServiceHost("clickhouse", 9000)
	if err != nil || host != "clickhouse."+ObservabilityNamespace+".svc.cluster.local:9000" {
		t.Fatalf("clickhouse host = %q, %v", host, err)
	}
	if _, err := ServiceHost("doctor-smoke", 1); err == nil {
		t.Fatal("image-only component returned a service host")
	}
}
