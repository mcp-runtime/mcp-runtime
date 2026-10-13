package platformrelease

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"

	"mcp-runtime/pkg/platforminventory"
)

// ComponentWiring is the non-image pod configuration a release requires of a
// component's Deployment: the identity it runs as and literal env it needs.
// update adds missing wiring and corrects drift in exactly these fields; it
// never removes other env, so operator customisation is preserved.
type ComponentWiring struct {
	ServiceAccountName           string            `json:"serviceAccountName,omitempty"`
	AutomountServiceAccountToken *bool             `json:"automountServiceAccountToken,omitempty"`
	Env                          map[string]string `json:"env,omitempty"`
}

// IsZero reports whether the wiring asks for nothing.
func (w *ComponentWiring) IsZero() bool {
	return w == nil || (w.ServiceAccountName == "" && w.AutomountServiceAccountToken == nil && len(w.Env) == 0)
}

var (
	envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	dnsLabel       = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
)

func (w *ComponentWiring) validate(where string) []error {
	if w == nil {
		return nil
	}
	var errs []error
	if w.ServiceAccountName != "" && !dnsLabel.MatchString(w.ServiceAccountName) {
		errs = append(errs, fmt.Errorf("%s: wiring.serviceAccountName %q is invalid", where, w.ServiceAccountName))
	}
	for name := range w.Env {
		if !envNamePattern.MatchString(name) {
			errs = append(errs, fmt.Errorf("%s: wiring.env name %q is invalid", where, name))
		}
	}
	return errs
}

// platformObjectKinds are the only kinds a release may ask update to apply.
var platformObjectKinds = map[string]bool{
	"ServiceAccount":     true,
	"Role":               true,
	"RoleBinding":        true,
	"ClusterRole":        true,
	"ClusterRoleBinding": true,
}

// PlatformObject identifies one object in a release's platform bundle.
type PlatformObject struct {
	Kind      string
	Namespace string // empty for cluster-scoped kinds
	Name      string
	Object    map[string]any
}

// Key is a stable identifier for logs and plans.
func (o PlatformObject) Key() string {
	if o.Namespace == "" {
		return o.Kind + "/" + o.Name
	}
	return o.Kind + "/" + o.Namespace + "/" + o.Name
}

func platformNamespaces() map[string]bool {
	set := map[string]bool{}
	for _, c := range Catalog() {
		if c.Namespace != "" {
			set[c.Namespace] = true
		}
	}
	set[platforminventory.ObservabilityNamespace] = true
	return set
}

// ParsePlatformObjects decodes a release's platform bundle and enforces what a
// remote manifest may ask of the cluster: RBAC and ServiceAccount kinds only,
// platform-owned names and namespaces, no wildcard or privilege-escalating
// rules, and bindings only to roles defined in the same bundle and subjects
// that are platform ServiceAccounts. A release can therefore never bind an
// existing powerful role (cluster-admin) or grant itself wildcard access.
func ParsePlatformObjects(bundle string) ([]PlatformObject, error) {
	bundle = strings.TrimSpace(bundle)
	if bundle == "" {
		return nil, nil
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(bundle), 4096)
	var objs []PlatformObject
	for {
		var raw map[string]any
		if err := decoder.Decode(&raw); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("decode platform objects: %w", err)
		}
		if len(raw) == 0 {
			continue
		}
		kind, _ := raw["kind"].(string)
		if !platformObjectKinds[kind] {
			return nil, fmt.Errorf("platform objects may only contain ServiceAccount, Role, RoleBinding, ClusterRole or ClusterRoleBinding; found %q", kind)
		}
		meta, _ := raw["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		namespace, _ := meta["namespace"].(string)
		objs = append(objs, PlatformObject{Kind: kind, Namespace: strings.TrimSpace(namespace), Name: strings.TrimSpace(name), Object: raw})
	}
	return objs, validatePlatformObjects(objs)
}

func validatePlatformObjects(objs []PlatformObject) error {
	var errs []error
	namespaces := platformNamespaces()
	defined := map[string]bool{}
	seen := map[string]bool{}
	for _, o := range objs {
		if o.Kind == "Role" || o.Kind == "ClusterRole" {
			defined[o.Kind+"/"+o.Namespace+"/"+o.Name] = true
		}
	}
	for _, o := range objs {
		key := o.Key()
		if o.Name == "" {
			errs = append(errs, fmt.Errorf("%s object is missing metadata.name", o.Kind))
			continue
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("%s is defined twice", key))
		}
		seen[key] = true
		clusterScoped := o.Kind == "ClusterRole" || o.Kind == "ClusterRoleBinding"
		switch {
		case clusterScoped && o.Namespace != "":
			errs = append(errs, fmt.Errorf("%s must not set a namespace", key))
		case clusterScoped && !strings.HasPrefix(o.Name, "mcp-"):
			errs = append(errs, fmt.Errorf("%s: cluster-scoped names must start with \"mcp-\"", key))
		case !clusterScoped && !namespaces[o.Namespace]:
			errs = append(errs, fmt.Errorf("%s: namespace %q is not a platform namespace", key, o.Namespace))
		}
		switch o.Kind {
		case "Role", "ClusterRole":
			errs = append(errs, validateRules(key, o.Object)...)
		case "RoleBinding", "ClusterRoleBinding":
			errs = append(errs, validateBinding(key, o, defined, namespaces)...)
		}
	}
	return errors.Join(errs...)
}

