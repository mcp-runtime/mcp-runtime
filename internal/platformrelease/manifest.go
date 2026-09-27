package platformrelease

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/yaml"
)

const (
	// ManifestAPIVersion is the release manifest schema version.
	ManifestAPIVersion = "mcpruntime.org/v1alpha1"
	// ManifestKind is the release manifest kind.
	ManifestKind = "PlatformRelease"
	// ManifestAssetName is the file name attached to GitHub releases.
	ManifestAssetName = "platform-manifest.json"
	// CRDsAssetName is the standalone multi-document CRD YAML attached to
	// GitHub releases. update also accepts CRDs embedded in the manifest.
	CRDsAssetName = "platform-crds.yaml"
	// DefaultReleaseBaseURL is where --to resolves release manifests.
	DefaultReleaseBaseURL = "https://github.com/mcp-runtime/mcp-runtime/releases/download"

	maxManifestBytes    = 4 << 20
	manifestHTTPTimeout = 30 * time.Second
)

// Manifest is the authoritative per-release component version list.
type Manifest struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Version is the platform release version (semver).
	Version string `json:"version"`
	// Registry optionally resolves relative component repositories. When
	// empty, relative repositories resolve against the registry host of the
	// image currently running in the cluster.
	Registry string `json:"registry,omitempty"`
	// CRDChange reports that this release changes CustomResourceDefinitions.
	// When true, update applies CRDs from CRDs (or --crds / platform-crds.yaml)
	// before patching component images.
	CRDChange bool `json:"crdChange,omitempty"`
	// CRDs is a multi-document YAML of CustomResourceDefinitions embedded in
	// the release manifest so `mcp-runtime update --to` can apply schema
	// changes without a separate setup run.
	CRDs       string              `json:"crds,omitempty"`
	Components []ManifestComponent `json:"components"`
}

// ManifestComponent pins one component image.
type ManifestComponent struct {
	Name       string `json:"name"`
	Repository string `json:"repository"`
	Tag        string `json:"tag"`
	Digest     string `json:"digest,omitempty"`
}

// Component returns the manifest entry for name.
func (m *Manifest) Component(name string) (ManifestComponent, bool) {
	for _, c := range m.Components {
		if c.Name == name {
			return c, true
		}
	}
	return ManifestComponent{}, false
}

// ParseManifest decodes (JSON or YAML) and validates a release manifest.
func ParseManifest(data []byte) (*Manifest, error) {
	var m Manifest
	if err := yaml.UnmarshalStrict(data, &m); err != nil {
		return nil, fmt.Errorf("decode release manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// Validate checks manifest shape, versions, references, and component names.
func (m *Manifest) Validate() error {
	var errs []error
	if m.APIVersion != ManifestAPIVersion {
		errs = append(errs, fmt.Errorf("apiVersion must be %q, got %q", ManifestAPIVersion, m.APIVersion))
	}
	if m.Kind != ManifestKind {
		errs = append(errs, fmt.Errorf("kind must be %q, got %q", ManifestKind, m.Kind))
	}
	if _, err := ParseVersion(m.Version); err != nil {
		errs = append(errs, fmt.Errorf("version: %w", err))
	}
	if m.Registry != "" && !hostPattern.MatchString(m.Registry) {
		errs = append(errs, fmt.Errorf("registry %q is not a valid registry host", m.Registry))
	}
	if len(m.Components) == 0 {
		errs = append(errs, errors.New("components must not be empty"))
	}
	seen := map[string]bool{}
	for i, c := range m.Components {
		where := fmt.Sprintf("components[%d] (%s)", i, c.Name)
		if _, ok := Lookup(c.Name); !ok {
			errs = append(errs, fmt.Errorf("%s: unknown component; known components: %s", where, strings.Join(ComponentNames(), ", ")))
		}
		if seen[c.Name] {
			errs = append(errs, fmt.Errorf("%s: duplicate component", where))
		}
		seen[c.Name] = true
		if !ValidTag(c.Tag) {
			errs = append(errs, fmt.Errorf("%s: tag %q is invalid", where, c.Tag))
		}
		if c.Digest != "" && !ValidDigest(c.Digest) {
			errs = append(errs, fmt.Errorf("%s: digest %q is invalid (want sha256:<64 hex>)", where, c.Digest))
		}
		ref, err := ParseImageRef(c.Repository)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: repository: %w", where, err))
		} else if ref.Tag != "" || ref.Digest != "" {
			errs = append(errs, fmt.Errorf("%s: repository %q must not include a tag or digest; use the tag/digest fields", where, c.Repository))
		}
	}
	return errors.Join(errs...)
}

// TargetRef resolves the manifest entry into an image reference. Relative
// repositories resolve against m.Registry, then fallbackRegistry.
func (m *Manifest) TargetRef(c ManifestComponent, fallbackRegistry string) (ImageRef, error) {
	ref, err := ParseImageRef(c.Repository)
	if err != nil {
		return ImageRef{}, err
	}
	if ref.Registry == "" {
		if m.Registry != "" {
			ref.Registry = m.Registry
		} else {
			ref.Registry = fallbackRegistry
		}
	}
	ref.Tag = c.Tag
	ref.Digest = c.Digest
	return ref, nil
}

// ReleaseManifestURL returns the GitHub release asset URL for a version.
func ReleaseManifestURL(version string) string {
	return DefaultReleaseBaseURL + "/" + url.PathEscape(version) + "/" + ManifestAssetName
}

// ReleaseCRDsURL returns the GitHub release asset URL for the CRD bundle.
func ReleaseCRDsURL(version string) string {
	return DefaultReleaseBaseURL + "/" + url.PathEscape(version) + "/" + CRDsAssetName
}

// LoadManifest reads a manifest from a local path or an https:// URL.
func LoadManifest(ctx context.Context, source string, client *http.Client) ([]byte, error) {
	source = strings.TrimSpace(source)
	lower := strings.ToLower(source)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return fetchManifest(ctx, source, client)
	case strings.HasPrefix(lower, "http://"):
		return nil, fmt.Errorf("refusing to fetch release manifest over plain http: %s (use https or a local file)", source)
	}
	f, err := os.Open(source) // #nosec G304 -- operator-supplied manifest path.
	if err != nil {
		return nil, fmt.Errorf("open release manifest: %w", err)
	}
	defer f.Close()
	return readLimited(f)
}

func fetchManifest(ctx context.Context, rawURL string, client *http.Client) ([]byte, error) {
	if client == nil {
		client = &http.Client{Timeout: manifestHTTPTimeout}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build release manifest request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch release manifest %s: %w", rawURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch release manifest %s: HTTP %d (does the release publish %s?)", rawURL, resp.StatusCode, ManifestAssetName)
	}
	return readLimited(resp.Body)
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxManifestBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read release manifest: %w", err)
	}
	if len(data) > maxManifestBytes {
		return nil, fmt.Errorf("release manifest exceeds %d bytes", maxManifestBytes)
	}
	return data, nil
}

