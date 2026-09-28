// Package imagecache provides content-hash based GHCR reuse for QA E2E /
// setup --test-mode platform images. Unchanged component hashes pull from
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
	// HashPaths are files or directories relative to the repo root whose contents
	// contribute to the content hash. Keep aligned with Dockerfile COPY sets.
	HashPaths []string
}

// Specs maps canonical component names to build/hash inputs.
// Names align with update.builtImageSpecs plus e2e-registry.
var Specs = map[string]Spec{
	"operator": {
		Component:  "operator",
		UseMake:    true,
		Dockerfile: "Dockerfile.operator",
		HashPaths: []string{
			"Dockerfile.operator",
			"Makefile.operator",
			"go.mod",
			"go.sum",
			"api",
			"cmd/operator",
			"internal/operator",
			"pkg",
		},
	},
	"gateway-proxy": {
		Component:  "gateway-proxy",
		Dockerfile: "services/mcp-gateway/Dockerfile",
		Context:    ".",
		HashPaths: []string{
			"services/mcp-gateway/Dockerfile",
			"go.mod",
			"go.sum",
			"api",
			"pkg",
			"services/mcp-gateway",
		},
	},
	"platform-api": {
		Component:  "platform-api",
		Dockerfile: "services/platform-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("platform-api"),
	},
	"runtime-api": {
		Component:  "runtime-api",
		Dockerfile: "services/runtime-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("runtime-api"),
	},
	"analytics-api": {
		Component:  "analytics-api",
		Dockerfile: "services/analytics-api/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("analytics-api"),
	},
	"ingest": {
		Component:  "ingest",
		Dockerfile: "services/ingest/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("ingest"),
	},
	"processor": {
		Component:  "processor",
		Dockerfile: "services/processor/Dockerfile",
		Context:    ".",
		HashPaths:  goServiceHashPaths("processor"),
	},
	"ui": {
		Component:  "ui",
		Dockerfile: "services/ui/Dockerfile",
		Context:    ".",
		HashPaths: []string{
			"services/ui/Dockerfile",
			"go.mod",
			"go.sum",
			"pkg",
			"services/ui",
		},
	},
	"doctor-smoke": {
		Component:  "doctor-smoke",
		Dockerfile: "services/doctor-smoke/Dockerfile",
		Context:    ".",
		HashPaths: []string{
			"services/doctor-smoke/Dockerfile",
			"go.mod",
			"go.sum",
			"pkg",
			"cmd/doctor-smoke",
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
		"api",
		"pkg",
		"services/" + service,
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
