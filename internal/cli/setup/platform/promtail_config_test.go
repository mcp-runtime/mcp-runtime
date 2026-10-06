package platform

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

const promtailManifestPath = "../../../../k8s/18-promtail.yaml"

// promtailRelabel mirrors the Prometheus relabel_config fields Promtail uses.
type promtailRelabel struct {
	SourceLabels []string `json:"source_labels"`
	Separator    *string  `json:"separator"`
	Regex        *string  `json:"regex"`
	TargetLabel  string   `json:"target_label"`
	Replacement  *string  `json:"replacement"`
	Action       string   `json:"action"`
}

type promtailScrapeConfig struct {
	JobName             string `json:"job_name"`
	KubernetesSDConfigs []struct {
		Role       string `json:"role"`
		Namespaces any    `json:"namespaces"`
		Selectors  []struct {
			Role  string `json:"role"`
			Field string `json:"field"`
		} `json:"selectors"`
	} `json:"kubernetes_sd_configs"`
	StaticConfigs  []any             `json:"static_configs"`
	PipelineStages []map[string]any  `json:"pipeline_stages"`
	RelabelConfigs []promtailRelabel `json:"relabel_configs"`
}

type promtailDaemonSet struct {
	Spec struct {
		Template struct {
			Spec struct {
				Containers []struct {
					Args []string `json:"args"`
					Env  []struct {
						Name      string `json:"name"`
						ValueFrom struct {
							FieldRef struct {
								FieldPath string `json:"fieldPath"`
							} `json:"fieldRef"`
						} `json:"valueFrom"`
					} `json:"env"`
					VolumeMounts []struct {
						Name      string `json:"name"`
						MountPath string `json:"mountPath"`
						ReadOnly  bool   `json:"readOnly"`
					} `json:"volumeMounts"`
				} `json:"containers"`
				Volumes []struct {
					Name     string `json:"name"`
					HostPath *struct {
						Path string `json:"path"`
					} `json:"hostPath"`
				} `json:"volumes"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
}

func loadPromtailConfig(t *testing.T) (string, string) {
	t.Helper()
	content, err := os.ReadFile(promtailManifestPath)
	if err != nil {
		t.Fatalf("read promtail manifest: %v", err)
	}
	var cfg, ds string
	for _, doc := range strings.Split(string(content), "\n---\n") {
		var obj struct {
			Kind string            `json:"kind"`
			Data map[string]string `json:"data"`
		}
		if err := yaml.Unmarshal([]byte(doc), &obj); err != nil {
			t.Fatalf("manifest document is not valid YAML: %v", err)
		}
		switch obj.Kind {
		case "ConfigMap":
			cfg = obj.Data["promtail.yaml"]
		case "DaemonSet":
			ds = doc
		}
	}
	if cfg == "" || ds == "" {
		t.Fatal("promtail ConfigMap or DaemonSet missing")
	}
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(cfg), &parsed); err != nil {
		t.Fatalf("promtail.yaml is not valid YAML: %v", err)
	}
	return cfg, ds
}

// expandPromtailEnv applies -config.expand-env semantics: ${VAR} expands and
// $$ is a literal dollar sign.
func expandPromtailEnv(cfg string, env map[string]string) string {
	const dollar = "\x00DOLLAR\x00"
	out := strings.ReplaceAll(cfg, "$$", dollar)
	out = os.Expand(out, func(name string) string { return env[name] })
	return strings.ReplaceAll(out, dollar, "$")
}

func loadPromtailScrapeConfigs(t *testing.T, env map[string]string) []promtailScrapeConfig {
	t.Helper()
	raw, _ := loadPromtailConfig(t)
	var cfg struct {
		ScrapeConfigs []promtailScrapeConfig `json:"scrape_configs"`
	}
	if err := yaml.Unmarshal([]byte(expandPromtailEnv(raw, env)), &cfg); err != nil {
		t.Fatalf("expanded promtail.yaml is not valid YAML: %v", err)
	}
	return cfg.ScrapeConfigs
}

func loadPromtailDaemonSet(t *testing.T) promtailDaemonSet {
	t.Helper()
	_, doc := loadPromtailConfig(t)
	var ds promtailDaemonSet
	if err := yaml.Unmarshal([]byte(doc), &ds); err != nil {
		t.Fatalf("DaemonSet is not valid YAML: %v", err)
	}
	if len(ds.Spec.Template.Spec.Containers) != 1 {
		t.Fatalf("expected one promtail container, got %d", len(ds.Spec.Template.Spec.Containers))
	}
	return ds
}

// applyPromtailRelabel runs the replace/drop/keep subset of Prometheus
// relabeling that the manifest uses. It returns nil when the target is dropped.
func applyPromtailRelabel(t *testing.T, rules []promtailRelabel, in map[string]string) map[string]string {
	t.Helper()
	labels := map[string]string{}
	for k, v := range in {
		labels[k] = v
	}
	for _, r := range rules {
		sep := ";"
		if r.Separator != nil {
			sep = *r.Separator
		}
		expr := "(.*)"
		if r.Regex != nil {
			expr = *r.Regex
		}
		re, err := regexp.Compile("^(?:" + expr + ")$")
		if err != nil {
			t.Fatalf("invalid relabel regex %q: %v", expr, err)
		}
		values := make([]string, 0, len(r.SourceLabels))
		for _, name := range r.SourceLabels {
			values = append(values, labels[name])
		}
		value := strings.Join(values, sep)
		match := re.FindStringSubmatchIndex(value)
		switch r.Action {
		case "", "replace":
			if match == nil {
				continue
			}
			repl := "$1"
			if r.Replacement != nil {
				repl = *r.Replacement
			}
			result := string(re.ExpandString(nil, repl, value, match))
			if result == "" {
				delete(labels, r.TargetLabel)
			} else {
				labels[r.TargetLabel] = result
			}
		case "drop":
			if match != nil {
				return nil
			}
		case "keep":
			if match == nil {
				return nil
			}
		default:
			t.Fatalf("relabel action %q is not modeled by this test", r.Action)
		}
	}
	return labels
}

func kubernetesPodsJob(t *testing.T, env map[string]string) promtailScrapeConfig {
	t.Helper()
	for _, sc := range loadPromtailScrapeConfigs(t, env) {
		if sc.JobName == "kubernetes-pods" {
			return sc
		}
	}
	t.Fatal("promtail config has no kubernetes-pods job")
	return promtailScrapeConfig{}
}

// TestPromtailDiscoverySelectsPodsOnPromtailNode guards issue #496. Promtail
// derives its own spec.nodeName selector and __host__ filter from HOSTNAME.
// HOSTNAME defaults to the Promtail pod name, which matches no pods, so the
// DaemonSet must set it to the node name and the config must select on it.
func TestPromtailDiscoverySelectsPodsOnPromtailNode(t *testing.T) {
	ds := loadPromtailDaemonSet(t)
	container := ds.Spec.Template.Spec.Containers[0]

	hostnameFromNode := false
	for _, env := range container.Env {
		if env.Name == "HOSTNAME" && env.ValueFrom.FieldRef.FieldPath == "spec.nodeName" {
			hostnameFromNode = true
		}
	}
	if !hostnameFromNode {
		t.Fatal("promtail DaemonSet must set HOSTNAME from spec.nodeName; Promtail filters pod discovery by HOSTNAME")
	}
	expandEnv := false
	for _, arg := range container.Args {
		if arg == "-config.expand-env=true" {
			expandEnv = true
		}
	}
	if !expandEnv {
		t.Fatal("promtail must run with -config.expand-env=true so ${HOSTNAME} expands")
	}

	const node = "worker-1"
	job := kubernetesPodsJob(t, map[string]string{"HOSTNAME": node})
	if len(job.KubernetesSDConfigs) != 1 || job.KubernetesSDConfigs[0].Role != "pod" {
		t.Fatalf("kubernetes-pods job must use one pod-role discovery config, got %+v", job.KubernetesSDConfigs)
	}
	sd := job.KubernetesSDConfigs[0]
	if sd.Namespaces != nil {
		t.Fatalf("kubernetes discovery must cover all namespaces, got namespaces=%v", sd.Namespaces)
	}
	want := "spec.nodeName=" + node
	found := false
	for _, sel := range sd.Selectors {
		if sel.Role != "pod" {
			t.Fatalf("selector role must be pod, got %q", sel.Role)
		}
		if sel.Field == want {
			found = true
		} else if sel.Field != "" {
			t.Fatalf("unexpected pod selector field %q; only %q is allowed", sel.Field, want)
		}
	}
	if !found {
		t.Fatalf("kubernetes-pods discovery must select %q, got %+v", want, sd.Selectors)
	}
}

func TestPromtailRelabelBuildsCRIPathAndWorkloadLabels(t *testing.T) {
	const (
		node = "worker-1"
		uid  = "4f8c1e2a-1111-2222-3333-444455556666"
	)
	job := kubernetesPodsJob(t, map[string]string{"HOSTNAME": node})

	cases := []struct {
		name    string
		meta    map[string]string
		wantApp string
	}{
		{
			name: "mcp server pod with operator app label",
			meta: map[string]string{
				"__meta_kubernetes_namespace":           "mcp-servers",
				"__meta_kubernetes_pod_name":            "buddy-7d9f8-abcde",
				"__meta_kubernetes_pod_uid":             uid,
				"__meta_kubernetes_pod_container_name":  "buddy",
				"__meta_kubernetes_pod_node_name":       node,
				"__meta_kubernetes_pod_phase":           "Running",
				"__meta_kubernetes_pod_label_app":       "buddy",
				"__meta_kubernetes_pod_controller_name": "buddy-7d9f8",
			},
			wantApp: "buddy",
		},
		{
			name: "team namespace pod with recommended name label",
			meta: map[string]string{
				"__meta_kubernetes_namespace":                        "team-alpha",
				"__meta_kubernetes_pod_name":                         "tools-0",
				"__meta_kubernetes_pod_uid":                          uid,
				"__meta_kubernetes_pod_container_name":               "server",
				"__meta_kubernetes_pod_node_name":                    node,
				"__meta_kubernetes_pod_phase":                        "Running",
				"__meta_kubernetes_pod_label_app_kubernetes_io_name": "tools",
				"__meta_kubernetes_pod_controller_name":              "tools",
			},
			wantApp: "tools",
		},
		{
			name: "pod without app labels falls back to controller",
			meta: map[string]string{
				"__meta_kubernetes_namespace":           "kube-system",
				"__meta_kubernetes_pod_name":            "svclb-traefik-xyz",
				"__meta_kubernetes_pod_uid":             uid,
				"__meta_kubernetes_pod_container_name":  "lb-tcp-80",
				"__meta_kubernetes_pod_node_name":       node,
				"__meta_kubernetes_pod_phase":           "Pending",
				"__meta_kubernetes_pod_controller_name": "svclb-traefik",
			},
			wantApp: "svclb-traefik",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			labels := applyPromtailRelabel(t, job.RelabelConfigs, tc.meta)
			if labels == nil {
				t.Fatal("live pod target was dropped")
			}
			wantLabels := map[string]string{
				"namespace": tc.meta["__meta_kubernetes_namespace"],
				"pod":       tc.meta["__meta_kubernetes_pod_name"],
				"container": tc.meta["__meta_kubernetes_pod_container_name"],
				"node":      node,
				"app":       tc.wantApp,
				"__host__":  node,
			}
			for k, want := range wantLabels {
				if labels[k] != want {
					t.Errorf("label %s = %q, want %q (labels %v)", k, labels[k], want, labels)
				}
			}

			path := labels["__path__"]
			container := tc.meta["__meta_kubernetes_pod_container_name"]
			if want := "/var/log/pods/*" + uid + "/" + container + "/*.log"; path != want {
				t.Fatalf("__path__ = %q, want %q", path, want)
			}
			// CRI layout used by containerd, k3s and Kind:
			// /var/log/pods/<namespace>_<pod>_<uid>/<container>/<restart>.log
			podDir := "/var/log/pods/" + tc.meta["__meta_kubernetes_namespace"] + "_" + tc.meta["__meta_kubernetes_pod_name"] + "_" + uid
			for _, file := range []string{"0.log", "3.log"} {
				ok, err := filepath.Match(path, podDir+"/"+container+"/"+file)
				if err != nil || !ok {
					t.Errorf("__path__ %q does not match CRI log file %s (err %v)", path, file, err)
				}
			}
			for _, other := range []string{
				podDir + "/other-container/0.log",
				"/var/log/pods/" + tc.meta["__meta_kubernetes_namespace"] + "_x_other-uid/" + container + "/0.log",
				podDir + "/" + container + "/0.log.20261006-101010.gz",
			} {
				if ok, _ := filepath.Match(path, other); ok {
					t.Errorf("__path__ %q must not match %s", path, other)
				}
			}
		})
	}

	for _, phase := range []string{"Succeeded", "Failed"} {
		meta := map[string]string{
			"__meta_kubernetes_pod_uid":            uid,
			"__meta_kubernetes_pod_container_name": "job",
			"__meta_kubernetes_pod_phase":          phase,
		}
		if applyPromtailRelabel(t, job.RelabelConfigs, meta) != nil {
			t.Errorf("%s pod targets must be dropped", phase)
		}
	}
}

func TestPromtailPipelineParsesCRIAndRedactsSecrets(t *testing.T) {
	configs := loadPromtailScrapeConfigs(t, map[string]string{"HOSTNAME": "worker-1"})
	if len(configs) == 0 {
		t.Fatal("promtail has no scrape configs")
	}
	for _, sc := range configs {
		if len(sc.StaticConfigs) > 0 {
			t.Errorf("job %q uses static_configs; path-based jobs duplicate discovered streams and hide discovery gaps", sc.JobName)
		}
		if len(sc.PipelineStages) == 0 {
			t.Fatalf("job %q has no pipeline stages", sc.JobName)
		}
		if _, ok := sc.PipelineStages[0]["cri"]; !ok {
			t.Errorf("job %q must parse the CRI envelope first, got %v", sc.JobName, sc.PipelineStages[0])
		}

		var redactors []*regexp.Regexp
		var replacements []string
		for _, stage := range sc.PipelineStages {
			replace, ok := stage["replace"].(map[string]any)
			if !ok {
				continue
			}
			expr, _ := replace["expression"].(string)
			repl, _ := replace["replace"].(string)
			re, err := regexp.Compile(expr)
			if err != nil {
				t.Fatalf("job %q redaction expression %q does not compile: %v", sc.JobName, expr, err)
			}
			redactors = append(redactors, re)
			replacements = append(replacements, repl)
		}
		for _, line := range []string{
			`Authorization: Bearer eyJhbGciOiJSUzI1NiJ9.payload.sig`,
			`{"access_token":"tok-abc","client_secret":"cs-123"}`,
		} {
			out := line
			for i, re := range redactors {
				out = re.ReplaceAllString(out, replacements[i])
			}
			for _, secret := range []string{"eyJhbGciOiJSUzI1NiJ9", "tok-abc", "cs-123"} {
				if strings.Contains(out, secret) {
					t.Errorf("job %q leaks %q after redaction: %q", sc.JobName, secret, out)
				}
			}
			if !strings.Contains(out, "[REDACTED]") {
				t.Errorf("job %q did not redact %q: %q", sc.JobName, line, out)
			}
		}
	}
}

func TestPromtailMountsOnlyPodLogs(t *testing.T) {
	ds := loadPromtailDaemonSet(t)
	hostPaths := map[string]string{}
	for _, v := range ds.Spec.Template.Spec.Volumes {
		if v.HostPath != nil {
			hostPaths[v.Name] = v.HostPath.Path
		}
	}
	podLogsMounted := false
	for _, m := range ds.Spec.Template.Spec.Containers[0].VolumeMounts {
		hostPath, ok := hostPaths[m.Name]
		if !ok {
			continue
		}
		if hostPath != "/var/log/pods" {
			t.Errorf("promtail mounts host path %q; only /var/log/pods is needed for pod logs", hostPath)
		}
		if !m.ReadOnly {
			t.Errorf("host path mount %q must be read-only", m.MountPath)
		}
		if m.MountPath == "/var/log/pods" {
			podLogsMounted = true
		}
	}
	if !podLogsMounted {
		t.Fatal("promtail must mount host /var/log/pods at /var/log/pods to match discovered __path__ globs")
	}
}

func TestPromtailConfigEscapesDollarsForExpandEnv(t *testing.T) {
	cfg, _ := loadPromtailConfig(t)
	// With env expansion enabled, bare capture references would expand to empty.
	for _, line := range strings.Split(cfg, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "#") {
			continue
		}
		for _, ref := range []string{"$1", "${1}", "${2}"} {
			escaped := "$" + ref
			if strings.Contains(strings.ReplaceAll(trim, escaped, ""), ref) {
				t.Fatalf("unescaped capture reference %s under -config.expand-env: %q", ref, trim)
			}
		}
	}
}

func TestPromtailManifestUsesCollectorNamespace(t *testing.T) {
	content, err := os.ReadFile(promtailManifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(content), "namespace: mcp-log-collector") < 4 {
		t.Fatal("promtail resources must use the collector namespace")
	}
	if !strings.Contains(string(content), "url: http://loki.mcp-observability.svc:3100/loki/api/v1/push") {
		t.Fatal("promtail must push to Loki in the observability namespace")
	}
}