// GenerateManifest builds the default release manifest for version: every
// MCP Runtime-built component, relative repositories, tag = version. When
// crdChange is true, crdsYAML (multi-document CustomResourceDefinition YAML)
// is embedded so update can apply schema changes from the release JSON alone.
func GenerateManifest(version string, crdChange bool, crdsYAML string) (*Manifest, error) {
	if _, err := ParseVersion(version); err != nil {
		return nil, err
	}
	m := &Manifest{APIVersion: ManifestAPIVersion, Kind: ManifestKind, Version: version, CRDChange: crdChange}
	if crdChange {
		m.CRDs = strings.TrimSpace(crdsYAML)
		if m.CRDs == "" {
			return nil, errors.New("crdChange requires non-empty CRD YAML")
		}
	}
	for _, c := range catalog {
		if !c.Built || c.Repository == "" {
			continue
		}
		m.Components = append(m.Components, ManifestComponent{Name: c.Name, Repository: c.Repository, Tag: version})
	}
	return m, m.Validate()
}

// BundleCRDs reads every .yaml/.yml file under dir (lexically sorted) and
// returns a multi-document YAML bundle suitable for embedding in a release
// manifest or publishing as platform-crds.yaml.
func BundleCRDs(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("read CRD directory %s: %w", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".yaml" && ext != ".yml" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if len(names) == 0 {
		return "", fmt.Errorf("no YAML CRDs found in %s", dir)
	}
	var b strings.Builder
	for i, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name)) // #nosec G304 -- release packaging of repo CRDs.
		if err != nil {
			return "", fmt.Errorf("read CRD %s: %w", name, err)
		}
		text := strings.TrimSpace(string(data))
		if text == "" {
			continue
		}
		if i > 0 {
			b.WriteString("\n---\n")
		}
		b.WriteString(text)
		b.WriteByte('\n')
	}
	out := strings.TrimSpace(b.String())
	if out == "" {
		return "", fmt.Errorf("CRD directory %s produced an empty bundle", dir)
	}
	return out, nil
}

// FilterCRDBundle keeps only CustomResourceDefinition documents from a
// multi-document YAML bundle and returns their metadata.name values in order.
// Any other non-empty kind is rejected so update cannot apply arbitrary
// cluster objects from a release or --crds file.
func FilterCRDBundle(crdsYAML string) (filtered string, names []string, err error) {
	crdsYAML = strings.TrimSpace(crdsYAML)
	if crdsYAML == "" {
		return "", nil, nil
	}
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(crdsYAML), 4096)
	var docs []string
	for {
		var obj map[string]any
		if err := decoder.Decode(&obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return "", nil, fmt.Errorf("decode CRD bundle: %w", err)
		}
		if len(obj) == 0 {
			continue
		}
		kind, _ := obj["kind"].(string)
		if kind != "CustomResourceDefinition" {
			return "", nil, fmt.Errorf("CRD bundle may only contain CustomResourceDefinition objects; found %q", kind)
		}
		meta, _ := obj["metadata"].(map[string]any)
		name, _ := meta["name"].(string)
		name = strings.TrimSpace(name)
		if name == "" {
			return "", nil, errors.New("CustomResourceDefinition missing metadata.name")
		}
		raw, err := yaml.Marshal(obj)
		if err != nil {
			return "", nil, fmt.Errorf("encode CustomResourceDefinition %s: %w", name, err)
		}
		docs = append(docs, strings.TrimSpace(string(raw)))
		names = append(names, name)
	}
	if len(docs) == 0 {
		return "", nil, nil
	}
	return strings.Join(docs, "\n---\n") + "\n", names, nil
}

// CRDObjectNames returns metadata.name values for CustomResourceDefinition
// documents in a multi-document YAML bundle.
func CRDObjectNames(crdsYAML string) ([]string, error) {
	_, names, err := FilterCRDBundle(crdsYAML)
	return names, err
}
