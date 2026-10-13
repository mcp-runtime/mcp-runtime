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
	// Args are container flags in --name or --name=value form. A missing flag
	// is appended; an existing flag with the same name is replaced unless the
	// flag is repeatable (see RepeatableFlags).
	Args []string `json:"args,omitempty"`
	// DNSOptions are pod resolver options (for example ndots) that must be set.
	DNSOptions []DNSOption `json:"dnsOptions,omitempty"`
}

// DNSOption is one pod dnsConfig option.
type DNSOption struct {
	Name  string `json:"name"`
	Value string `json:"value,omitempty"`
}

// IsZero reports whether the wiring asks for nothing.
func (w *ComponentWiring) IsZero() bool {
	return w == nil || (w.ServiceAccountName == "" && w.AutomountServiceAccountToken == nil &&
		len(w.Env) == 0 && len(w.Args) == 0 && len(w.DNSOptions) == 0)
}

// RepeatableFlags are flags that may legitimately appear more than once, so
// wiring appends them instead of replacing an existing occurrence.
var RepeatableFlags = map[string]bool{"--enable-feature": true}

// FlagName returns the flag part of a --name or --name=value argument.
func FlagName(arg string) string {
	if i := strings.IndexByte(arg, '='); i >= 0 {
		return arg[:i]
	}
	return arg
}

