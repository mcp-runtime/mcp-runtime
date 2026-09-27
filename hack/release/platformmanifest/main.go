// Command platformmanifest prints the platform release component manifest
// (platform-manifest.json) for a release version, generated from the CLI's
// component catalog so `mcp-runtime update` and releases stay in sync.
//
//	go run ./hack/release/platformmanifest -version v0.5.0 [-crd-change] [-crds-dir config/crd/bases]
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
	crdChange := flag.Bool("crd-change", false, "mark the release as changing CRDs and embed them for update")
	crdsDir := flag.String("crds-dir", "config/crd/bases", "directory of CRD YAML files to embed when -crd-change is set")
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

	m, err := platformrelease.GenerateManifest(*version, *crdChange, crdsYAML)
	if err != nil {
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
