package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	policypkg "mcp-runtime/pkg/policy"
)

func newCacheTestServer(policyFile string) *gatewayServer {
	return &gatewayServer{
		policyFile:            policyFile,
		serverName:            "demo",
		serverNamespace:       "mcp-servers",
		defaultPolicyMode:     defaultPolicyMode,
		defaultPolicyDecision: defaultPolicyDecision,
		defaultPolicyVersion:  defaultPolicyVersion,
		oauthProviders:        map[string]*oauthProvider{},
	}
}

func writeStampedPolicy(t *testing.T, path string, mutate func(*policypkg.Document)) *policypkg.Document {
	t.Helper()
	doc := &policypkg.Document{
		Server: policypkg.Server{Name: "demo", Namespace: "mcp-servers"},
		Policy: &policypkg.Config{Mode: "allow-list", DefaultDecision: "deny", PolicyVersion: "v1"},
	}
	if mutate != nil {
		mutate(doc)
	}
	if err := policypkg.Stamp(doc, ""); err != nil {
		t.Fatalf("Stamp() error = %v", err)
	}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return doc
}

func TestReloadPolicyRetainsLastKnownGoodOnInvalidUpdate(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	good := writeStampedPolicy(t, file, nil)

	s := newCacheTestServer(file)
	if err := s.reloadPolicy(); err != nil {
		t.Fatalf("reloadPolicy() error = %v", err)
	}
	snap := s.loadPolicySnapshot()
	if !snap.Ready {
		t.Fatal("snapshot not ready after a valid load")
	}
	if snap.Revision != good.Revision {
		t.Fatalf("revision = %q, want %q", snap.Revision, good.Revision)
	}

	// Replace the file with an unsupported schema version: an invalid update.
	bad := &policypkg.Document{Server: policypkg.Server{Name: "demo"}, SchemaVersion: "v999", Revision: "sha256:bad"}
	data, _ := json.Marshal(bad)
	if err := os.WriteFile(file, data, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	if err := s.reloadPolicy(); err == nil {
		t.Fatal("reloadPolicy() error = nil, want validation error")
	}
	snap = s.loadPolicySnapshot()
	if !snap.Ready {
		t.Fatal("snapshot should remain ready (last-known-good retained)")
	}
	if snap.Revision != good.Revision {
		t.Fatalf("revision changed to %q on invalid update, want retained %q", snap.Revision, good.Revision)
	}
	if snap.Err == nil {
		t.Fatal("expected last reload error to be recorded")
	}
	// Traffic continues on the last-known-good policy: no policy_unavailable.
	pol, perr := s.currentPolicy()
	if perr != nil {
		t.Fatalf("currentPolicy() error = %v, want nil (serve last-known-good)", perr)
	}
	if pol.Revision != good.Revision {
		t.Fatalf("currentPolicy revision = %q, want %q", pol.Revision, good.Revision)
	}
}

func TestReadyFailsUntilValidPolicyLoaded(t *testing.T) {
	s := newCacheTestServer("")
	// Seed the unready default snapshot, as startPolicyCache does before the
	// first reload.
	s.snapshotPolicy(policySnapshot{Policy: s.defaultPolicyDocument()})

	recorder := httptest.NewRecorder()
	s.handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("ready status = %d, want %d before first valid load", recorder.Code, http.StatusServiceUnavailable)
	}
	if _, perr := s.currentPolicy(); perr == nil {
		t.Fatal("currentPolicy() error = nil before first valid load, want policy_unavailable")
	}

	// A no-file gateway reaches ready once its stamped default validates.
	if err := s.reloadPolicy(); err != nil {
		t.Fatalf("reloadPolicy() error = %v", err)
	}
	recorder = httptest.NewRecorder()
	s.handleReady(recorder, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("ready status = %d, want %d after valid load", recorder.Code, http.StatusOK)
	}
}

func TestConfigStatusExposesSanitizedMetadata(t *testing.T) {
	file := filepath.Join(t.TempDir(), "policy.json")
	good := writeStampedPolicy(t, file, nil)

	s := newCacheTestServer(file)
	if err := s.reloadPolicy(); err != nil {
		t.Fatalf("reloadPolicy() error = %v", err)
	}

	recorder := httptest.NewRecorder()
	s.handleConfigStatus(recorder, httptest.NewRequest(http.MethodGet, "/config/status", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("config status code = %d, want %d", recorder.Code, http.StatusOK)
	}
	var status configStatus
	if err := json.Unmarshal(recorder.Body.Bytes(), &status); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if !status.Ready {
		t.Fatal("config status ready = false, want true")
	}
	if status.SchemaVersion != policypkg.SchemaVersion {
		t.Fatalf("schema version = %q, want %q", status.SchemaVersion, policypkg.SchemaVersion)
	}
	if status.Revision != good.Revision {
		t.Fatalf("revision = %q, want %q", status.Revision, good.Revision)
	}
	if status.LoadedAt == "" {
		t.Fatal("loaded_at empty, want a timestamp")
	}
	if status.LastReloadError != "" {
		t.Fatalf("last_reload_error = %q, want empty", status.LastReloadError)
	}
	// The sanitized status must not leak the policy body.
	for _, leaked := range []string{"grants", "sessions", "tools", "server"} {
		if jsonContains(t, recorder.Body.Bytes(), leaked) {
			t.Fatalf("config status leaked %q from policy body: %s", leaked, recorder.Body.String())
		}
	}
}

func TestNextPolicyReloadIntervalAddsBoundedJitter(t *testing.T) {
	for i := 0; i < 1000; i++ {
		interval := nextPolicyReloadInterval()
		if interval < policyReloadInterval {
			t.Fatalf("interval = %s, want at least %s", interval, policyReloadInterval)
		}
		if interval >= policyReloadInterval+policyReloadJitter {
			t.Fatalf("interval = %s, want less than %s", interval, policyReloadInterval+policyReloadJitter)
		}
	}
}

func jsonContains(t *testing.T, body []byte, key string) bool {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	_, ok := m[key]
	return ok
}
