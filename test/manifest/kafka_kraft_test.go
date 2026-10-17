package manifest_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	networkingv1 "k8s.io/api/networking/v1"
	sigsyaml "sigs.k8s.io/yaml"
)

func TestKafkaNetworkPolicyRestrictsPlaintextClientIngress(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", "05-kafka-networkpolicy.yaml"))
	if err != nil {
		t.Fatalf("read Kafka NetworkPolicy: %v", err)
	}
	var policy networkingv1.NetworkPolicy
	if err := sigsyaml.Unmarshal(raw, &policy); err != nil {
		t.Fatalf("decode Kafka NetworkPolicy: %v", err)
	}
	if policy.ObjectMeta.Name != "kafka-client-ingress" || policy.ObjectMeta.Namespace != "mcp-observability" {
		t.Fatalf("NetworkPolicy identity = %s/%s, want mcp-observability/kafka-client-ingress", policy.ObjectMeta.Namespace, policy.ObjectMeta.Name)
	}
	if policy.Spec.PodSelector.MatchLabels["app"] != "kafka" || len(policy.Spec.Ingress) != 2 {
		t.Fatalf("NetworkPolicy must select Kafka pods and have separate client and broker ingress rules: %#v", policy.Spec)
	}

	clientRule := policy.Spec.Ingress[0]
	if len(clientRule.From) != 3 || len(clientRule.Ports) != 1 || clientRule.Ports[0].Port == nil || clientRule.Ports[0].Port.IntValue() != 9092 {
		t.Fatalf("Kafka client rule must allow only the three platform clients on 9092: %#v", clientRule)
	}
	clients := map[string]bool{}
	for _, peer := range clientRule.From {
		if peer.NamespaceSelector != nil || peer.PodSelector == nil {
			t.Fatalf("Kafka clients must be selected by same-namespace pod labels: %#v", peer)
		}
		clients[peer.PodSelector.MatchLabels["app"]] = true
	}
	for _, app := range []string{"mcp-ingest", "mcp-processor", "kafka-topic-init"} {
		if !clients[app] {
			t.Errorf("Kafka clients missing %q", app)
		}
	}

	brokerRule := policy.Spec.Ingress[1]
	if len(brokerRule.From) != 1 || brokerRule.From[0].NamespaceSelector != nil || brokerRule.From[0].PodSelector == nil || brokerRule.From[0].PodSelector.MatchLabels["app"] != "kafka" {
		t.Fatalf("Kafka inter-broker rule must be same-namespace broker-only: %#v", brokerRule)
	}
	ports := map[int32]bool{}
	for _, port := range brokerRule.Ports {
		if port.Port != nil {
			ports[int32(port.Port.IntValue())] = true
		}
	}
	if !ports[9092] || !ports[9093] || len(ports) != 2 {
		t.Fatalf("Kafka inter-broker rule ports = %v, want 9092 and 9093", ports)
	}
}

type kafkaKRaftDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name        string            `yaml:"name"`
		Annotations map[string]string `yaml:"annotations"`
	} `yaml:"metadata"`
	Spec struct {
		Replicas *int32 `yaml:"replicas"`
		Template struct {
			Spec struct {
				Containers []struct {
					Name    string `yaml:"name"`
					Command []string
					Env     []struct {
						Name  string `yaml:"name"`
						Value string `yaml:"value"`
					} `yaml:"env"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func TestKafkaManifestDefinesThreeNodeKRaftCluster(t *testing.T) {
	for _, manifest := range []string{"05-kafka.yaml", "05-kafka-hostpath.yaml"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", manifest))
		if err != nil {
			t.Fatalf("read %s: %v", manifest, err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		foundKafka := false
		for {
			var doc kafkaKRaftDoc
			if err := dec.Decode(&doc); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				t.Fatalf("decode %s: %v", manifest, err)
			}
			if doc.Kind == "Service" && doc.Metadata.Name == "zookeeper" {
				t.Fatalf("%s must not deploy ZooKeeper", manifest)
			}
			if doc.Kind != "StatefulSet" || doc.Metadata.Name != "kafka" {
				continue
			}
			foundKafka = true
			if doc.Spec.Replicas == nil || *doc.Spec.Replicas != 3 {
				t.Fatalf("%s Kafka replicas = %v, want 3", manifest, doc.Spec.Replicas)
			}
			if doc.Metadata.Annotations["mcpruntime.org/kafka-mode"] != "kraft" {
				t.Fatalf("%s missing KRaft mode annotation", manifest)
			}
			env := map[string]string{}
			var command string
			for _, container := range doc.Spec.Template.Spec.Containers {
				if container.Name != "kafka" {
					continue
				}
				command = strings.Join(container.Command, "\n")
				for _, item := range container.Env {
					env[item.Name] = item.Value
				}
			}
			for key, want := range map[string]string{
				"KAFKA_PROCESS_ROLES":                            "broker,controller",
				"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR":         "3",
				"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR": "3",
				"KAFKA_MIN_INSYNC_REPLICAS":                      "2",
				"KAFKA_AUTO_CREATE_TOPICS_ENABLE":                "false",
			} {
				if env[key] != want {
					t.Fatalf("%s %s = %q, want %q", manifest, key, env[key], want)
				}
			}
			if !strings.Contains(env["KAFKA_CONTROLLER_QUORUM_VOTERS"], "0@kafka-0.") ||
				!strings.Contains(env["KAFKA_CONTROLLER_QUORUM_VOTERS"], "2@kafka-2.") {
				t.Fatalf("%s has invalid controller quorum: %q", manifest, env["KAFKA_CONTROLLER_QUORUM_VOTERS"])
			}
			if !strings.Contains(command, `KAFKA_NODE_ID="${HOSTNAME##*-}"`) {
				t.Fatalf("%s must derive node ID from StatefulSet ordinal", manifest)
			}
		}
		if !foundKafka {
			t.Fatalf("%s missing Kafka StatefulSet", manifest)
		}
	}
}

func TestKafkaTopicInitCreatesReplicatedPlatformTopic(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", "05-kafka-topic-init.yaml"))
	if err != nil {
		t.Fatalf("read topic init manifest: %v", err)
	}
	text := string(raw)
	for _, want := range []string{
		"--topic mcp.events",
		"--partitions 3",
		"--replication-factor 3",
		"--config min.insync.replicas=2",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("topic init manifest missing %q", want)
		}
	}
}
