package platforminventory

import (
	"reflect"
	"testing"
)

func TestCatalogIdentityAndDependencies(t *testing.T) {
	seen := map[string]bool{}
	aliases := map[string]string{}
	for _, c := range Catalog() {
		if seen[c.Key] || c.Key == "" || c.Owner == "" || len(c.Capabilities) == 0 {
			t.Fatalf("invalid inventory entry: %+v", c)
		}
		seen[c.Key] = true
		for _, name := range append([]string{c.Key}, c.Aliases...) {
			if other, ok := aliases[name]; ok {
				t.Fatalf("alias %q shared by %s and %s", name, other, c.Key)
			}
			aliases[name] = c.Key
		}
		if c.Kind != "" && (c.Namespace == "" || c.Resource == "") {
			t.Fatalf("incomplete workload: %+v", c)
		}
	}
	for _, c := range Catalog() {
		for _, d := range c.Dependencies {
			if !seen[d.Component] || d.Component == c.Key {
				t.Fatalf("invalid dependency: %s -> %s", c.Key, d.Component)
			}
		}
	}
	for name, key := range map[string]string{" API ": "platform-api", "RUNTIME": "runtime-api", "analytics": "analytics-api", "prom": "prometheus", "otel": "otel-collector"} {
		c, ok := Find(name)
		if !ok || c.Key != key {
			t.Fatalf("Find(%q) = %+v, %v", name, c, ok)
		}
	}
	if _, ok := Lookup("api"); ok {
		t.Fatal("release lookup must not accept CLI aliases")
	}
}

func TestCatalogCopiesNestedMetadata(t *testing.T) {
	c, _ := Lookup("platform-api")
	c.Aliases[0] = "corrupted"
	c.Capabilities[0] = "corrupted"
	c.Dependencies[0].Component = "corrupted"
	c.PortTarget.RemotePort = 1
	next, _ := Lookup("platform-api")
	if next.Aliases[0] != "api" || next.Capabilities[0] != Control || next.Dependencies[0].Component != "postgres" || next.PortTarget.RemotePort != 8080 {
		t.Fatalf("caller mutated canonical inventory: %+v", next)
	}
}

func TestPlatformManagementSurfaceIsStable(t *testing.T) {
	want := []string{"clickhouse", "kafka", "ingest", "platform-api", "runtime-api", "analytics-api", "processor", "ui", "gateway", "prometheus", "grafana", "otel-collector", "tempo", "loki", "promtail"}
	var got []string
	for _, c := range PlatformComponents(false) {
		got = append(got, c.Key)
		if c.Kind != "" && c.Namespace != ownerNamespace[c.Owner] {
			t.Fatalf("unexpected component placement: %+v", c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("management surface changed: %v", got)
	}
	withOperator := PlatformComponents(true)
	if len(withOperator) != 16 || withOperator[0].Key != "operator" {
		t.Fatalf("public status surface changed: %+v", withOperator)
	}
}
