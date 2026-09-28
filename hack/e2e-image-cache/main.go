// Command e2e-image-cache ensures a local Docker image via content-hash GHCR
// reuse (pull on hit, build+optional push on miss). Used by test/e2e/qa-e2e.sh.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"mcp-runtime/internal/cli/setup/assetpath"
	"mcp-runtime/internal/cli/setup/platform/imagecache"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "e2e-image-cache: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: e2e-image-cache hash|ensure|component")
	}
	switch args[0] {
	case "hash":
		return cmdHash(args[1:])
	case "ensure":
		return cmdEnsure(args[1:])
	case "component":
		return cmdComponent(args[1:])
	default:
		return fmt.Errorf("unknown command %q (want hash|ensure|component)", args[0])
	}
}

func cmdHash(args []string) error {
	fs := flag.NewFlagSet("hash", flag.ContinueOnError)
	component := fs.String("component", "", "component name")
	root := fs.String("root", "", "repository root (default: detect)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*component) == "" {
		return fmt.Errorf("--component is required")
	}
	repoRoot, err := resolveRoot(*root)
	if err != nil {
		return err
	}
	hash, err := imagecache.ContentHash(repoRoot, *component)
	if err != nil {
		return err
	}
	fmt.Println(hash)
	return nil
}

func cmdComponent(args []string) error {
	fs := flag.NewFlagSet("component", flag.ContinueOnError)
	image := fs.String("image", "", "local image reference")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*image) == "" {
		return fmt.Errorf("--image is required")
	}
	name := imagecache.ComponentFromLocalImage(*image)
	if name == "" {
		return fmt.Errorf("cannot map image %q to a cache component", *image)
	}
	fmt.Println(name)
	return nil
}

func cmdEnsure(args []string) error {
	fs := flag.NewFlagSet("ensure", flag.ContinueOnError)
	component := fs.String("component", "", "component name (optional if --image maps)")
	image := fs.String("image", "", "local image tag to ensure")
	dockerfile := fs.String("dockerfile", "", "dockerfile path relative to root (optional)")
	contextDir := fs.String("context", ".", "docker build context")
	root := fs.String("root", "", "repository root (default: detect)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*image) == "" {
		return fmt.Errorf("--image is required")
	}
	comp := strings.TrimSpace(*component)
	if comp == "" {
		comp = imagecache.ComponentFromLocalImage(*image)
	}
	if comp == "" {
		return fmt.Errorf("cannot resolve component for image %q; pass --component", *image)
	}
	spec, ok := imagecache.Specs[comp]
	if !ok {
		return fmt.Errorf("unknown component %q", comp)
	}
	repoRoot, err := resolveRoot(*root)
	if err != nil {
		return err
	}

	df := strings.TrimSpace(*dockerfile)
	if df == "" {
		df = spec.Dockerfile
	}
	ctxDir := strings.TrimSpace(*contextDir)
	if ctxDir == "" || ctxDir == "." {
		if spec.Context != "" {
			ctxDir = spec.Context
		} else {
			ctxDir = "."
		}
	}

	if !imagecache.Enabled() {
		return buildLocal(repoRoot, comp, *image, df, ctxDir, spec.UseMake)
	}

	opts := imagecache.OptionsFromEnv()
	opts.Progress = func(msg string) { fmt.Fprintln(os.Stderr, msg) }
	_, err = imagecache.EnsureLocalImage(context.Background(), repoRoot, comp, *image, opts, func() error {
		return buildLocal(repoRoot, comp, *image, df, ctxDir, spec.UseMake)
	})
	return err
}

func buildLocal(repoRoot, component, image, dockerfile, contextDir string, useMake bool) error {
	if useMake || component == "operator" {
		cmd := exec.Command("make", "-f", "Makefile.operator", "docker-build-operator-no-test", "IMG="+image)
		cmd.Dir = repoRoot
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		return cmd.Run()
	}
	df := dockerfile
	if !filepath.IsAbs(df) {
		df = filepath.Join(repoRoot, filepath.FromSlash(df))
	}
	ctx := contextDir
	if !filepath.IsAbs(ctx) {
		ctx = filepath.Join(repoRoot, filepath.FromSlash(ctx))
	}
	fmt.Fprintf(os.Stderr, "[image] building %s\n", image)
	cmd := exec.Command("docker", "build", "-t", image, "-f", df, ctx)
	cmd.Dir = repoRoot
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func resolveRoot(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		abs, err := filepath.Abs(explicit)
		if err != nil {
			return "", err
		}
		return abs, nil
	}
	return assetpath.ResolveRepoRoot()
}