func validateRules(key string, obj map[string]any) []error {
	var errs []error
	rules, _ := obj["rules"].([]any)
	for i, r := range rules {
		rule, _ := r.(map[string]any)
		for _, field := range []string{"apiGroups", "resources", "verbs"} {
			list, _ := rule[field].([]any)
			for _, v := range list {
				s, _ := v.(string)
				if s == "*" {
					errs = append(errs, fmt.Errorf("%s rules[%d]: wildcard %s are not allowed", key, i, field))
				}
				if field == "verbs" && (s == "escalate" || s == "bind" || s == "impersonate") {
					errs = append(errs, fmt.Errorf("%s rules[%d]: verb %q is not allowed", key, i, s))
				}
			}
		}
	}
	return errs
}

func validateBinding(key string, o PlatformObject, defined, namespaces map[string]bool) []error {
	var errs []error
	ref, _ := o.Object["roleRef"].(map[string]any)
	refKind, _ := ref["kind"].(string)
	refName, _ := ref["name"].(string)
	refNS := ""
	if refKind == "Role" {
		refNS = o.Namespace
	}
	if !defined[refKind+"/"+refNS+"/"+refName] {
		errs = append(errs, fmt.Errorf("%s: roleRef %s/%s must be defined in the same release bundle", key, refKind, refName))
	}
	subjects, _ := o.Object["subjects"].([]any)
	if len(subjects) == 0 {
		errs = append(errs, fmt.Errorf("%s: subjects must not be empty", key))
	}
	for _, s := range subjects {
		subject, _ := s.(map[string]any)
		if kind, _ := subject["kind"].(string); kind != "ServiceAccount" {
			errs = append(errs, fmt.Errorf("%s: subject kind %q is not allowed; only ServiceAccount", key, kind))
		}
		if ns, _ := subject["namespace"].(string); !namespaces[ns] {
			errs = append(errs, fmt.Errorf("%s: subject namespace %q is not a platform namespace", key, ns))
		}
	}
	return errs
}

// BundlePlatformObjects collects the named ServiceAccount/RBAC objects (keys
// like "ClusterRole/mcp-ingest-tokenreview" or
// "ServiceAccount/mcp-observability/mcp-ingest") from the repository's k8s/
// manifests into one validated multi-document bundle, in key order. Releases
// declare the objects update manages explicitly, so update never touches the
// rest of the platform's RBAC.
func BundlePlatformObjects(dir string, keys []string) (string, error) {
	if len(keys) == 0 {
		return "", nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read platform manifest directory %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	found := map[string]string{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- release packaging of repo manifests.
		if err != nil {
			return "", fmt.Errorf("read %s: %w", name, err)
		}
		decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		for {
			var obj map[string]any
			if err := decoder.Decode(&obj); err != nil {
				// Files with templating placeholders are not plain YAML; they
				// cannot hold the objects a release selects, so skip them. A
				// selected object that is not found still fails below.
				break
			}
			kind, _ := obj["kind"].(string)
			if !platformObjectKinds[kind] {
				continue
			}
			meta, _ := obj["metadata"].(map[string]any)
			ns, _ := meta["namespace"].(string)
			n, _ := meta["name"].(string)
			key := PlatformObject{Kind: kind, Namespace: ns, Name: n}.Key()
			if _, dup := found[key]; dup {
				continue // hostpath variants repeat the same objects
			}
			out, err := yaml.Marshal(obj)
			if err != nil {
				return "", fmt.Errorf("encode %s: %w", key, err)
			}
			found[key] = strings.TrimSpace(string(out))
		}
	}
	var docs []string
	for _, key := range keys {
		doc, ok := found[key]
		if !ok {
			return "", fmt.Errorf("platform object %q is not defined in %s", key, dir)
		}
		docs = append(docs, doc)
	}
	bundle := strings.Join(docs, "\n---\n")
	if _, err := ParsePlatformObjects(bundle); err != nil {
		return "", err
	}
	return bundle, nil
}

// ReleaseWiring is what a release asks update to apply beyond images; it is
// the format of config/release/wiring.yaml.
type ReleaseWiring struct {
	// PlatformObjects are keys of the k8s/ objects update manages.
	PlatformObjects []string                    `json:"platformObjects"`
	Components      map[string]*ComponentWiring `json:"components"`
}

// LoadWiring reads config/release/wiring.yaml.
func LoadWiring(path string) (ReleaseWiring, error) {
	data, err := os.ReadFile(path) // #nosec G304 -- release packaging of a repo file.
	if err != nil {
		return ReleaseWiring{}, fmt.Errorf("read wiring %s: %w", path, err)
	}
	var w ReleaseWiring
	if err := yaml.UnmarshalStrict(data, &w); err != nil {
		return ReleaseWiring{}, fmt.Errorf("decode wiring %s: %w", path, err)
	}
	return w, nil
}

// ApplyReleaseExtras embeds the platform bundle and per-component wiring in a
// generated manifest and re-validates it.
func (m *Manifest) ApplyReleaseExtras(platformObjects string, wiring map[string]*ComponentWiring) error {
	m.PlatformObjects = strings.TrimSpace(platformObjects)
	for name, w := range wiring {
		found := false
		for i := range m.Components {
			if m.Components[i].Name == name {
				m.Components[i].Wiring = w
				found = true
			}
		}
		if !found {
			return fmt.Errorf("wiring names component %q, which is not in the manifest", name)
		}
	}
	return m.Validate()
}
