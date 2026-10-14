package platformrelease

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// ReleaseOptions selects what GenerateReleaseManifest reads from the repository.
type ReleaseOptions struct {
	// Version is the release version (for example v0.8.0) and Commit the full
	// lowercase Git SHA of the release source.
	Version string
	Commit  string
	// CRDChange and CRDsYAML embed the CRD bundle when the release changes CRDs.
	CRDChange bool
	CRDsYAML  string
	// K8sDir is the directory of platform manifests (k8s/). WiringPath is
	// config/release/wiring.yaml; empty skips release-declared extras.
	K8sDir     string
	WiringPath string
}

// GenerateReleaseManifest builds the complete platform-manifest.json for a
// release exactly as the release workflow publishes it: built images tagged to
// the commit, third-party image pins from k8s/, config-only components, and the
// declared ServiceAccount/RBAC objects, ConfigMaps and wiring. The result has
// been verified with VerifyRelease and parses with the strict decoder update uses.
func GenerateReleaseManifest(opts ReleaseOptions) (*Manifest, error) {
	m, err := GenerateManifestForCommit(opts.Version, opts.Commit, opts.CRDChange, opts.CRDsYAML)
	if err != nil {
		return nil, err
	}
	var release ReleaseWiring
	if path := strings.TrimSpace(opts.WiringPath); path != "" {
		if release, err = LoadWiring(path); err != nil {
			return nil, err
		}
	}
	dir := strings.TrimSpace(opts.K8sDir)
	objects, err := BundlePlatformObjects(dir, release.PlatformObjects)
	if err != nil {
		return nil, err
	}
	configMaps, err := BundleConfigMaps(dir, release.ConfigMaps)
	if err != nil {
		return nil, err
	}
	// Third-party image pins come from the same k8s/ Deployment setup renders.
	for _, name := range release.ThirdPartyImages {
		repository, tag, err := K8sDeploymentImage(dir, name)
		if err != nil {
			return nil, err
		}
		m.Components = append(m.Components, ManifestComponent{Name: name, Repository: repository, Tag: tag})
	}
	if err := m.ApplyReleaseExtras(ReleaseExtras{
		PlatformObjects: objects,
		ConfigMaps:      configMaps,
		Wiring:          release.Components,
		ConfigOnly:      release.ConfigOnly,
	}); err != nil {
		return nil, err
	}
	if err := m.VerifyRelease(opts.Version, opts.Commit); err != nil {
		return nil, err
	}
	return m, nil
}

// VerifyRelease checks the invariants a published release manifest must hold,
// so a release cannot discover a broken manifest at tag time:
//   - every component MCP Runtime builds is present and tagged to the commit;
//   - third-party pins carry an explicit upstream tag, never the commit tag or latest;
//   - config-only components carry no image coordinates;
//   - the manifest round-trips through the strict decoder `update` uses.
func (m *Manifest) VerifyRelease(version, commit string) error {
	if m.Version != version {
		return fmt.Errorf("manifest version %q, want %q", m.Version, version)
	}
	commitTag := version + "-g" + commit
	var errs []error
	have := map[string]ManifestComponent{}
	for _, c := range m.Components {
		have[c.Name] = c
	}
	for _, c := range Catalog() {
		entry, ok := have[c.Name]
		switch {
		case c.Built && c.Repository != "":
			if !ok {
				errs = append(errs, fmt.Errorf("built component %s is missing from the manifest", c.Name))
			} else if entry.Tag != commitTag {
				errs = append(errs, fmt.Errorf("built component %s has tag %q, want %q", c.Name, entry.Tag, commitTag))
			}
		case ok && entry.ConfigOnly:
			if entry.Tag != "" || entry.Repository != "" || entry.Digest != "" {
				errs = append(errs, fmt.Errorf("config-only component %s must not carry image coordinates", c.Name))
			}
		case ok:
			// An image pin for a component MCP Runtime does not build.
			if entry.Tag == "" || entry.Tag == "latest" || entry.Tag == commitTag {
				errs = append(errs, fmt.Errorf("third-party component %s needs an explicit upstream tag, got %q", c.Name, entry.Tag))
			}
		}
	}
	data, err := json.Marshal(m)
	if err != nil {
		errs = append(errs, err)
	} else if _, err := ParseManifest(data); err != nil {
		errs = append(errs, fmt.Errorf("manifest does not round-trip through the update decoder: %w", err))
	}
	return errors.Join(errs...)
}
