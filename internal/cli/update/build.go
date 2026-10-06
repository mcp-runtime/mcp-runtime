package update

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"mcp-runtime/internal/cli/core"
	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/internal/platformrelease"
)

const (
	ImageActionBuild = "build"
	ImageActionReuse = "reuse"

	defaultUpdateImagePlatform = "linux/amd64"
	// Sequential by default: concurrent docker builds of Go services have
	// crashed the compiler under load (fatal error: fault). Opt in with
	// --build-parallelism.
	defaultBuildParallelism = 1
	defaultBuildRetries     = 1
)

// imageBuildSpec describes how to build one Built component image from source.
type imageBuildSpec struct {
	Component  string
	Dockerfile string // relative to source; empty for operator (make)
	Context    string // relative to source; empty for operator
	UseMake    bool
}

// builtImageSpecs maps catalog Built component names to Docker/make build inputs.
// Aligns with setup's analyticsComponents + operator/gateway paths.
var builtImageSpecs = map[string]imageBuildSpec{
	"operator":      {Component: "operator", UseMake: true},
	"gateway-proxy": {Component: "gateway-proxy", Dockerfile: "services/mcp-gateway/Dockerfile", Context: "."},
	"platform-api":  {Component: "platform-api", Dockerfile: "services/platform-api/Dockerfile", Context: "."},
	"runtime-api":   {Component: "runtime-api", Dockerfile: "services/runtime-api/Dockerfile", Context: "."},
	"analytics-api": {Component: "analytics-api", Dockerfile: "services/analytics-api/Dockerfile", Context: "."},
	"ingest":        {Component: "ingest", Dockerfile: "services/ingest/Dockerfile", Context: "."},
	"processor":     {Component: "processor", Dockerfile: "services/processor/Dockerfile", Context: "."},
	"ui":            {Component: "ui", Dockerfile: "services/ui/Dockerfile", Context: "."},
	"doctor-smoke":  {Component: "doctor-smoke", Dockerfile: "services/doctor-smoke/Dockerfile", Context: "."},
}

// ImageBuildAction is one unique image the update may build or reuse.
type ImageBuildAction struct {
	Component string `json:"component"`
	Image     string `json:"image"`
	Action    string `json:"action"` // build | reuse
	Reason    string `json:"reason,omitempty"`
}

// BuildOptions controls optional local image build/push during update.
type BuildOptions struct {
	Enabled       bool
	Source        string
	ImagePlatform string
	Parallelism   int
	Retries       int // extra attempts after the first failure (default 1)
	// SkipRegistryProbe forces every Built candidate to Action=build without
	// contacting the registry (used for --dry-run without Docker).
	SkipRegistryProbe bool
	// Progress receives human-readable lines (may be nil).
	Progress func(string)
	// Out receives docker/make stdout/stderr (may be nil).
	Out io.Writer

	// Injectable for tests.
	RegistryHasImage func(ctx context.Context, image string) (bool, error)
	BuildImage       func(ctx context.Context, source, platform string, spec imageBuildSpec, image string) error
	PushImage        func(ctx context.Context, image string) error
}

func resolveImagePlatform(explicit string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		return p
	}
	if p := strings.TrimSpace(os.Getenv("MCP_IMAGE_PLATFORM")); p != "" {
		return p
	}
	return defaultUpdateImagePlatform
}

func resolveSourceDir(source string) (string, error) {
	src := strings.TrimSpace(source)
	if src == "" || src == "." {
		root, err := assetpath.ResolveRepoRoot()
		if err != nil {
			return "", fmt.Errorf("resolve repository root from working directory: %w", err)
		}
		return root, nil
	}
	abs, err := filepath.Abs(src)
	if err != nil {
		return "", err
	}
	if !assetpath.IsRepoRoot(abs) {
		return "", fmt.Errorf("source %q is not an mcp-runtime repository root (need go.mod, services/, k8s/)", abs)
	}
	return abs, nil
}

// tagOnlyImage strips a digest so docker build/push and registry probes use repo:tag.
func tagOnlyImage(image string) (string, error) {
	ref, err := platformrelease.ParseImageRef(image)
	if err != nil {
		return "", err
	}
	if ref.Tag == "" {
		return "", fmt.Errorf("image %q has no tag to build", image)
	}
	ref.Digest = ""
	return ref.String(), nil
}

// rewriteBuiltTargetsToTags sets TargetImage to repo:tag for Built rows when
// --build is used, so Apply never pins a release digest that the local build
// did not produce.
func rewriteBuiltTargetsToTags(plan *Plan) error {
	if plan == nil {
		return nil
	}
	for i := range plan.Rows {
		row := &plan.Rows[i]
		if row.Action != ActionUpdate || !row.component.Built {
			continue
		}
		tagOnly, err := tagOnlyImage(row.TargetImage)
		if err != nil {
			return fmt.Errorf("%s: %w", row.Component, err)
		}
		row.TargetImage = tagOnly
	}
	return nil
}