var (
	envNamePattern   = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	dnsLabel         = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)
	argPattern       = regexp.MustCompile(`^--[a-zA-Z0-9][a-zA-Z0-9.-]*(=[^\s]*)?$`)
	dnsOptionPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
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
	for _, arg := range w.Args {
		if !argPattern.MatchString(arg) {
			errs = append(errs, fmt.Errorf("%s: wiring.args entry %q must look like --flag or --flag=value", where, arg))
		}
	}
	for _, opt := range w.DNSOptions {
		if !dnsOptionPattern.MatchString(opt.Name) {
			errs = append(errs, fmt.Errorf("%s: wiring.dnsOptions name %q is invalid", where, opt.Name))
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
	ConfigMaps      []ConfigMapSpec             `json:"configMaps"`
	Components      map[string]*ComponentWiring `json:"components"`
	// ConfigOnly names components whose image the release does not manage but
	// whose wiring or config-driven restarts update applies.
	ConfigOnly []string `json:"configOnly"`
	// ThirdPartyImages names components whose image pin is taken from their
	// Deployment in k8s/ (for example the platform gateway's Traefik).
	ThirdPartyImages []string `json:"thirdPartyImages"`
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

// ReleaseExtras is everything a release declares beyond images.
type ReleaseExtras struct {
	PlatformObjects string
	ConfigMaps      []ManifestConfigMap
	Wiring          map[string]*ComponentWiring
	ConfigOnly      []string
}

// ApplyReleaseExtras embeds the platform bundle, ConfigMaps and per-component
// wiring in a generated manifest and re-validates it.
func (m *Manifest) ApplyReleaseExtras(extras ReleaseExtras) error {
	m.PlatformObjects = strings.TrimSpace(extras.PlatformObjects)
	m.ConfigMaps = extras.ConfigMaps
	for _, name := range extras.ConfigOnly {
		if _, ok := Lookup(name); !ok {
			return fmt.Errorf("configOnly names component %q, which is not in the catalog", name)
		}
		m.Components = append(m.Components, ManifestComponent{Name: name, ConfigOnly: true})
	}
	for name, w := range extras.Wiring {
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

const maxConfigMapBytes = 1 << 20

func validateConfigMaps(cms []ManifestConfigMap) []error {
	var errs []error
	namespaces := platformNamespaces()
	seen := map[string]bool{}
	for _, cm := range cms {
		key := cm.Key()
		switch {
		case cm.Name == "" || !dnsLabel.MatchString(cm.Name):
			errs = append(errs, fmt.Errorf("configMaps %q: invalid name", key))
		case !namespaces[cm.Namespace]:
			errs = append(errs, fmt.Errorf("configMaps %s: namespace %q is not a platform namespace", key, cm.Namespace))
		}
		if seen[key] {
			errs = append(errs, fmt.Errorf("configMaps %s is declared twice", key))
		}
		seen[key] = true
		if len(cm.Data) == 0 {
			errs = append(errs, fmt.Errorf("configMaps %s declares no data", key))
		}
		size := 0
		for k, v := range cm.Data {
			if k == "" || strings.ContainsAny(k, "/\\") {
				errs = append(errs, fmt.Errorf("configMaps %s: invalid key %q", key, k))
			}
			size += len(k) + len(v)
		}
		if size > maxConfigMapBytes {
			errs = append(errs, fmt.Errorf("configMaps %s exceeds %d bytes", key, maxConfigMapBytes))
		}
		for _, name := range cm.Restart {
			if c, ok := Lookup(name); !ok || !c.HasWorkload() {
				errs = append(errs, fmt.Errorf("configMaps %s: restart component %q is unknown or has no workload", key, name))
			}
		}
	}
	return errs
}

// ConfigMapSpec selects a k8s/ ConfigMap for a release.
type ConfigMapSpec struct {
	// Key is "<namespace>/<name>".
	Key     string   `json:"key"`
	Restart []string `json:"restart,omitempty"`
}

// BundleConfigMaps reads the named ConfigMaps (all string data keys) from the
// repository's k8s/ manifests.
func BundleConfigMaps(dir string, specs []ConfigMapSpec) ([]ManifestConfigMap, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read platform manifest directory %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	found := map[string]ManifestConfigMap{}
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- release packaging of repo manifests.
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		for {
			var obj struct {
				Kind     string `json:"kind"`
				Metadata struct {
					Name      string `json:"name"`
					Namespace string `json:"namespace"`
				} `json:"metadata"`
				Data map[string]string `json:"data"`
			}
			if err := decoder.Decode(&obj); err != nil {
				break // EOF, or a templated file that cannot hold a selected ConfigMap
			}
			if obj.Kind != "ConfigMap" {
				continue
			}
			cm := ManifestConfigMap{Namespace: obj.Metadata.Namespace, Name: obj.Metadata.Name, Data: obj.Data}
			if _, dup := found[cm.Key()]; !dup {
				found[cm.Key()] = cm
			}
		}
	}
	var out []ManifestConfigMap
	for _, spec := range specs {
		cm, ok := found[spec.Key]
		if !ok {
			return nil, fmt.Errorf("ConfigMap %q is not defined in %s", spec.Key, dir)
		}
		cm.Restart = spec.Restart
		out = append(out, cm)
	}
	if errs := validateConfigMaps(out); len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	return out, nil
}

// K8sDeploymentImage returns repository and tag of a component's container
// image as written in the repository's k8s/ manifests.
func K8sDeploymentImage(dir, component string) (repository, tag string, err error) {
	c, ok := Lookup(component)
	if !ok || !c.HasWorkload() {
		return "", "", fmt.Errorf("component %q has no workload", component)
	}
	dep, err := FindK8sDeployment(dir, c.Namespace, c.Deployment)
	if err != nil {
		return "", "", err
	}
	containers, _ := nestedMap(dep, "spec", "template", "spec")["containers"].([]any)
	for _, ci := range containers {
		container, _ := ci.(map[string]any)
		if container["name"] != c.Container {
			continue
		}
		image, _ := container["image"].(string)
		ref, perr := ParseImageRef(image)
		if perr != nil {
			return "", "", fmt.Errorf("%s image %q: %w", component, image, perr)
		}
		ref.Tag = ""
		ref.Digest = ""
		repo := ref.String()
		_, tagPart := splitImageTag(image)
		return repo, tagPart, nil
	}
	return "", "", fmt.Errorf("container %q not found in Deployment %s/%s", c.Container, c.Namespace, c.Deployment)
}

func splitImageTag(image string) (string, string) {
	slash := strings.LastIndexByte(image, '/')
	if i := strings.LastIndexByte(image, ':'); i > slash {
		return image[:i], image[i+1:]
	}
	return image, ""
}

func nestedMap(m map[string]any, path ...string) map[string]any {
	for _, p := range path {
		next, _ := m[p].(map[string]any)
		m = next
	}
	return m
}

// FindK8sDeployment loads a Deployment document from the repository's k8s/ manifests.
func FindK8sDeployment(dir, namespace, name string) (map[string]any, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !e.IsDir() && (ext == ".yaml" || ext == ".yml") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(dir, f)) // #nosec G304 -- repo manifests.
		if err != nil {
			return nil, err
		}
		decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(data)), 4096)
		for {
			var obj map[string]any
			if err := decoder.Decode(&obj); err != nil {
				break
			}
			meta, _ := obj["metadata"].(map[string]any)
			if obj["kind"] == "Deployment" && meta["name"] == name && meta["namespace"] == namespace {
				return obj, nil
			}
		}
	}
	return nil, fmt.Errorf("deployment %s/%s not found in %s", namespace, name, dir)
}
