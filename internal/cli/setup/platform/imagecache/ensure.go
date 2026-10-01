package imagecache

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"mcp-runtime/internal/platformrelease"
)

const (
	ActionReuse = "reuse"
	ActionBuild = "build"

	envImageCache         = "E2E_IMAGE_CACHE"
	envSetupImageCache    = "MCP_SETUP_IMAGE_CACHE"
	envGHCRPush           = "E2E_GHCR_PUSH"
	envImageCacheRegistry = "E2E_IMAGE_CACHE_REGISTRY"
	envRepoOwner          = "GITHUB_REPOSITORY_OWNER"
)

// Options controls GHCR pull/push behavior for EnsureLocalImage.
type Options struct {
	// Registry is the GHCR prefix without trailing slash, e.g.
	// ghcr.io/mcp-runtime/mcp-runtime. Empty uses DefaultRegistry().
	Registry string
	// Platform is the architecture of the image being built or reused.
	Platform string
	// Push enables pushing newly built images to GHCR.
	Push bool
	// Progress receives human-readable status lines (may be nil).
	Progress func(string)
	// Out receives docker command output (may be nil).
	Out io.Writer

	// Injectable for tests.
	ManifestExists func(ctx context.Context, image string) (bool, error)
	Pull           func(ctx context.Context, image string) error
	Tag            func(ctx context.Context, source, target string) error
	PushImage      func(ctx context.Context, image string) error
}

// Result describes whether EnsureLocalImage reused or built.
type Result struct {
	Action     string
	Hash       string
	CacheRef   string
	LocalImage string
}

// Enabled reports whether content-hash GHCR caching is turned on.
// Opt in with E2E_IMAGE_CACHE=1 or MCP_SETUP_IMAGE_CACHE=1.
// E2E_IMAGE_CACHE=0 disables even when setup cache is set.
func Enabled() bool {
	if strings.TrimSpace(os.Getenv(envImageCache)) == "0" {
		return false
	}
	return strings.TrimSpace(os.Getenv(envImageCache)) == "1" ||
		strings.TrimSpace(os.Getenv(envSetupImageCache)) == "1"
}

// PushEnabled reports whether newly built images should be pushed to GHCR.
func PushEnabled() bool {
	return strings.TrimSpace(os.Getenv(envGHCRPush)) == "1"
}

// DefaultRegistry returns ghcr.io/<owner>/mcp-runtime.
func DefaultRegistry() string {
	if v := strings.TrimSpace(os.Getenv(envImageCacheRegistry)); v != "" {
		return strings.TrimRight(v, "/")
	}
	owner := strings.TrimSpace(os.Getenv(envRepoOwner))
	if owner == "" {
		owner = "mcp-runtime"
	}
	return fmt.Sprintf("ghcr.io/%s/mcp-runtime", owner)
}

// CacheRef builds ghcr.io/.../<component>:<hash>-<architecture>.
func CacheRef(registry, component, hash, platform string) string {
	reg := strings.TrimRight(strings.TrimSpace(registry), "/")
	if reg == "" {
		reg = DefaultRegistry()
	}
	return fmt.Sprintf("%s/%s:%s-%s", reg, component, hash, strings.TrimPrefix(platform, "linux/"))
}

// OptionsFromEnv fills Registry and Push from environment defaults.
func OptionsFromEnv() Options {
	platform := strings.TrimSpace(os.Getenv("MCP_IMAGE_PLATFORM"))
	if platform == "" {
		platform = strings.TrimSpace(os.Getenv("DOCKER_DEFAULT_PLATFORM"))
	}
	if platform == "" {
		platform = "linux/" + runtime.GOARCH
	}
	return Options{
		Registry: DefaultRegistry(),
		Platform: platform,
		Push:     PushEnabled(),
	}
}

