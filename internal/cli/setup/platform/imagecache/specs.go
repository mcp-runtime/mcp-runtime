// Package imagecache provides content-hash based GHCR reuse for QA E2E and
// setup platform images. Unchanged component hashes pull from
// ghcr.io instead of rebuilding.
package imagecache

import (
	"strings"

	"mcp-runtime/internal/platformrelease"
)

// Spec describes how to hash and build one platform component image.
type Spec struct {
	Component  string
	Dockerfile string // relative to repo root; empty when UseMake
	Context    string // relative to repo root; empty means "."
	UseMake    bool
	// GoPackage is the binary package directory relative to the repo root.
	// Its local transitive imports are hashed for the selected target platform.
	GoPackage string
	// HashPaths cover non-Go build inputs relative to the repo root.
	HashPaths []string
}

// Specs maps canonical component names to build/hash inputs.
// Names align with update.builtImageSpecs plus e2e-registry.
var Specs = map[string]Spec{
	"operator": {
		Component:  "operator",
		UseMake:    true,
		GoPackage:  "cmd/operator",
		Dockerfile: "Dockerfile.operator",
		HashPaths: []string{
			"Dockerfile.operator",
			"Makefile.operator",
			"go.mod",
			"go.sum",
		},
	},
	"gateway-proxy": {
		Component:  "gateway-proxy",
		GoPackage:  "services/mcp-gateway",
		Dockerfile: "services/mcp-gateway/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("mcp-gateway"),
	},
	"platform-api": {
		Component:  "platform-api",
		GoPackage:  "services/platform-api",
		Dockerfile: "services/platform-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("platform-api"),
	},
	"runtime-api": {
		Component:  "runtime-api",
		GoPackage:  "services/runtime-api",
		Dockerfile: "services/runtime-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("runtime-api"),
	},
	"analytics-api": {
		Component:  "analytics-api",
		GoPackage:  "services/analytics-api",
		Dockerfile: "services/analytics-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("analytics-api"),
	},
	"ingest": {
		Component:  "ingest",
		GoPackage:  "services/ingest",
		Dockerfile: "services/ingest/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("ingest"),
	},
	"processor": {
		Component:  "processor",
		GoPackage:  "services/processor",
		Dockerfile: "services/processor/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("processor"),
	},
	"ui": {
		Component:  "ui",
		GoPackage:  "services/ui",
		Dockerfile: "services/ui/Dockerfile",
		Context:    ".",
		HashPaths:  append(goServiceHashPaths("ui"), "services/ui/frontend"),
	},
	"doctor-smoke": {
		Component:  "doctor-smoke",
		GoPackage:  "cmd/doctor-smoke",
		Dockerfile: "services/doctor-smoke/Dockerfile",
		Context:    ".",
		HashPaths: []string{
			"services/doctor-smoke/Dockerfile",
			"go.mod",
			"go.sum",
		},
	},
	"e2e-registry": {
		Component:  "e2e-registry",
		Dockerfile: "test/e2e/registry.Dockerfile",
		Context:    ".",
		HashPaths: []string{
			"test/e2e/registry.Dockerfile",
		},
	},
}

func goServiceHashPaths(service string) []string {
	return []string{
		"services/" + service + "/Dockerfile",
		"go.mod",
		"go.sum",
		"services/" + service + "/go.mod",
		"services/" + service + "/go.sum",
	}
}

// ComponentFromLocalImage maps a local docker tag used by qa-e2e/setup to a Spec name.
func ComponentFromLocalImage(image string) string {
	ref, err := platformrelease.ParseImageRef(image)
	if err != nil {
		return ""
	}
	name := ref.Path
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		name = name[i+1:]
	}
	switch name {
	case "mcp-runtime-operator":
		return "operator"
	case "mcp-runtime-registry":
		return "e2e-registry"
	case "mcp-sentinel-mcp-gateway":
		return "gateway-proxy"
	case "mcp-sentinel-ingest":
		return "ingest"
	case "mcp-platform-api":
		return "platform-api"
	case "mcp-runtime-api":
		return "runtime-api"
	case "mcp-analytics-api":
		return "analytics-api"
	case "mcp-sentinel-processor":
		return "processor"
	case "mcp-sentinel-ui":
		return "ui"
	case "mcp-runtime-doctor-smoke":
		return "doctor-smoke"
	default:
		return ""
	}
}
