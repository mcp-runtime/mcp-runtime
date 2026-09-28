package imagecache

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const hashHexLen = 16

// ContentHash returns the first 16 hex characters of a sha256 over the sorted
// file list under Spec.HashPaths for component.
func ContentHash(repoRoot, component string) (string, error) {
	spec, ok := Specs[component]
	if !ok {
		return "", fmt.Errorf("unknown image cache component %q", component)
	}
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", err
	}
	files, err := collectHashFiles(root, spec.HashPaths)
	if err != nil {
		return "", err
	}
	if len(files) == 0 {
		return "", fmt.Errorf("component %q: no hash inputs under %v", component, spec.HashPaths)
	}
	h := sha256.New()
	for _, rel := range files {
		abs := filepath.Join(root, filepath.FromSlash(rel))
		info, err := os.Stat(abs)
		if err != nil {
			return "", err
		}
		if _, err := fmt.Fprintf(h, "%s\x00%d\x00", rel, info.Size()); err != nil {
			return "", err
		}
		f, err := os.Open(abs)
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
