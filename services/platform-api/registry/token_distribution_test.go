package registry

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"mcp-runtime/pkg/registryauth"
)

// Optional disposable-container acceptance check. No kube context is used and
// the signing private key never leaves the test process. Only the public root
// is copied into Distribution. CI can opt in with MCP_REGISTRY_NATIVE_E2E=1.
func TestDistributionEnforcesNativeRegistryScopes(t *testing.T) {
	if os.Getenv("MCP_REGISTRY_NATIVE_E2E") != "1" {
		t.Skip("set MCP_REGISTRY_NATIVE_E2E=1 for disposable Distribution acceptance")
	}
	docker := func(args ...string) (string, error) {
		t.Helper()
		program := "docker"
		if os.Getenv("MCP_E2E_DOCKER_SUDO") == "1" {
			program = "sudo"
			args = append([]string{"-n", "docker"}, args...)
		}
		out, err := exec.Command(program, args...).CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("docker command failed: %w", err)
		}
		return strings.TrimSpace(string(out)), nil
	}
	deps, cert := registryTestDeps(t)
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { HandleToken(w, r, deps) }))
	defer broker.Close()
	if _, err := docker("image", "inspect", "registry:2.8.3"); err != nil {
		if _, err := docker("pull", "registry:2.8.3"); err != nil {
			t.Fatal(err)
		}
	}
	id, err := docker("create", "-p", "127.0.0.1::5000", "-e", "REGISTRY_AUTH=token", "-e", "REGISTRY_AUTH_TOKEN_REALM="+broker.URL, "-e", "REGISTRY_AUTH_TOKEN_SERVICE="+registryauth.Service, "-e", "REGISTRY_AUTH_TOKEN_ISSUER="+registryauth.Issuer, "-e", "REGISTRY_AUTH_TOKEN_ROOTCERTBUNDLE=/tmp/registry-root.crt", "registry:2.8.3")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker("rm", "-f", id) })
	path := filepath.Join(t.TempDir(), "root.crt")
	if err := os.WriteFile(path, cert, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := docker("cp", path, id+":/tmp/registry-root.crt"); err != nil {
		t.Fatal(err)
	}
	if _, err := docker("start", id); err != nil {
		t.Fatal(err)
	}
	port, err := docker("port", id, "5000/tcp")
	if err != nil {
		t.Fatal(err)
	}
	base := "http://" + port
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := client.Get(base + "/v2/")
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 401 {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatal("registry did not start with native authentication")
		}
		time.Sleep(100 * time.Millisecond)
	}
	request := func(method, path, token string) int {
		t.Helper()
		req, _ := http.NewRequest(method, base+path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	for _, tc := range []struct{ method, path string }{{"GET", "/v2/_catalog"}, {"GET", "/v2/mcp-platform-api/manifests/latest"}, {"POST", "/v2/acme/app/blobs/uploads/"}} {
		if code := request(tc.method, tc.path, ""); code != 401 {
			t.Fatalf("anonymous %s %s status %d", tc.method, tc.path, code)
		}
	}
	token := func(scope, user, password string) string {
		t.Helper()
		req, _ := http.NewRequest("GET", broker.URL+"?service="+registryauth.Service+"&scope="+url.QueryEscape(scope), nil)
		req.SetBasicAuth(user, password)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("broker status %d", resp.StatusCode)
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		return body.Token
	}
	own := token("repository:acme/app:pull,push", "alice", "test-password")
	if code := request("GET", "/v2/acme/app/tags/list", own); code != 404 {
		t.Fatalf("valid own token rejected: %d", code)
	}
	if code := request("POST", "/v2/acme/app/blobs/uploads/", own); code != 202 {
		t.Fatalf("authorized upload rejected: %d", code)
	}
	if code := request("GET", "/v2/other/app/tags/list", own); code != 401 {
		t.Fatalf("cross-team bearer accepted: %d", code)
	}
	readOnly := token("repository:acme/app:pull", "mcp-pull-mcp-team-acme", "mcpp_test")
	if code := request("GET", "/v2/acme/app/tags/list", readOnly); code != 404 {
		t.Fatalf("node pull rejected: %d", code)
	}
	publishRegistryFixture(t, client, base, own, readOnly)
	if code := request("POST", "/v2/acme/app/blobs/uploads/", readOnly); code != 401 {
		t.Fatalf("node credential could upload: %d", code)
	}
}

// Publish a complete minimal OCI image, then read it with a node bearer. This
// exercises blob finalization and manifests, not only upload authorization.
func publishRegistryFixture(t *testing.T, client *http.Client, base, publisher, reader string) {
	t.Helper()
	config := []byte(`{"architecture":"amd64","os":"linux","rootfs":{"type":"layers","diff_ids":[]}}`)
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(config))
	send := func(method, path, contentType, token string, body []byte) (int, http.Header, []byte) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, resp.Header, data
	}
	code, headers, _ := send("POST", "/v2/acme/app/blobs/uploads/", "", publisher, nil)
	if code != 202 {
		t.Fatalf("upload start: %d", code)
	}
	location, err := url.Parse(headers.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	query := location.Query()
	query.Set("digest", digest)
	location.RawQuery = query.Encode()
	code, _, _ = send("PUT", location.RequestURI(), "application/octet-stream", publisher, config)
	if code != 201 {
		t.Fatalf("blob finalization: %d", code)
	}
	mediaType := "application/vnd.oci.image.manifest.v1+json"
	manifest, _ := json.Marshal(map[string]any{"schemaVersion": 2, "mediaType": mediaType, "config": map[string]any{"mediaType": "application/vnd.oci.image.config.v1+json", "size": len(config), "digest": digest}, "layers": []any{}})
	code, _, _ = send("PUT", "/v2/acme/app/manifests/acceptance", mediaType, publisher, manifest)
	if code != 201 {
		t.Fatalf("manifest publication: %d", code)
	}
	req, _ := http.NewRequest("GET", base+"/v2/acme/app/manifests/acceptance", nil)
	req.Header.Set("Authorization", "Bearer "+reader)
	req.Header.Set("Accept", mediaType)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode != 200 || !bytes.Equal(data, manifest) {
		t.Fatalf("node manifest pull: %d", resp.StatusCode)
	}
	code, _, data = send("GET", "/v2/acme/app/blobs/"+digest, "", reader, nil)
	if code != 200 || !bytes.Equal(data, config) {
		t.Fatalf("node config pull: %d", code)
	}
}
