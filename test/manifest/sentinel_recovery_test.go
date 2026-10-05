package manifest_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// Contract tests for the eviction/restart recovery hardening of the
// platform stack (issues #71 and #72). They pin the manifest properties
// that make recovery deterministic without any placement changes.

type recoveryProbe struct {
	FailureThreshold int `yaml:"failureThreshold"`
	PeriodSeconds    int `yaml:"periodSeconds"`
}

type recoveryDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Value int `yaml:"value"`
	Spec  struct {
		PVCRetention struct {
			WhenDeleted string `yaml:"whenDeleted"`
			WhenScaled  string `yaml:"whenScaled"`
		} `yaml:"persistentVolumeClaimRetentionPolicy"`
		Template struct {
			Spec struct {
				PriorityClassName             string `yaml:"priorityClassName"`
				TerminationGracePeriodSeconds *int   `yaml:"terminationGracePeriodSeconds"`
				Affinity                      any    `yaml:"affinity"`
				NodeSelector                  any    `yaml:"nodeSelector"`
				Containers                    []struct {
					Name            string         `yaml:"name"`
					ImagePullPolicy string         `yaml:"imagePullPolicy"`
					StartupProbe    *recoveryProbe `yaml:"startupProbe"`
					ReadinessProbe  *recoveryProbe `yaml:"readinessProbe"`
					LivenessProbe   *recoveryProbe `yaml:"livenessProbe"`
					Resources       struct {
						Requests map[string]string `yaml:"requests"`
						Limits   map[string]string `yaml:"limits"`
					} `yaml:"resources"`
				} `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func loadRecoveryDocs(t *testing.T, manifest string) []recoveryDoc {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "k8s", manifest))
	if err != nil {
		t.Fatalf("read %s: %v", manifest, err)
	}
	var docs []recoveryDoc
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var doc recoveryDoc
		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				return docs
			}
			t.Fatalf("decode %s: %v", manifest, err)
		}
		docs = append(docs, doc)
	}
}

func recoveryWorkload(t *testing.T, manifest, kind, name string) recoveryDoc {
	t.Helper()
	for _, doc := range loadRecoveryDocs(t, manifest) {
		if doc.Kind == kind && doc.Metadata.Name == name {
			return doc
		}
	}
	t.Fatalf("%s: %s/%s not found", manifest, kind, name)
	return recoveryDoc{}
}

func TestPlatformPriorityClassesOrderDataAboveServices(t *testing.T) {
	values := map[string]int{}
	for _, doc := range loadRecoveryDocs(t, "00-priority-classes.yaml") {
		if doc.Kind != "PriorityClass" {
			t.Fatalf("unexpected kind %q in priority class manifest", doc.Kind)
		}
		values[doc.Metadata.Name] = doc.Value
	}
	data, services := values["mcp-shared-data"], values["mcp-shared-services"]
	if data == 0 || services == 0 {
		t.Fatalf("missing priority classes: %v", values)
	}
	if data <= services {
		t.Fatalf("data priority %d must exceed services priority %d so stateful stores are evicted last", data, services)
	}
	if data >= 1000000000 {
		t.Fatalf("user priority class %d must stay below system classes", data)
	}
}

func TestPlatformStatefulStoresAreEvictionResilient(t *testing.T) {
	stores := []struct{ manifest, name string }{
		{"03-clickhouse.yaml", "clickhouse"},
		{"03-clickhouse-hostpath.yaml", "clickhouse"},
		{"05-kafka.yaml", "kafka"},
		{"05-kafka-hostpath.yaml", "kafka"},
		{"20-postgres.yaml", "mcp-postgres"},
		{"20-postgres-hostpath.yaml", "mcp-postgres"},
	}
	for _, store := range stores {
		t.Run(store.manifest, func(t *testing.T) {
			sts := recoveryWorkload(t, store.manifest, "StatefulSet", store.name)
			pod := sts.Spec.Template.Spec
			if pod.PriorityClassName != "mcp-shared-data" {
				t.Fatalf("priorityClassName = %q, want mcp-shared-data", pod.PriorityClassName)
			}
			if sts.Spec.PVCRetention.WhenDeleted != "Retain" || sts.Spec.PVCRetention.WhenScaled != "Retain" {
				t.Fatalf("persistentVolumeClaimRetentionPolicy = %+v, want Retain/Retain", sts.Spec.PVCRetention)
			}
			if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds < 60 {
				t.Fatalf("terminationGracePeriodSeconds = %v, want >= 60 for clean flush", pod.TerminationGracePeriodSeconds)
			}
			main := pod.Containers[0]
			if main.Resources.Requests["cpu"] == "" || main.Resources.Requests["memory"] == "" || main.Resources.Limits["memory"] == "" {
				t.Fatalf("container %s needs cpu/memory requests and a memory limit: %+v", main.Name, main.Resources)
			}
			if main.Resources.Requests["ephemeral-storage"] == "" {
				t.Fatalf("container %s needs an ephemeral-storage request so eviction ranking accounts for it", main.Name)
			}
			if main.ReadinessProbe == nil || main.LivenessProbe == nil {
				t.Fatalf("container %s needs readiness and liveness probes", main.Name)
			}
		})
	}
}

func TestPlatformSlowStartStoresHaveStartupProbes(t *testing.T) {
	// Startup probes keep liveness from killing a store that is replaying logs
	// or recovering after an eviction.
	for _, store := range []struct{ manifest, name string }{
		{"03-clickhouse.yaml", "clickhouse"},
		{"03-clickhouse-hostpath.yaml", "clickhouse"},
		{"05-kafka.yaml", "kafka"},
		{"05-kafka-hostpath.yaml", "kafka"},
	} {
		sts := recoveryWorkload(t, store.manifest, "StatefulSet", store.name)
		probe := sts.Spec.Template.Spec.Containers[0].StartupProbe
		if probe == nil {
			t.Fatalf("%s: %s missing startupProbe", store.manifest, store.name)
		}
		if window := probe.FailureThreshold * probe.PeriodSeconds; window < 180 {
			t.Fatalf("%s: startup window %ds too short for recovery, want >= 180s", store.manifest, window)
		}
	}
}

func TestPlatformPipelineWorkloadsRecoverFromTransientPullAndBrokerLoss(t *testing.T) {
	for _, manifest := range []string{"06-ingest.yaml", "07-processor.yaml"} {
		name := strings.TrimSuffix(strings.SplitN(manifest, "-", 2)[1], ".yaml")
		deploy := recoveryWorkload(t, manifest, "Deployment", "mcp-"+name)
		pod := deploy.Spec.Template.Spec
		if pod.PriorityClassName != "mcp-shared-services" {
			t.Fatalf("%s priorityClassName = %q, want mcp-shared-services", manifest, pod.PriorityClassName)
		}
		c := pod.Containers[0]
		// Always would make every restart depend on the registry being up, which
		// turns a transient registry outage during node pressure into a
		// prolonged outage. Test-mode :latest images are switched to Always at
		// render time instead.
		if c.ImagePullPolicy != "IfNotPresent" {
			t.Fatalf("%s imagePullPolicy = %q, want IfNotPresent", manifest, c.ImagePullPolicy)
		}
		if c.StartupProbe == nil {
			t.Fatalf("%s missing startupProbe; liveness would restart pods that wait on Kafka", manifest)
		}
		if c.ReadinessProbe == nil || c.LivenessProbe == nil {
			t.Fatalf("%s needs readiness and liveness probes", manifest)
		}
		if c.Resources.Requests["cpu"] == "" || c.Resources.Requests["memory"] == "" || c.Resources.Limits["memory"] == "" {
			t.Fatalf("%s needs resource requests and a memory limit", manifest)
		}
	}
}

func TestPlatformUIStartupProbeCoversPostgresWait(t *testing.T) {
	// The UI listens only after its session store opens, and setup applies
	// Postgres after the UI. Liveness must not count that two-minute wait.
	c := recoveryWorkload(t, "09-ui.yaml", "Deployment", "mcp-ui").Spec.Template.Spec.Containers[0]
	if c.StartupProbe == nil {
		t.Fatal("09-ui.yaml missing startupProbe; liveness would restart a UI waiting on Postgres")
	}
	if window := c.StartupProbe.FailureThreshold * c.StartupProbe.PeriodSeconds; window <= 120 {
		t.Fatalf("09-ui.yaml startup window %ds must exceed the 120s session store wait", window)
	}
}

func TestPlatformServiceDeploymentsUseServicesPriorityClass(t *testing.T) {
	for manifest, deployment := range map[string]string{
		"08-analytics-api.yaml": "mcp-analytics-api",
		"08-platform-api.yaml":  "mcp-platform-api",
		"08-runtime-api.yaml":   "mcp-runtime-api",
		"09-ui.yaml":            "mcp-ui",
		"10-gateway.yaml":       "mcp-platform-gateway",
	} {
		deploy := recoveryWorkload(t, manifest, "Deployment", deployment)
		if got := deploy.Spec.Template.Spec.PriorityClassName; got != "mcp-shared-services" {
			t.Fatalf("%s priorityClassName = %q, want mcp-shared-services", manifest, got)
		}
	}
}

func TestPlatformRecoveryHardeningKeepsPlacementUnchanged(t *testing.T) {
	// Kafka keeps its soft anti-affinity and no manifest gains a nodeSelector
	// as part of recovery hardening.
	for _, manifest := range []string{"05-kafka.yaml", "05-kafka-hostpath.yaml"} {
		sts := recoveryWorkload(t, manifest, "StatefulSet", "kafka")
		if sts.Spec.Template.Spec.Affinity == nil {
			t.Fatalf("%s lost its pod anti-affinity", manifest)
		}
	}
	for _, f := range []struct{ manifest, kind, name string }{
		{"03-clickhouse.yaml", "StatefulSet", "clickhouse"},
		{"06-ingest.yaml", "Deployment", "mcp-ingest"},
		{"07-processor.yaml", "Deployment", "mcp-processor"},
	} {
		if recoveryWorkload(t, f.manifest, f.kind, f.name).Spec.Template.Spec.NodeSelector != nil {
			t.Fatalf("%s must not gain a nodeSelector", f.manifest)
		}
	}
}
