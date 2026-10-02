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
	if imagecache.LocalLatest() {
		root, err := assetpath.ResolveRepoRoot()
		if err != nil {
			core.Warn(fmt.Sprintf("local image cache disabled (cannot resolve repo root): %v", err))
			return buildFn()
		}
		opts := imagecache.OptionsFromEnv()
		platform, err := resolveSetupImagePlatformClientGo()
		if err != nil {
			return fmt.Errorf("resolve image cache target platform: %w", err)
		}
		opts.Platform = platform
		opts.Progress = func(msg string) { core.Info(msg) }
		_, err = imagecache.EnsureLocalLatest(context.Background(), root, component, localImage, opts, buildFn)
		return err
	}
	if !imagecache.Enabled() {
		return buildFn()
	}
	root, err := assetpath.ResolveRepoRoot()
	if err != nil {
		core.Warn(fmt.Sprintf("image cache disabled (cannot resolve repo root): %v", err))
		return buildFn()
	}
	opts := imagecache.OptionsFromEnv()
	platform, err := resolveSetupImagePlatformClientGo()
	if err != nil {
		return fmt.Errorf("resolve image cache target platform: %w", err)
	}
	opts.Platform = platform
	opts.Progress = func(msg string) { core.Info(msg) }
	_, err = imagecache.EnsureLocalImage(context.Background(), root, component, localImage, opts, buildFn)
	return err
}