// EnsureLocalImage makes localImage available: on GHCR hash hit pull+retag;
// otherwise run buildFn then optionally push the hash-tagged ref to GHCR.
func EnsureLocalImage(ctx context.Context, repoRoot, component, localImage string, opts Options, buildFn func() error) (Result, error) {
	if opts.Platform != "linux/amd64" && opts.Platform != "linux/arm64" {
		return Result{}, fmt.Errorf("unsupported image cache platform %q", opts.Platform)
	}
	if _, ok := Specs[component]; !ok {
		return Result{}, fmt.Errorf("unknown image cache component %q", component)
	}
	if err := validateImageReference("local image", localImage); err != nil {
		return Result{}, err
	}
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	hash, err := ContentHashForPlatform(repoRoot, component, opts.Platform)
	if err != nil {
		return Result{}, err
	}
	registry := opts.Registry
	if registry == "" {
		registry = DefaultRegistry()
	}
	cacheRef := CacheRef(registry, component, hash, opts.Platform)
	if err := validateImageReference("cache image", cacheRef); err != nil {
		return Result{}, err
	}
	result := Result{Hash: hash, CacheRef: cacheRef, LocalImage: localImage}

	existsFn := opts.ManifestExists
	if existsFn == nil {
		existsFn = dockerManifestExists
	}
	pullFn := opts.Pull
	if pullFn == nil {
		pullFn = dockerPull
	}
	tagFn := opts.Tag
	if tagFn == nil {
		tagFn = dockerTag
	}
	pushFn := opts.PushImage
	if pushFn == nil {
		pushFn = dockerPush
	}

	exists, err := existsFn(ctx, cacheRef)
	if err != nil {
		progress(fmt.Sprintf("[image] cache probe failed for %s (%s): %v; building", component, hash, err))
	} else if exists {
		progress(fmt.Sprintf("[image] cache hit %s %s", component, hash))
		if err := pullFn(ctx, cacheRef); err != nil {
			return Result{}, fmt.Errorf("pull cached image %s: %w", cacheRef, err)
		}
		if err := tagFn(ctx, cacheRef, localImage); err != nil {
			return Result{}, fmt.Errorf("tag %s -> %s: %w", cacheRef, localImage, err)
		}
		result.Action = ActionReuse
		return result, nil
	}

	progress(fmt.Sprintf("[image] cache miss %s %s; building", component, hash))
	if buildFn == nil {
		return Result{}, fmt.Errorf("build function required for cache miss on %s", component)
	}
	if err := buildFn(); err != nil {
		return Result{}, err
	}
	if err := tagFn(ctx, localImage, cacheRef); err != nil {
		return Result{}, fmt.Errorf("tag %s -> %s: %w", localImage, cacheRef, err)
	}
	if opts.Push {
		progress(fmt.Sprintf("[image] pushing cache %s", cacheRef))
		if err := pushFn(ctx, cacheRef); err != nil {
			// Push failure must not block Kind publish; warn via progress.
			progress(fmt.Sprintf("[image] cache push failed for %s: %v (continuing)", cacheRef, err))
		}
	}
	result.Action = ActionBuild
	return result, nil
}

func dockerManifestExists(ctx context.Context, image string) (bool, error) {
	// #nosec G204 -- image references are parsed and validated before reaching Docker; no shell is used.
	cmd := exec.CommandContext(ctx, "docker", "manifest", "inspect", image)
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	text := strings.ToLower(string(output) + err.Error())
	if isRegistryNotFound(text) {
		return false, nil
	}
	return false, fmt.Errorf("docker manifest inspect %s: %w\n%s", image, err, strings.TrimSpace(string(output)))
}

func dockerPull(ctx context.Context, image string) error {
	// #nosec G204 -- image references are parsed and validated before reaching Docker; no shell is used.
	cmd := exec.CommandContext(ctx, "docker", "pull", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker pull %s: %w\n%s", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dockerTag(ctx context.Context, source, target string) error {
	// #nosec G204 -- both image references are parsed and validated before reaching Docker; no shell is used.
	cmd := exec.CommandContext(ctx, "docker", "tag", source, target)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker tag %s %s: %w\n%s", source, target, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func dockerPush(ctx context.Context, image string) error {
	// #nosec G204 -- image references are parsed and validated before reaching Docker; no shell is used.
	cmd := exec.CommandContext(ctx, "docker", "push", image)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("docker push %s: %w\n%s", image, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func validateImageReference(label, image string) error {
	ref, err := platformrelease.ParseImageRef(image)
	if err != nil || ref.String() != image {
		if err == nil {
			err = fmt.Errorf("reference is not canonical")
		}
		return fmt.Errorf("invalid %s %q: %w", label, image, err)
	}
	return nil
}

func isRegistryNotFound(text string) bool {
	for _, needle := range []string{
		"manifest unknown",
		"not found",
		"no such manifest",
		"does not exist",
		"name unknown",
		"401",
		"403",
		"404",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}
