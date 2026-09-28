package platform

import (
	"context"
	"fmt"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/internal/cli/setup/platform/imagecache"
)

// ensureImageViaCache runs buildFn unless content-hash GHCR caching is enabled
// and the component hash already exists remotely (pull + retag to localImage).
// On miss it builds, then optionally pushes the hash tag to GHCR.
func ensureImageViaCache(component, localImage string, buildFn func() error) error {
	if !imagecache.Enabled() {
		return buildFn()
	}
	root, err := assetpath.ResolveRepoRoot()
	if err != nil {
		core.Warn(fmt.Sprintf("image cache disabled (cannot resolve repo root): %v", err))
		return buildFn()
	}
	opts := imagecache.OptionsFromEnv()
	opts.Progress = func(msg string) { core.Info(msg) }
	_, err = imagecache.EnsureLocalImage(context.Background(), root, component, localImage, opts, buildFn)
	return err
}
