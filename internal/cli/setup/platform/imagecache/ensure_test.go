package imagecache

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestContentHashStableAndSensitive(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "services", "ingest", "Dockerfile"), "FROM scratch\n")
	mustWrite(t, filepath.Join(root, "go.mod"), "module example\n")
	mustWrite(t, filepath.Join(root, "go.sum"), "")
	mustWrite(t, filepath.Join(root, "api", "doc.go"), "package api\n")
	mustWrite(t, filepath.Join(root, "pkg", "used", "used.go"), "package used\n")
	mustWrite(t, filepath.Join(root, "pkg", "unused", "unused.go"), "package unused\n")
	mustWrite(t, filepath.Join(root, "services", "ingest", "main.go"), "package main\nimport _ \"example/pkg/used\"\n")
	mustWrite(t, filepath.Join(root, "test", "e2e", "registry.Dockerfile"), "FROM registry:2.8.3\n")

	h1, err := ContentHash(root, "ingest")
	if err != nil {
		t.Fatalf("ContentHash: %v", err)
	}
	h2, err := ContentHash(root, "ingest")
	if err != nil {
		t.Fatalf("ContentHash second: %v", err)
	}
	if h1 != h2 {
		t.Fatalf("hash not stable: %q vs %q", h1, h2)
	}
	if len(h1) != hashHexLen {
		t.Fatalf("hash length %d, want %d", len(h1), hashHexLen)
	}
	mustWrite(t, filepath.Join(root, "pkg", "unused", "unused.go"), "package unused\n// patchset changes another package\n")
	if got, err := ContentHash(root, "ingest"); err != nil || got != h1 {
		t.Fatalf("unrelated package changed ingest hash: got %q, err %v, want %q", got, err, h1)
	}
	mustWrite(t, filepath.Join(root, "pkg", "used", "used.go"), "package used\n// patchset changes an import\n")
	hUsed, err := ContentHash(root, "ingest")
	if err != nil || hUsed == h1 {
		t.Fatalf("imported package did not change ingest hash: got %q, err %v", hUsed, err)
	}
	registryHash, err := ContentHash(root, "e2e-registry")
	if err != nil {
		t.Fatalf("registry ContentHash: %v", err)
	}

	mustWrite(t, filepath.Join(root, "services", "ingest", "main.go"), "package main\n// change\n")
	h3, err := ContentHash(root, "ingest")
	if err != nil {
		t.Fatalf("ContentHash after change: %v", err)
	}
	if h3 == hUsed {
		t.Fatalf("hash did not change after source edit")
	}
	if got, err := ContentHash(root, "e2e-registry"); err != nil || got != registryHash {
		t.Fatalf("unrelated registry hash changed: got %q, err %v, want %q", got, err, registryHash)
	}

	mustWrite(t, filepath.Join(root, ".dockerignore"), "pkg/generated/\n")
	h4, err := ContentHash(root, "ingest")
	if err != nil {
		t.Fatalf("ContentHash after .dockerignore change: %v", err)
	}
	if h4 == h3 {
		t.Fatal("hash did not change after .dockerignore change")
	}
	if got, err := ContentHash(root, "e2e-registry"); err != nil || got == registryHash {
		t.Fatalf("shared .dockerignore change did not invalidate registry: got %q, err %v", got, err)
	}
}

