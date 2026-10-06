package certmanager

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/util/version"
)

func TestCertManagerReleaseMatchesInstallManifest(t *testing.T) {
	t.Parallel()
	rel := CertManagerRelease()
	v, err := version.ParseSemantic(rel)
	if err != nil || v.PreRelease() != "" || !strings.HasPrefix(rel, "v") {
		t.Fatalf("pinned cert-manager release %q must be a stable vX.Y.Z tag (err=%v)", rel, err)
	}
	want := "https://github.com/cert-manager/cert-manager/releases/download/" + rel + "/cert-manager.yaml"
	if got := CertManagerInstallManifestURL(); got != want {
		t.Fatalf("install manifest URL = %q, want %q", got, want)
	}
}

// TestCertManagerReleaseReferencesDoNotDrift keeps docs, skills, and setup
// messages on the pinned release so a version bump updates them together.
func TestCertManagerReleaseReferencesDoNotDrift(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "..")
	pinned := CertManagerRelease()
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`cert-manager/releases/download/(v\d+\.\d+\.\d+)/cert-manager\.yaml`),
		regexp.MustCompile(`Installing cert-manager (v\d+\.\d+\.\d+)`),
	}
	for _, dir := range []string{"docs", ".codex/skills", "internal", "cmd", "hack", "test/e2e"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); err != nil {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			switch filepath.Ext(path) {
			case ".md", ".go", ".sh", ".yaml", ".yml":
			default:
				return nil
			}
			if strings.HasSuffix(path, "_test.go") {
				return nil
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, re := range patterns {
				for _, m := range re.FindAllStringSubmatch(string(data), -1) {
					if m[1] != pinned {
						rel, _ := filepath.Rel(root, path)
						t.Errorf("%s references cert-manager %s; the install pin is %s", rel, m[1], pinned)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}
