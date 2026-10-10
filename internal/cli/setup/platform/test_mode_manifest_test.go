package platform

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"mcp-runtime/internal/cli/setup/assetpath"
)

func TestTraefikReplicaModePatch(t *testing.T) {
	for _, mode := range []struct {
		test, multi string
		single      bool
	}{
		{"", "0", false}, {"1", "0", true}, {"1", "1", false},
	} {
		t.Setenv("MCP_RUNTIME_TEST_MODE", mode.test)
		t.Setenv("MCP_TEST_MULTI_REPLICA", mode.multi)
		var spec traefikDeploymentSpec
		if err := json.Unmarshal([]byte(`{"spec":{"replicas":2,"template":{"spec":{"containers":[{"name":"traefik","args":[],"volumeMounts":[]}],"volumes":[]}}}}`), &spec); err != nil {
			t.Fatal(err)
		}
		patch, err := traefikMiddlewarePatch(spec, "traefik")
		if err != nil {
			t.Fatal(err)
		}
		var ops []jsonPatchOperation
		if err := json.Unmarshal(patch, &ops); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, op := range ops {
			if op.Path == "/spec/replicas" {
				found = true
				if op.Value != float64(1) {
					t.Fatalf("replicas=%v", op.Value)
				}
			}
		}
		if found != mode.single {
			t.Fatalf("test=%s multi=%s has replica patch=%v", mode.test, mode.multi, found)
		}
	}
}

func TestTestModeWorkloadReplicas(t *testing.T) {
	paths := append(analyticsServiceManifests("k8s/20-postgres.yaml"),
		"k8s/03-clickhouse.yaml", "k8s/03-clickhouse-hostpath.yaml", "k8s/05-kafka.yaml",
		"k8s/05-kafka-hostpath.yaml", "k8s/20-postgres-hostpath.yaml",
		"config/manager/manager.yaml", "config/ingress/base/traefik.yaml")
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			resolved, err := assetpath.ResolveRepoAssetPath(path)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := os.ReadFile(resolved)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("MCP_RUNTIME_TEST_MODE", "")
			t.Setenv("MCP_TEST_MULTI_REPLICA", "0")
			normal, err := renderTestModeManifest(string(raw))
			if err != nil || normal != string(raw) {
				t.Fatalf("normal manifest changed: %v", err)
			}
			t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
			t.Setenv("MCP_TEST_MULTI_REPLICA", "1")
			multi, err := renderTestModeManifest(string(raw))
			if err != nil || multi != string(raw) {
				t.Fatalf("multi-replica manifest changed: %v", err)
			}
			t.Setenv("MCP_TEST_MULTI_REPLICA", "0")
			t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
			rendered, err := renderTestModeManifest(string(raw))
			if err != nil {
				t.Fatal(err)
			}
			decoder := yaml.NewDecoder(strings.NewReader(rendered))
			for {
				var object map[string]any
				if err := decoder.Decode(&object); err != nil {
					if err == io.EOF {
						break
					}
					t.Fatal(err)
				}
				if object["kind"] == "Deployment" || object["kind"] == "StatefulSet" {
					if object["spec"].(map[string]any)["replicas"] != 1 {
						t.Fatalf("workload does not have one replica: %v", object["metadata"])
					}
				}
			}
		})
	}
}

func TestTestModeKafkaReplication(t *testing.T) {
	t.Setenv("MCP_RUNTIME_TEST_MODE", "1")
	t.Setenv("MCP_TEST_MULTI_REPLICA", "0")
	for _, path := range []string{"k8s/05-kafka.yaml", "k8s/05-kafka-hostpath.yaml", "k8s/05-kafka-topic-init.yaml"} {
		resolved, err := assetpath.ResolveRepoAssetPath(path)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := os.ReadFile(resolved)
		if err != nil {
			t.Fatal(err)
		}
		rendered, err := renderTestModeManifest(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(rendered, "1@kafka-1") || strings.Contains(rendered, "2@kafka-2") || strings.Contains(rendered, "--replication-factor 3") || strings.Contains(rendered, "min.insync.replicas=2") {
			t.Fatalf("multi-node Kafka settings remain in %s", path)
		}
		decoder := yaml.NewDecoder(strings.NewReader(rendered))
		for {
			var object map[string]any
			if err := decoder.Decode(&object); err != nil {
				if err == io.EOF {
					break
				}
				t.Fatal(err)
			}
			if object["kind"] != "StatefulSet" {
				continue
			}
			for _, item := range testModeContainers(object["spec"].(map[string]any)) {
				container := item.(map[string]any)
				for _, entry := range container["env"].([]any) {
					env := entry.(map[string]any)
					name := env["name"].(string)
					if strings.Contains(name, "REPLICATION_FACTOR") || name == "KAFKA_MIN_INSYNC_REPLICAS" || name == "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR" {
						if env["value"] != "1" {
							t.Fatalf("%s=%v", name, env["value"])
						}
					}
				}
			}
		}
	}
}

func TestKafkaReplicaModeRequiresFreshStore(t *testing.T) {
	t.Setenv("MCP_TEST_MULTI_REPLICA", "0")
	for _, testMode := range []string{"", "1"} {
		t.Setenv("MCP_RUNTIME_TEST_MODE", testMode)
		desired := setupKafkaReplicas()
		current := &appsv1.StatefulSet{ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{"mcpruntime.org/kafka-mode": "kraft"}}}
		current.Spec.Replicas = &desired
		if err := checkKafkaReplicaMode(current); err != nil {
			t.Fatal(err)
		}
		if kafkaStatefulSetNeedsKRaftRecreate(current) == false {
			t.Fatal("incomplete layout should still need reconciliation")
		}
		other := int32(3)
		if desired == 3 {
			other = 1
		}
		current.Spec.Replicas = &other
		if err := checkKafkaReplicaMode(current); err == nil {
			t.Fatal("existing quorum transition must be rejected")
		}
		current.Annotations = nil
		if err := checkKafkaReplicaMode(current); err == nil {
			t.Fatal("an older unannotated store must also be preserved")
		}
	}
}