func TestContentHashTracksTargetArchitecture(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "go.mod"), "module example\n")
	mustWrite(t, filepath.Join(root, "services", "ingest", "Dockerfile"), "FROM scratch\n")
	mustWrite(t, filepath.Join(root, "services", "ingest", "main.go"), "package main\n")
	mustWrite(t, filepath.Join(root, "services", "ingest", "amd64.go"), "//go:build amd64\n\npackage main\n")
	mustWrite(t, filepath.Join(root, "services", "ingest", "arm64.go"), "//go:build arm64\n\npackage main\n")
	amdBefore, err := ContentHashForPlatform(root, "ingest", "linux/amd64")
	if err != nil {
		t.Fatal(err)
	}
	armBefore, err := ContentHashForPlatform(root, "ingest", "linux/arm64")
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "services", "ingest", "amd64.go"), "//go:build amd64\n\npackage main\n// changed\n")
	amdAfter, err := ContentHashForPlatform(root, "ingest", "linux/amd64")
	if err != nil || amdAfter == amdBefore {
		t.Fatalf("amd64 source change did not invalidate amd64: %q, %v", amdAfter, err)
	}
	armAfter, err := ContentHashForPlatform(root, "ingest", "linux/arm64")
	if err != nil || armAfter != armBefore {
		t.Fatalf("amd64 source change invalidated arm64: %q, %v", armAfter, err)
	}
}

func TestComponentFromLocalImage(t *testing.T) {
	cases := map[string]string{
		"docker.io/library/mcp-runtime-operator:latest": "operator",
		"mcp-gateway:latest":                            "gateway-proxy",
		"mcp-platform-api:latest":                       "platform-api",
		"registry.local/mcp-ui:dev":                     "ui",
		"mcp-runtime-registry:latest":                   "e2e-registry",
		"unknown:latest":                                "",
	}
	for image, want := range cases {
		if got := ComponentFromLocalImage(image); got != want {
			t.Fatalf("ComponentFromLocalImage(%q)=%q want %q", image, got, want)
		}
	}
}

func TestEnsureLocalImageReuseAndBuild(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "test", "e2e", "registry.Dockerfile"), "FROM registry:2.8.3\n")

	var pulled, tagged, pushed []string
	var builds int
	opts := Options{
		Registry: "ghcr.io/test/mcp-runtime",
		Platform: "linux/amd64",
		Push:     true,
		ManifestExists: func(_ context.Context, image string) (bool, error) {
			return true, nil
		},
		Pull: func(_ context.Context, image string) error {
			pulled = append(pulled, image)
			return nil
		},
		Tag: func(_ context.Context, source, target string) error {
			tagged = append(tagged, source+"->"+target)
			return nil
		},
		PushImage: func(_ context.Context, image string) error {
			pushed = append(pushed, image)
			return nil
		},
	}

	res, err := EnsureLocalImage(context.Background(), root, "e2e-registry", "mcp-runtime-registry:latest", opts, func() error {
		builds++
		return nil
	})
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if res.Action != ActionReuse {
		t.Fatalf("action=%q want reuse", res.Action)
	}
	if builds != 0 {
		t.Fatalf("build called on cache hit")
	}
	if len(pulled) != 1 || len(pushed) != 0 {
		t.Fatalf("pulled=%v pushed=%v", pulled, pushed)
	}
	if res.CacheRef != "ghcr.io/test/mcp-runtime/e2e-registry:"+res.Hash+"-amd64" {
		t.Fatalf("cache reference %q does not include target architecture", res.CacheRef)
	}
	if armRef := CacheRef(opts.Registry, "e2e-registry", res.Hash, "linux/arm64"); armRef == res.CacheRef {
		t.Fatal("different target architectures share a cache reference")
	}

	opts.ManifestExists = func(_ context.Context, image string) (bool, error) {
		return false, nil
	}
	res, err = EnsureLocalImage(context.Background(), root, "e2e-registry", "mcp-runtime-registry:latest", opts, func() error {
		builds++
		return nil
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if res.Action != ActionBuild {
		t.Fatalf("action=%q want build", res.Action)
	}
	if builds != 1 {
		t.Fatalf("builds=%d want 1", builds)
	}
	if len(pushed) != 1 {
		t.Fatalf("expected push on miss, got %v", pushed)
	}
}

func TestEnsureLocalImageRejectsInvalidReferencesBeforeCallingDocker(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "test", "e2e", "registry.Dockerfile"), "FROM registry:2.8.3\n")
	for _, image := range []string{"-v", "mcp-runtime-registry:latest;echo bad", " mcp-runtime-registry:latest"} {
		t.Run(image, func(t *testing.T) {
			called := false
			_, err := EnsureLocalImage(context.Background(), root, "e2e-registry", image, Options{
				Platform: "linux/amd64",
				ManifestExists: func(context.Context, string) (bool, error) {
					called = true
					return false, nil
				},
			}, func() error { called = true; return nil })
			if err == nil {
				t.Fatal("expected invalid image reference to fail")
			}
			if called {
				t.Fatal("docker/build callback called for invalid image reference")
			}
		})
	}
}

