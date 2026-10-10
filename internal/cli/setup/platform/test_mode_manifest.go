package platform

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
	appsv1 "k8s.io/api/apps/v1"
)

func setupSingleReplicaMode() bool {
	return os.Getenv("MCP_RUNTIME_TEST_MODE") == "1" && os.Getenv("MCP_TEST_MULTI_REPLICA") != "1"
}

func setupKafkaReplicas() int32 {
	if setupSingleReplicaMode() {
		return 1
	}
	return kafkaKRaftReplicaCount
}

// Test-mode is a functional, single-instance install. Keep the checked-in
// manifests as the normal deployment defaults and transform only setup's YAML.
func renderTestModeManifest(content string) (string, error) {
	if !setupSingleReplicaMode() {
		return content, nil
	}
	decoder := yaml.NewDecoder(strings.NewReader(content))
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	defer encoder.Close()
	for {
		var object map[string]any
		if err := decoder.Decode(&object); err != nil {
			if err == io.EOF {
				break
			}
			return "", fmt.Errorf("decode test-mode manifest: %w", err)
		}
		if len(object) == 0 {
			continue
		}
		spec, _ := object["spec"].(map[string]any)
		switch object["kind"] {
		case "Deployment", "StatefulSet":
			if spec == nil {
				return "", fmt.Errorf("test-mode workload has no spec")
			}
			spec["replicas"] = 1
		case "PodDisruptionBudget":
			if spec != nil {
				delete(spec, "maxUnavailable")
				spec["minAvailable"] = 1
			}
		}
		metadata, _ := object["metadata"].(map[string]any)
		if metadata["name"] == kafkaStatefulSetName && object["kind"] == "StatefulSet" {
			mutateTestKafkaContainers(spec)
		}
		if metadata["name"] == kafkaTopicInitJob && object["kind"] == "Job" {
			mutateTestKafkaTopicJob(spec)
		}
		if err := encoder.Encode(object); err != nil {
			return "", fmt.Errorf("encode test-mode manifest: %w", err)
		}
	}
	return output.String(), nil
}

func testModeContainers(spec map[string]any) []any {
	template, _ := spec["template"].(map[string]any)
	podSpec, _ := template["spec"].(map[string]any)
	containers, _ := podSpec["containers"].([]any)
	return containers
}

func mutateTestKafkaContainers(spec map[string]any) {
	for _, item := range testModeContainers(spec) {
		container, _ := item.(map[string]any)
		env, _ := container["env"].([]any)
		for _, item := range env {
			entry, _ := item.(map[string]any)
			switch entry["name"] {
			case "KAFKA_CONTROLLER_QUORUM_VOTERS":
				value, _ := entry["value"].(string)
				entry["value"] = strings.Split(value, ",")[0]
			case "KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR", "KAFKA_TRANSACTION_STATE_LOG_MIN_ISR", "KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR", "KAFKA_DEFAULT_REPLICATION_FACTOR", "KAFKA_MIN_INSYNC_REPLICAS":
				entry["value"] = "1"
			}
		}
	}
}

func mutateTestKafkaTopicJob(spec map[string]any) {
	for _, item := range testModeContainers(spec) {
		container, _ := item.(map[string]any)
		command, _ := container["command"].([]any)
		for i, item := range command {
			if text, ok := item.(string); ok {
				text = strings.ReplaceAll(text, "--replication-factor 3", "--replication-factor 1")
				command[i] = strings.ReplaceAll(text, "min.insync.replicas=2", "min.insync.replicas=1")
			}
		}
	}
}

func checkKafkaReplicaMode(current *appsv1.StatefulSet) error {
	if current.Spec.Replicas != nil && *current.Spec.Replicas != setupKafkaReplicas() {
		return fmt.Errorf("kafka has %d replicas, but this setup mode requires %d; use a fresh cluster for the selected replica profile instead of changing the quorum of an existing Kafka data store", *current.Spec.Replicas, setupKafkaReplicas())
	}
	return nil
}
