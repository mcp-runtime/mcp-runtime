package platforminventory

import (
	"fmt"
	"maps"
	"sort"

	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	LayoutVersion   = 1
	LegacyLayout    = "legacy-v1"
	SeparatedLayout = "separated-v1"
)

// Layout is a nonsecret placement record. Loading or resolving a record never
// moves resources. Persistence and explicit migration belong to setup.
type Layout struct {
	Version    int              `json:"version"`
	Name       string           `json:"name"`
	Namespaces map[Owner]string `json:"namespaces"`
}

// NewLayout returns a fresh template. Current callers continue to use the
// legacy catalog; choosing the separated template requires an explicit caller.
func NewLayout(name string) (Layout, error) {
	l := Layout{Version: LayoutVersion, Name: name, Namespaces: map[Owner]string{
		Operator: OperatorNamespace, Platform: DefaultNamespace, Observability: DefaultNamespace,
		LogCollector: DefaultNamespace, Certificates: "cert-manager", Registry: "registry", Ingress: "traefik",
	}}
	switch name {
	case LegacyLayout:
	case SeparatedLayout:
		l.Namespaces[Platform] = "mcp-platform"
		l.Namespaces[Observability] = "mcp-observability"
		l.Namespaces[LogCollector] = "mcp-log-collector"
	default:
		return Layout{}, fmt.Errorf("unsupported platform layout %q", name)
	}
	return l, nil
}

func (l Layout) Validate() error {
	template, err := NewLayout(l.Name)
	if err != nil {
		return err
	}
	if l.Version != LayoutVersion {
		return fmt.Errorf("unsupported platform layout version %d", l.Version)
	}
	if len(l.Namespaces) != len(template.Namespaces) {
		return fmt.Errorf("layout must specify exactly the supported owners")
	}
	for owner := range template.Namespaces {
		ns, ok := l.Namespaces[owner]
		if !ok || len(validation.IsDNS1123Label(ns)) != 0 {
			return fmt.Errorf("invalid namespace %q for owner %q", ns, owner)
		}
	}
	if l.Name == LegacyLayout {
		// Without a migration record we recognize the installed legacy layout only.
		if !maps.Equal(l.Namespaces, template.Namespaces) {
			return fmt.Errorf("legacy layout must match the legacy namespace inventory")
		}
	} else {
		seen := map[string]Owner{}
		for owner, ns := range l.Namespaces {
			if other, ok := seen[ns]; ok {
				return fmt.Errorf("separated owners %q and %q share namespace %q", owner, other, ns)
			}
			seen[ns] = owner
		}
		// Registry and upstream infrastructure remain outside this migration.
		for _, owner := range []Owner{Operator, Certificates, Registry, Ingress} {
			if l.Namespaces[owner] != template.Namespaces[owner] {
				return fmt.Errorf("layout cannot relocate owner %q", owner)
			}
		}
	}
	return nil
}

// Component resolves a stable identifier using an explicit, validated layout.
func (l Layout) Component(key string) (Component, error) {
	if err := l.Validate(); err != nil {
		return Component{}, err
	}
	return l.component(key)
}

func (l Layout) component(key string) (Component, error) {
	c, ok := Lookup(key)
	if !ok {
		return Component{}, fmt.Errorf("unknown platform component %q", key)
	}
	if c.Kind != "" {
		c.Namespace = l.Namespaces[c.Owner]
	}
	return c, nil
}

// ResolveLayout checks observed workload namespaces against the record. A nil
// record accepts a verified legacy installation, including absent optional
// workloads. Ambiguous or partially moved installations require an explicit
// migration; ordinary status/update must never choose a namespace by guessing.
// Callers must observe exact inventory workload identities, not label matches.
func ResolveLayout(record *Layout, observed map[string][]string) (Layout, error) {
	l, _ := NewLayout(LegacyLayout)
	if record != nil {
		l = *record
		l.Namespaces = maps.Clone(record.Namespaces)
	}
	if err := l.Validate(); err != nil {
		return Layout{}, err
	}
	keys := make([]string, 0, len(observed))
	for key := range observed {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		c, err := l.component(key)
		if err != nil {
			return Layout{}, err
		}
		if c.Kind == "" && len(observed[key]) > 0 {
			return Layout{}, fmt.Errorf("image-only component %q cannot have an observed workload", key)
		}
		for _, ns := range observed[key] {
			if ns != c.Namespace {
				return Layout{}, fmt.Errorf("component %q observed in %q, expected %q; explicit migration required", key, ns, c.Namespace)
			}
		}
	}
	return l, nil
}
