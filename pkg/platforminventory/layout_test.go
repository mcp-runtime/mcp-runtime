package platforminventory

import "testing"

func TestLayoutResolution(t *testing.T) {
	legacy, err := ResolveLayout(nil, map[string][]string{"platform-api": {"mcp-sentinel"}, "operator": {"mcp-runtime"}})
	if err != nil || legacy.Name != LegacyLayout {
		t.Fatalf("legacy discovery: %+v, %v", legacy, err)
	}
	separated, _ := NewLayout(SeparatedLayout)
	for key, want := range map[string]string{"platform-api": "mcp-platform", "postgres": "mcp-platform", "gateway": "mcp-platform", "analytics-api": "mcp-observability", "clickhouse": "mcp-observability", "promtail": "mcp-log-collector", "operator": "mcp-runtime", "registry": "registry", "traefik": "traefik", "cert-manager-controller": "cert-manager", "doctor-smoke": ""} {
		c, err := separated.Component(key)
		if err != nil || c.Namespace != want {
			t.Fatalf("%s = %q, %v; want %q", key, c.Namespace, err, want)
		}
	}
	got, err := ResolveLayout(&separated, map[string][]string{"platform-api": {"mcp-platform"}})
	if err != nil {
		t.Fatal(err)
	}
	got.Namespaces[Platform] = "changed"
	if separated.Namespaces[Platform] != "mcp-platform" {
		t.Fatal("resolution aliases caller record")
	}
	c, _ := Lookup("platform-api")
	if c.Namespace != "mcp-sentinel" {
		t.Fatal("resolving a layout changed current defaults")
	}
}

func TestLayoutRejectsAmbiguityAndDrift(t *testing.T) {
	separated, _ := NewLayout(SeparatedLayout)
	for _, tt := range []struct {
		name     string
		record   *Layout
		observed map[string][]string
	}{
		{"missing record after move", nil, map[string][]string{"platform-api": {"mcp-platform"}}},
		{"both placements", nil, map[string][]string{"platform-api": {"mcp-sentinel", "mcp-platform"}}},
		{"partial migration", &separated, map[string][]string{"platform-api": {"mcp-sentinel"}}},
		{"unknown component", nil, map[string][]string{"rogue": {"mcp-sentinel"}}},
		{"image has workload", nil, map[string][]string{"doctor-smoke": {"mcp-runtime"}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ResolveLayout(tt.record, tt.observed); err == nil {
				t.Fatal("accepted unsafe discovery")
			}
		})
	}
}

func TestLayoutValidation(t *testing.T) {
	for _, mutate := range []func(*Layout){
		func(l *Layout) { l.Version++ },
		func(l *Layout) { l.Name = "unknown" },
		func(l *Layout) { delete(l.Namespaces, Platform) },
		func(l *Layout) { l.Namespaces["unknown"] = "unknown" },
		func(l *Layout) { l.Namespaces[Platform] = "INVALID" },
		func(l *Layout) { l.Namespaces[Platform] = l.Namespaces[Observability] },
		func(l *Layout) { l.Namespaces[Operator] = "mcp-system" },
		func(l *Layout) { l.Namespaces[Registry] = "new-registry" },
	} {
		l, _ := NewLayout(SeparatedLayout)
		mutate(&l)
		if err := l.Validate(); err == nil {
			t.Fatalf("accepted malformed record: %+v", l)
		}
	}
	l, _ := NewLayout(LegacyLayout)
	l.Namespaces[Platform] = "mcp-platform"
	if err := l.Validate(); err == nil {
		t.Fatal("accepted modified legacy record")
	}
}
