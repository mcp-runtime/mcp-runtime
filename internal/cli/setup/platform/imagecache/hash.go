package imagecache

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

const hashHexLen = 16

// ContentHash returns the target platform's source hash using environment
// defaults for the platform. Use ContentHashForPlatform when the target is known.
func ContentHash(repoRoot, component string) (string, error) {
	return ContentHashForPlatform(repoRoot, component, OptionsFromEnv().Platform)
}

// ContentHashForPlatform hashes the source files compiled into a component for
// the requested platform, plus its Docker and module inputs.
func ContentHashForPlatform(repoRoot, component, platform string) (string, error) {
	spec, ok := Specs[component]
	if !ok {
		return "", fmt.Errorf("unknown image cache component %q", component)
	}
	if platform != "linux/amd64" && platform != "linux/arm64" {
		return "", fmt.Errorf("unsupported image cache platform %q", platform)
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	paths := append(append([]string{}, spec.HashPaths...), ".dockerignore")
	if spec.GoPackage != "" {
		goFiles, err := goDependencyFiles(root, spec.GoPackage, platform)
		if err != nil {
			return "", fmt.Errorf("resolve %s Go dependencies: %w", component, err)
		}
		paths = append(paths, goFiles...)
	}
	// Every platform Dockerfile builds from the repository root. A change to
	// .dockerignore can change the files present in that build context.
	files, err := collectHashFiles(root, paths)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("component %q: no hash inputs under %v", component, spec.HashPaths)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return "", err
	}
	defer rootFS.Close()
	h := sha256.New()
	for _, rel := range files {
		info, err := rootFS.Stat(filepath.FromSlash(rel))
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size()); err != nil {
			return "", err
		}
		f, err := rootFS.Open(filepath.FromSlash(rel))
		if err != nil {
			return "", err
		}
		_, copyErr := io.Copy(h, f)
		closeErr := f.Close()
		if copyErr != nil {
			return "", copyErr
		}
		if closeErr != nil {
			return "", closeErr
		}
		if _, err := h.Write([]byte{0}); err != nil {
			return "", err
		}
	}
	sum := fmt.Sprintf("%x", h.Sum(nil))
	return sum[:hashHexLen], nil
}

func goDependencyFiles(repoRoot, packageDir, platform string) ([]string, error) {
	// go list resolves build tags for the same GOOS, GOARCH and CGO setting as
	// the platform Dockerfiles. A dependency lookup error must never reuse an
	// image with an incomplete hash.
	arch := strings.TrimPrefix(platform, "linux/")
	cmd := exec.Command("go", "list", "-deps", "-json", "-mod=readonly", ".")
	cmd.Dir = filepath.Join(repoRoot, filepath.FromSlash(packageDir))
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("go list: %w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("go list: %w", err)
	}
	var paths []string
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var pkg struct {
			Dir        string
			GoFiles    []string
			CgoFiles   []string
			CFiles     []string
			CXXFiles   []string
			HFiles     []string
			SFiles     []string
			SysoFiles  []string
			EmbedFiles []string
		}
		if err := decoder.Decode(&pkg); err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("decode go list: %w", err)
		}
		if pkg.Dir == "" {
			continue
		}
		relDir, err := filepath.Rel(repoRoot, pkg.Dir)
		if err != nil || relDir == ".." || strings.HasPrefix(relDir, ".."+string(filepath.Separator)) {
			continue // standard library or downloaded module
		}
		for _, group := range [][]string{pkg.GoFiles, pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.HFiles, pkg.SFiles, pkg.SysoFiles, pkg.EmbedFiles} {
			for _, name := range group {
				paths = append(paths, filepath.ToSlash(filepath.Join(relDir, name)))
			}
		}
	}
	return paths, nil
}

func collectHashFiles(repoRoot string, paths []string) ([]string, error) {
	seen := map[string]struct{}{}
	var out []string
	for _, p := range paths {
		abs := filepath.Join(repoRoot, filepath.FromSlash(p))
		info, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			continue
		}
		if !info.IsDir() {
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			rel, err := filepath.Rel(repoRoot, abs)
			if err != nil {
				return nil, err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := seen[rel]; !ok {
				seen[rel] = struct{}{}
				out = append(out, rel)
			}
			continue
		}
		err = filepath.WalkDir(abs, func(path string, d os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if shouldSkipDir(name) {
					return filepath.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() {
				return nil
			}
			rel, err := filepath.Rel(repoRoot, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if _, ok := seen[rel]; ok {
				return nil
			}
			seen[rel] = struct{}{}
			out = append(out, rel)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(out)
	return out, nil
}

func shouldSkipDir(name string) bool {
	switch name {
	case ".git", "node_modules", "bin", "vendor", "dist", "coverage", ".next", "graphify-out":
		return true
	default:
		return strings.HasPrefix(name, ".")
	}
}
