package manifest_test

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// TestServiceDockerfilesCopyEveryGoPackage guards the image build contract: a
// service Dockerfile must copy every directory that holds non-test Go sources,
// otherwise `go build` inside the image fails on a missing local package even
// though the host build passes.
func TestServiceDockerfilesCopyEveryGoPackage(t *testing.T) {
	root := filepath.Join("..", "..")
	dockerfiles, err := filepath.Glob(filepath.Join(root, "services", "*", "Dockerfile"))
	if err != nil || len(dockerfiles) == 0 {
		t.Fatalf("no service Dockerfiles found: %v", err)
	}
	for _, dockerfile := range dockerfiles {
		service := filepath.Base(filepath.Dir(dockerfile))
		t.Run(service, func(t *testing.T) {
			sources := dockerfileCopySources(t, dockerfile)
			serviceDir := filepath.Join(root, "services", service)
			err := filepath.WalkDir(serviceDir, func(p string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					return nil
				}
				switch d.Name() {
				case "frontend", "node_modules", "testdata", "static", "vendor":
					return filepath.SkipDir
				}
				if !hasNonTestGo(t, p) {
					return nil
				}
				rel, relErr := filepath.Rel(root, p)
				if relErr != nil {
					return relErr
				}
				rel = filepath.ToSlash(rel)
				if !dockerfileCopiesDir(sources, rel) {
					t.Errorf("%s does not copy Go package directory %s into the build stage", dockerfile, rel)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func dockerfileCopySources(t *testing.T, dockerfile string) []string {
	t.Helper()
	data, err := os.ReadFile(dockerfile)
	if err != nil {
		t.Fatal(err)
	}
	var sources []string
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 || !strings.EqualFold(fields[0], "COPY") {
			continue
		}
		args := fields[1:]
		copyFromStage := false
		var operands []string
		for _, arg := range args {
			if strings.HasPrefix(arg, "--") {
				copyFromStage = copyFromStage || strings.HasPrefix(arg, "--from")
				continue
			}
			operands = append(operands, arg)
		}
		if copyFromStage || len(operands) < 2 {
			continue
		}
		sources = append(sources, operands[:len(operands)-1]...)
	}
	return sources
}

func hasNonTestGo(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() && strings.HasSuffix(name, ".go") && !strings.HasSuffix(name, "_test.go") {
			return true
		}
	}
	return false
}

// dockerfileCopiesDir reports whether a COPY source brings in the Go files of
// dir: the directory itself, an ancestor directory, or a *.go glob inside it.
func dockerfileCopiesDir(sources []string, dir string) bool {
	for _, source := range sources {
		clean := path.Clean(source)
		if clean == dir || strings.HasPrefix(dir, clean+"/") {
			return true
		}
		if matched, _ := path.Match(source, dir+"/x.go"); matched {
			return true
		}
	}
	return false
}