func TestEnabledAndRegistry(t *testing.T) {
	t.Setenv(envImageCache, "")
	t.Setenv(envSetupImageCache, "")
	if Enabled() {
		t.Fatal("expected disabled by default")
	}
	t.Setenv(envSetupImageCache, "1")
	if !Enabled() {
		t.Fatal("expected enabled with MCP_SETUP_IMAGE_CACHE=1")
	}
	t.Setenv(envImageCache, "0")
	if Enabled() {
		t.Fatal("E2E_IMAGE_CACHE=0 should disable")
	}
	t.Setenv(envImageCache, "1")
	t.Setenv(envSetupImageCache, "")
	if !Enabled() {
		t.Fatal("expected enabled with E2E_IMAGE_CACHE=1")
	}
	t.Setenv(envImageCache, "local")
	if Enabled() {
		t.Fatal("E2E_IMAGE_CACHE=local must not enable GHCR")
	}
	if !LocalLatest() {
		t.Fatal("expected local latest mode")
	}

	t.Setenv(envImageCacheRegistry, "")
	t.Setenv(envRepoOwner, "acme")
	if got := DefaultRegistry(); got != "ghcr.io/acme/mcp-runtime" {
		t.Fatalf("DefaultRegistry=%q", got)
	}
	t.Setenv(envImageCacheRegistry, "ghcr.io/acme/cache/")
	if got := DefaultRegistry(); got != "ghcr.io/acme/cache" {
		t.Fatalf("override DefaultRegistry=%q", got)
	}
}

func TestEnsureLocalLatestKeepsOneImage(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "test", "e2e", "registry.Dockerfile"), "FROM registry:2.8.3\n")

	var builds, stamps, prunes int
	labels := map[string]string{}
	opts := Options{
		Platform: "linux/amd64",
		InspectLabel: func(_ context.Context, image, label string) (string, bool, error) {
			value, ok := labels[image+"\x00"+label]
			return value, ok, nil
		},
		StampLabel: func(_ context.Context, image, label, value string) error {
			stamps++
			labels[image+"\x00"+label] = value
			return nil
		},
		PruneDangling: func(context.Context) error {
			prunes++
			return nil
		},
	}

	res, err := EnsureLocalLatest(context.Background(), root, "e2e-registry", "mcp-runtime-registry:latest", opts, func() error {
		builds++
		return nil
	})
	if err != nil {
		t.Fatalf("miss: %v", err)
	}
	if res.Action != ActionBuild || builds != 1 || stamps != 1 || prunes != 1 {
		t.Fatalf("miss action=%s builds=%d stamps=%d prunes=%d", res.Action, builds, stamps, prunes)
	}
	if labels["mcp-runtime-registry:latest\x00"+ContentHashLabel] != res.Hash {
		t.Fatalf("stamped hash %q, want %q", labels["mcp-runtime-registry:latest\x00"+ContentHashLabel], res.Hash)
	}

	res, err = EnsureLocalLatest(context.Background(), root, "e2e-registry", "mcp-runtime-registry:latest", opts, func() error {
		builds++
		return nil
	})
	if err != nil {
		t.Fatalf("hit: %v", err)
	}
	if res.Action != ActionReuse || builds != 1 || stamps != 1 {
		t.Fatalf("hit action=%s builds=%d stamps=%d", res.Action, builds, stamps)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