// planImageBuilds lists unique Built-component images from plan.Changed().
// Every component the release changes is included. Commit-tagged release
// images are always rebuilt from their source revision; other tags are reused
// when already present in the registry.
func planImageBuilds(ctx context.Context, plan *Plan, opts BuildOptions) ([]ImageBuildAction, error) {
	if plan == nil {
		return nil, nil
	}
	probe := opts.RegistryHasImage
	if probe == nil {
		probe = dockerManifestExists
	}
	seen := map[string]bool{}
	var out []ImageBuildAction
	for _, row := range plan.Changed() {
		if !row.component.Built {
			continue
		}
		if _, ok := builtImageSpecs[row.Component]; !ok {
			return nil, fmt.Errorf("component %q is Built but has no Dockerfile/make mapping for --build", row.Component)
		}
		image, err := tagOnlyImage(row.TargetImage)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", row.Component, err)
		}
		if seen[image] {
			continue
		}
		seen[image] = true
		action := ImageBuildAction{Component: row.Component, Image: image, Action: ImageActionBuild, Reason: "target tag missing from registry"}
		ref, err := platformrelease.ParseImageRef(image)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", row.Component, err)
		}
		if _, ok := platformrelease.ReleaseCommitFromImageTag(ref.Tag); ok {
			action.Reason = "release image requires build from its source commit"
			out = append(out, action)
			continue
		}
		if opts.SkipRegistryProbe {
			action.Reason = "registry not checked (dry-run)"
			out = append(out, action)
			continue
		}
		exists, err := probe(ctx, image)
		if err != nil {
			return nil, fmt.Errorf("check registry for %s: %w", image, err)
		}
		if exists {
			action.Action = ImageActionReuse
			action.Reason = "target tag already in registry"
		}
		out = append(out, action)
	}
	return out, nil
}

// buildAndPushChanged builds and pushes only images marked Action=build.
// Commit-tagged release images require the exact clean source checkout.
func buildAndPushChanged(ctx context.Context, actions []ImageBuildAction, opts BuildOptions) error {
	if !opts.Enabled {
		return nil
	}
	source, err := resolveSourceDir(opts.Source)
	if err != nil {
		return core.WrapWithBase(core.ErrUpdateBuildFailed, err, err.Error())
	}
	platform := resolveImagePlatform(opts.ImagePlatform)
	progress := opts.Progress
	if progress == nil {
		progress = func(string) {}
	}
	var progressMu sync.Mutex
	safeProgress := func(msg string) {
		progressMu.Lock()
		defer progressMu.Unlock()
		progress(msg)
	}
	out := opts.Out
	if out == nil {
		out = io.Discard
	}
	buildFn := opts.BuildImage
	if buildFn == nil {
		buildFn = func(ctx context.Context, source, platform string, spec imageBuildSpec, image string) error {
			return buildComponentImage(ctx, source, platform, spec, image, out)
		}
	}
	pushFn := opts.PushImage
	if pushFn == nil {
		pushFn = func(ctx context.Context, image string) error {
			return dockerPush(ctx, image, out)
		}
	}
	parallel := opts.Parallelism
	if parallel <= 0 {
		parallel = defaultBuildParallelism
	}
	retries := defaultBuildRetries
	if opts.Retries > 0 {
		retries = opts.Retries
	}

	var toBuild []ImageBuildAction
	for _, a := range actions {
		if a.Action == ImageActionBuild {
			toBuild = append(toBuild, a)
		} else {
			safeProgress(fmt.Sprintf("Reusing image %s (%s)", a.Image, a.Component))
		}
	}
	if len(toBuild) == 0 {
		return nil
	}
	var releaseCommit string
	for _, a := range toBuild {
		ref, err := platformrelease.ParseImageRef(a.Image)
		if err != nil {
			return core.WrapWithBase(core.ErrUpdateBuildFailed, err, err.Error())
		}
		commit, ok := platformrelease.ReleaseCommitFromImageTag(ref.Tag)
		if !ok {
			continue
		}
		if releaseCommit != "" && releaseCommit != commit {
			return core.NewWithBase(core.ErrUpdateBuildFailed, "release manifest names images from different source commits")
		}
		releaseCommit = commit
	}
	if releaseCommit != "" {
		if err := verifyReleaseSource(ctx, source, releaseCommit); err != nil {
			return core.WrapWithBase(core.ErrUpdateBuildFailed, err, err.Error())
		}
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	var (
		errMu     sync.Mutex
		firstErr  error
		published []string
	)
	recordErr := func(err error) {
		errMu.Lock()
		defer errMu.Unlock()
		if firstErr == nil {
			firstErr = err
			cancel()
		}
	}
	recordPublished := func(image string) {
		errMu.Lock()
		defer errMu.Unlock()
		published = append(published, image)
	}

	for _, a := range toBuild {
		a := a
		spec := builtImageSpecs[a.Component]
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			var buildErr error
			attempts := 1 + retries
			for attempt := 1; attempt <= attempts; attempt++ {
				if ctx.Err() != nil {
					return
				}
				if attempt == 1 {
					safeProgress(fmt.Sprintf("Building image %s (%s)", a.Image, a.Component))
				} else {
					safeProgress(fmt.Sprintf("Retrying build %s (%s) attempt %d/%d", a.Image, a.Component, attempt, attempts))
				}
				buildErr = buildFn(ctx, source, platform, spec, a.Image)
				if buildErr == nil {
					break
				}
			}
			if buildErr != nil {
				recordErr(fmt.Errorf("build %s (%s): %w", a.Component, a.Image, buildErr))
				return
			}
			if ctx.Err() != nil {
				return
			}
			safeProgress(fmt.Sprintf("Pushing image %s (%s)", a.Image, a.Component))
			if err := pushFn(ctx, a.Image); err != nil {
				recordErr(fmt.Errorf("push %s (%s): %w", a.Component, a.Image, err))
				return
			}
			recordPublished(a.Image)
			safeProgress(fmt.Sprintf("Published image %s (%s)", a.Image, a.Component))
		}()
	}
	wg.Wait()
	if firstErr != nil {
		msg := firstErr.Error()
		if len(published) > 0 {
			msg = fmt.Sprintf("%s; already published (re-run --build to reuse): %s", msg, strings.Join(published, ", "))
		}
		return core.WrapWithBase(core.ErrUpdateBuildFailed, firstErr, msg)
	}
	return nil
}

