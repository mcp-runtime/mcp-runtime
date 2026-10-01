package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExplicitOperatorDockerfileUsesDockerBuild(t *testing.T) {
	root := t.TempDir()
	tools := t.TempDir()
	logPath := filepath.Join(t.TempDir(), "docker.log")
	for name, script := range map[string]string{
		"docker": "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$IMAGE_CACHE_TEST_LOG\"\n",
		"make":   "#!/bin/sh\nexit 99\n",
	} {
		if err := os.WriteFile(filepath.Join(tools, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", tools+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("IMAGE_CACHE_TEST_LOG", logPath)
	t.Setenv("E2E_IMAGE_CACHE", "0")

	err := run([]string{"ensure", "--image", "docker.io/library/mcp-runtime-operator:latest",
		"--dockerfile", "Dockerfile.operator", "--context", ".", "--root", root})
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "build --platform=linux/amd64 -t docker.io/library/mcp-runtime-operator:latest -f "+filepath.Join(root, "Dockerfile.operator")) {
		t.Fatalf("unexpected Docker arguments: %s", args)
	}
}
