// Package regression validates the incident -> regression-check coverage
// index at docs/contributor/regression-index.yaml (issue #546).
package regression

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	indexPath = "docs/contributor/regression-index.yaml"
	docPath   = "docs/contributor/regression-index.md"
)

type check struct {
	Kind    string `yaml:"kind"`
	Path    string `yaml:"path"`
	Name    string `yaml:"name"`
	Command string `yaml:"command"`
	CIJob   string `yaml:"ci_job"`
}

type waiver struct {
	Owner    string `yaml:"owner"`
	FollowUp string `yaml:"follow_up"`
	Expires  string `yaml:"expires"`
	Blocker  string `yaml:"blocker"`
}

type incident struct {
	ID                string  `yaml:"id"`
	Title             string  `yaml:"title"`
	ProtectedBehavior string  `yaml:"protected_behavior"`
	Status            string  `yaml:"status"`
	Checks            []check `yaml:"checks"`
	Gap               string  `yaml:"gap"`
	Waiver            *waiver `yaml:"waiver"`
}

type index struct {
	Version   int        `yaml:"version"`
	Incidents []incident `yaml:"incidents"`
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func loadIndex(t *testing.T, root string) index {
	t.Helper()
	var idx index
	dec := yaml.NewDecoder(strings.NewReader(readFile(t, root, indexPath)))
	dec.KnownFields(true)
	if err := dec.Decode(&idx); err != nil {
		t.Fatalf("parse %s: %v", indexPath, err)
	}
	return idx
}

// ciJobs returns the job keys of every workflow, keyed by file name.
func ciJobs(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(root, ".github/workflows/*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	for _, f := range files {
		var wf struct {
			Jobs map[string]yaml.Node `yaml:"jobs"`
		}
		if err := yaml.Unmarshal([]byte(readFile(t, root, filepath.Join(".github/workflows", filepath.Base(f)))), &wf); err != nil {
			t.Fatalf("parse %s: %v", f, err)
		}
		jobs := map[string]bool{}
		for k := range wf.Jobs {
			jobs[k] = true
		}
		out[filepath.Base(f)] = jobs
	}
	return out
}

func validate(idx index, now time.Time, read func(string) (string, error), jobs map[string]map[string]bool) []string {
	var errs []string
	add := func(id, format string, a ...any) {
		errs = append(errs, "incident #"+id+": "+fmt.Sprintf(format, a...))
	}
	if idx.Version != 1 {
		errs = append(errs, "unsupported index version")
	}
	if len(idx.Incidents) == 0 {
		errs = append(errs, "index lists no incidents")
	}
	seen := map[string]bool{}
	numeric := regexp.MustCompile(`^[0-9]+$`)
	for _, inc := range idx.Incidents {
		if !numeric.MatchString(inc.ID) {
			errs = append(errs, "incident id must be a GitHub issue number: "+inc.ID)
			continue
		}
		if seen[inc.ID] {
			add(inc.ID, "duplicate id")
		}
		seen[inc.ID] = true
		if strings.TrimSpace(inc.Title) == "" || strings.TrimSpace(inc.ProtectedBehavior) == "" {
			add(inc.ID, "title and protected_behavior are required")
		}
		switch inc.Status {
		case "covered":
			if len(inc.Checks) == 0 {
				add(inc.ID, "covered requires at least one mapped check")
			}
		case "partial":
			if len(inc.Checks) == 0 {
				add(inc.ID, "partial requires at least one mapped check")
			}
			if strings.TrimSpace(inc.Gap) == "" {
				add(inc.ID, "partial requires a gap description")
			}
			errs = append(errs, validateWaiver(inc, now)...)
		case "untested":
			if len(inc.Checks) > 0 {
				add(inc.ID, "untested must not list checks; use partial or covered")
			}
			errs = append(errs, validateWaiver(inc, now)...)
		default:
			add(inc.ID, "status must be covered, partial, or untested (got %q)", inc.Status)
		}
		for _, c := range inc.Checks {
			errs = append(errs, validateCheck(inc.ID, c, read, jobs)...)
		}
	}
	return errs
}

func validateWaiver(inc incident, now time.Time) []string {
	prefix := "incident #" + inc.ID + ": "
	if inc.Waiver == nil {
		return []string{prefix + inc.Status + " requires a waiver (owner, follow_up, expires, blocker)"}
	}
	var errs []string
	w := inc.Waiver
	if w.Owner == "" || w.FollowUp == "" || w.Blocker == "" || w.Expires == "" {
		errs = append(errs, prefix+"waiver needs owner, follow_up, expires, and blocker")
	}
	if w.Expires != "" {
		exp, err := time.Parse("2006-01-02", w.Expires)
		if err != nil {
			errs = append(errs, prefix+"waiver expires must be YYYY-MM-DD")
		} else if now.After(exp.Add(24 * time.Hour)) {
			errs = append(errs, prefix+"waiver expired on "+w.Expires+"; add a regression check or renew with justification")
		}
	}
	return errs
}

func validateCheck(id string, c check, read func(string) (string, error), jobs map[string]map[string]bool) []string {
	var errs []string
	fail := func(format string, a ...any) {
		errs = append(errs, "incident #"+id+": check "+c.Path+": "+fmt.Sprintf(format, a...))
	}
	if c.Kind == "" || c.Path == "" || c.Name == "" || c.Command == "" || c.CIJob == "" {
		fail("kind, path, name, command, and ci_job are required")
		return errs
	}
	body, err := read(c.Path)
	if err != nil {
		fail("path does not exist")
		return errs
	}
	switch c.Kind {
	case "go-test":
		if !regexp.MustCompile(`(?m)^func ` + regexp.QuoteMeta(c.Name) + `\(`).MatchString(body) {
			fail("func %s not found", c.Name)
		}
	case "e2e-scenario", "script":
		if !strings.Contains(body, c.Name) {
			fail("%q not found in file", c.Name)
		}
	default:
		fail("kind must be go-test, e2e-scenario, or script")
	}
	file, job, ok := strings.Cut(c.CIJob, "#")
	if !ok || jobs[file] == nil || !jobs[file][job] {
		fail("ci_job %q must be <workflow>.yaml#<job> naming an existing job", c.CIJob)
	}
	return errs
}

func TestRegressionIndex(t *testing.T) {
	root := repoRoot(t)
	read := func(rel string) (string, error) {
		b, err := os.ReadFile(filepath.Join(root, rel))
		return string(b), err
	}
	if errs := validate(loadIndex(t, root), time.Now(), read, ciJobs(t, root)); len(errs) > 0 {
		t.Fatalf("regression index invalid:\n- %s", strings.Join(errs, "\n- "))
	}
}

func TestRegressionIndexDocListsEveryIncident(t *testing.T) {
	root := repoRoot(t)
	doc := readFile(t, root, docPath)
	for _, inc := range loadIndex(t, root).Incidents {
		if !strings.Contains(doc, "#"+inc.ID) {
			t.Errorf("%s does not mention #%s", docPath, inc.ID)
		}
	}
}

func TestValidateRejectsUnmappedIncident(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	read := func(string) (string, error) { return "", os.ErrNotExist }
	valid := &waiver{Owner: "o", FollowUp: "u", Expires: "2026-12-31", Blocker: "b"}
	cases := map[string]incident{
		"covered without checks":  {ID: "1", Title: "t", ProtectedBehavior: "p", Status: "covered"},
		"untested without waiver": {ID: "1", Title: "t", ProtectedBehavior: "p", Status: "untested"},
		"expired waiver": {ID: "1", Title: "t", ProtectedBehavior: "p", Status: "untested",
			Waiver: &waiver{Owner: "o", FollowUp: "u", Expires: "2026-01-01", Blocker: "b"}},
		"missing check path": {ID: "1", Title: "t", ProtectedBehavior: "p", Status: "covered",
			Checks: []check{{Kind: "go-test", Path: "x_test.go", Name: "TestX", Command: "c", CIJob: "ci.yaml#test"}}},
	}
	for name, inc := range cases {
		if errs := validate(index{Version: 1, Incidents: []incident{inc}}, now, read, nil); len(errs) == 0 {
			t.Errorf("%s: expected validation errors", name)
		}
	}
	ok := incident{ID: "1", Title: "t", ProtectedBehavior: "p", Status: "untested", Waiver: valid}
	if errs := validate(index{Version: 1, Incidents: []incident{ok}}, now, read, nil); len(errs) != 0 {
		t.Errorf("valid waiver rejected: %v", errs)
	}
}
