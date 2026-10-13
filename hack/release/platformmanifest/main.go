// Command platformmanifest prints the platform release component manifest
// (platform-manifest.json) for a release version, generated from the CLI's
// component catalog so `mcp-runtime update` and releases stay in sync.
//
//	go run ./hack/release/platformmanifest -version v0.5.0 -commit <full-git-sha> [-crd-change] [-crds-dir config/crd/bases] [-k8s-dir k8s] [-wiring config/release/wiring.yaml]
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"

	"mcp-runtime/internal/platformrelease"
)

func main() {
	version := flag.String("version", "", "release version (semver, for example v0.5.0)")
	commit := flag.String("commit", "", "full Git commit SHA of the release source")
	crdChange := flag.Bool("crd-change", false, "mark the release as changing CRDs and embed them for update")
	crdsDir := flag.String("crds-dir", "config/crd/bases", "directory of CRD YAML files to embed when -crd-change is set")
	k8sDir := flag.String("k8s-dir", "k8s", "directory of platform manifests holding the ServiceAccount/RBAC objects the wiring file selects")
	wiringPath := flag.String("wiring", "config/release/wiring.yaml", "per-component Deployment wiring to embed for update (empty to skip)")
	writeCRDs := flag.String("write-crds", "", "optional path to write the standalone platform-crds.yaml bundle")
	flag.Parse()

	crdsYAML := ""
	if *crdChange || strings.TrimSpace(*writeCRDs) != "" {
		bundled, err := platformrelease.BundleCRDs(*crdsDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
			os.Exit(1)
		}
		crdsYAML = bundled
		if out := strings.TrimSpace(*writeCRDs); out != "" {
			if err := os.WriteFile(out, []byte(crdsYAML+"\n"), 0o600); err != nil {
				fmt.Fprintf(os.Stderr, "platformmanifest: write CRDs: %v\n", err)
				os.Exit(1)
			}
		}
	}

	m, err := platformrelease.GenerateManifestForCommit(*version, *commit, *crdChange, crdsYAML)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
		os.Exit(1)
	}
	var release platformrelease.ReleaseWiring
	if path := strings.TrimSpace(*wiringPath); path != "" {
		var err error
		if release, err = platformrelease.LoadWiring(path); err != nil {
			fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
			os.Exit(1)
		}
	}
	platformObjects, err := platformrelease.BundlePlatformObjects(strings.TrimSpace(*k8sDir), release.PlatformObjects)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
		os.Exit(1)
	}
	configMaps, err := platformrelease.BundleConfigMaps(strings.TrimSpace(*k8sDir), release.ConfigMaps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
		os.Exit(1)
	}
	// Third-party image pins come from the same k8s/ Deployment setup renders.
	for _, name := range release.ThirdPartyImages {
		repository, tag, err := platformrelease.K8sDeploymentImage(strings.TrimSpace(*k8sDir), name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
			os.Exit(1)
		}
		m.Components = append(m.Components, platformrelease.ManifestComponent{Name: name, Repository: repository, Tag: tag})
	}
	if err := m.ApplyReleaseExtras(platformrelease.ReleaseExtras{
		PlatformObjects: platformObjects,
		ConfigMaps:      configMaps,
		Wiring:          release.Components,
		ConfigOnly:      release.ConfigOnly,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
		os.Exit(1)
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		fmt.Fprintf(os.Stderr, "platformmanifest: %v\n", err)
		os.Exit(1)
	}
}