func verifyReleaseSource(ctx context.Context, source, commit string) error {
	cmd := exec.CommandContext(ctx, "git", "-C", source, "rev-parse", "HEAD")
	got, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read release source commit: %w", err)
	}
	if strings.TrimSpace(string(got)) != commit {
		return fmt.Errorf("release image requires source commit %s; checkout is %s", commit, strings.TrimSpace(string(got)))
	}
	cmd = exec.CommandContext(ctx, "git", "-C", source, "status", "--porcelain", "--untracked-files=normal")
	status, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("check release source cleanliness: %w", err)
	}
	if len(status) != 0 {
		return fmt.Errorf("release source checkout has local changes; use a clean checkout of %s", commit)
	}
	return nil
}

func buildComponentImage(ctx context.Context, source, platform string, spec imageBuildSpec, image string, out io.Writer) error {
	if spec.UseMake {
		return runValidated(ctx, source, "make", []string{
			"-f", "Makefile.operator",
			"docker-build-operator-no-test",
			"IMG=" + image,
			"DOCKER_PLATFORM=" + platform,
		}, out)
	}
	dockerfile := filepath.Join(source, filepath.FromSlash(spec.Dockerfile))
	contextDir := source
	if spec.Context != "" && spec.Context != "." {
		contextDir = filepath.Join(source, filepath.FromSlash(spec.Context))
	}
	if _, err := os.Stat(dockerfile); err != nil {
		return fmt.Errorf("dockerfile %s: %w", dockerfile, err)
	}
	return runValidated(ctx, source, "docker", []string{
		"build",
		"--platform", platform,
		"-f", dockerfile,
		"-t", image,
		contextDir,
	}, out)
}

func dockerPush(ctx context.Context, image string, out io.Writer) error {
	return runValidated(ctx, "", "docker", []string{"push", image}, out)
}

func dockerManifestExists(ctx context.Context, image string) (bool, error) {
	if err := validateExec("docker", []string{"manifest", "inspect", image}); err != nil {
		return false, err
	}
	// #nosec G204 -- args validated by validateExec allowlist and NoShellMeta.
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

func isRegistryNotFound(text string) bool {
	for _, needle := range []string{
		"manifest unknown",
		"not found",
		"no such manifest",
		"does not exist",
		"name unknown",
		"404",
	} {
		if strings.Contains(text, needle) {
			return true
		}
	}
	return false
}

func validateExec(name string, args []string) error {
	spec := core.ExecSpec{Name: name, Args: args}
	for _, v := range []core.ExecValidator{
		core.AllowlistBins("docker", "make"),
		core.NoShellMeta(),
		core.NoControlChars(),
	} {
		if err := v(spec); err != nil {
			return err
		}
	}
	return nil
}

func runValidated(ctx context.Context, dir, name string, args []string, out io.Writer) error {
	if err := validateExec(name, args); err != nil {
		return err
	}
	// #nosec G204 -- args validated by validateExec allowlist and NoShellMeta.
	cmd := exec.CommandContext(ctx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Stdout = out
	cmd.Stderr = out
	return cmd.Run()
}
