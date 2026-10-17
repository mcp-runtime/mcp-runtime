package update

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes/fake"
	"sigs.k8s.io/yaml"

	"mcp-runtime/internal/cli/setup/ingressmanifest"
	"mcp-runtime/internal/platformrelease"
)

func audienceMigrationFixture(t *testing.T) []runtime.Object {
	t.Helper()
	var objects []runtime.Object
	decoder := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(ingressmanifest.RenderPlatformUIIngress("platform.example.test", "", false, "mcp-platform", "mcp-observability")), 4096)
	for {
		var ingress networkingv1.Ingress
		err := decoder.Decode(&ingress)
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		objects = append(objects, &ingress)
	}
	controller := deployment("traefik", "traefik", "traefik", "traefik:v3.7.14", nil)
	container := &controller.Spec.Template.Spec.Containers[0]
	container.Args = []string{"--providers.kubernetesingress", "--providers.file.watch=true", "--providers.file.filename=/etc/traefik/dynamic/dynamic.yml"}
	container.VolumeMounts = []corev1.VolumeMount{{Name: "dynamic", MountPath: "/etc/traefik/dynamic", ReadOnly: true}}
	controller.Spec.Template.Spec.Volumes = []corev1.Volume{{Name: "dynamic", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "traefik-dynamic"}}}}}
	raw, err := os.ReadFile("../../../config/ingress/base/dynamic-config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var config corev1.ConfigMap
	if err := yaml.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	return append(objects, controller, &config)
}

func TestAudienceExchangeUpdatePreflightReadsRealWiring(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func([]runtime.Object) []runtime.Object
		allowed bool
	}{
		{"migrated", func(o []runtime.Object) []runtime.Object { return o }, true},
		{"flag-without-migration", func([]runtime.Object) []runtime.Object {
			return []runtime.Object{&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-shared-config", Namespace: "mcp-platform"}, Data: map[string]string{"TOKEN_AUDIENCE_EXCHANGE": "true"}}}
		}, false},
		{"missing-runtime-ingress", func(o []runtime.Object) []runtime.Object {
			return removeAudienceObject(o, ingressmanifest.PlatformRuntimeIngressName)
		}, false},
		{"missing-middleware", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if in, ok := object.(*networkingv1.Ingress); ok && in.Name == ingressmanifest.PlatformRuntimeIngressName {
					delete(in.Annotations, "traefik.ingress.kubernetes.io/router.middlewares")
				}
			}
			return o
		}, false},
		{"platform-route-exchanged", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if in, ok := object.(*networkingv1.Ingress); ok && in.Name == ingressmanifest.PlatformIngressName {
					in.Annotations["traefik.ingress.kubernetes.io/router.middlewares"] = "runtime-token-exchange@file"
				}
			}
			return o
		}, false},
		{"wrong-backend", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if in, ok := object.(*networkingv1.Ingress); ok && in.Name == ingressmanifest.PlatformRuntimeIngressName {
					in.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "mcp-platform-api"
				}
			}
			return o
		}, false},
		{"duplicate-route-with-unrelated-backend", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if in, ok := object.(*networkingv1.Ingress); ok && in.Name == ingressmanifest.PlatformRuntimeIngressName {
					duplicate := in.DeepCopy()
					duplicate.Name = "legacy-runtime-duplicate"
					duplicate.Spec.Rules[0].HTTP.Paths[0].Backend.Service.Name = "mcp-ui"
					return append(o, duplicate)
				}
			}
			return o
		}, false},
		{"controller-file-not-watched", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if d, ok := object.(*appsv1.Deployment); ok {
					d.Spec.Template.Spec.Containers[0].Args = []string{"--providers.kubernetesingress=true"}
				}
			}
			return o
		}, false},
		{"only-platform-namespace-watched", restrictAudienceNamespaces("mcp-platform"), false},
		{"only-analytics-namespace-watched", restrictAudienceNamespaces("mcp-observability"), false},
		{"both-api-namespaces-watched", restrictAudienceNamespaces("mcp-platform,mcp-observability"), true},
		{"configmap-not-mounted", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if d, ok := object.(*appsv1.Deployment); ok {
					d.Spec.Template.Spec.Volumes = nil
				}
			}
			return o
		}, false},
		{"middleware-target-swap", replaceAudienceConfig("/exchange/runtime", "/exchange/analytics"), false},
		{"trusts-forwarded-headers", replaceAudienceConfig("trustForwardHeader: false", "trustForwardHeader: true"), false},
		{"body-forwarding-enabled", replaceAudienceConfig("forwardBody: false", "forwardBody: true"), false},
		{"source-key-not-stripped", replaceAudienceConfig("authResponseHeaders:\n          - Authorization\n          - X-Api-Key", "authResponseHeaders:\n          - Authorization"), false},
		{"internal-route-exposed", func(o []runtime.Object) []runtime.Object {
			for _, object := range o {
				if in, ok := object.(*networkingv1.Ingress); ok && in.Name == ingressmanifest.PlatformIngressName {
					route := in.Spec.Rules[0].HTTP.Paths[0]
					route.Path = "/internal"
					in.Spec.Rules[0].HTTP.Paths = append(in.Spec.Rules[0].HTTP.Paths, route)
				}
			}
			return o
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			objects := tc.mutate(audienceMigrationFixture(t))
			cs := fake.NewClientset(objects...)
			err := requireAudienceExchangeMigration(context.Background(), cs)
			if (err == nil) != tc.allowed {
				t.Fatalf("allowed=%t err=%v", tc.allowed, err)
			}
			if err != nil && (!strings.Contains(err.Error(), "mcp-runtime setup") || !strings.Contains(err.Error(), "no update changes were made")) {
				t.Fatalf("missing recovery contract: %v", err)
			}
			for _, action := range cs.Actions() {
				if action.GetVerb() != "get" && action.GetVerb() != "list" {
					t.Fatalf("preflight mutated cluster: %v", action)
				}
			}
		})
	}
}

func removeAudienceObject(objects []runtime.Object, name string) []runtime.Object {
	var out []runtime.Object
	for _, object := range objects {
		if in, ok := object.(*networkingv1.Ingress); !ok || in.Name != name {
			out = append(out, object)
		}
	}
	return out
}

func restrictAudienceNamespaces(namespaces string) func([]runtime.Object) []runtime.Object {
	return func(objects []runtime.Object) []runtime.Object {
		for _, object := range objects {
			if deployment, ok := object.(*appsv1.Deployment); ok {
				deployment.Spec.Template.Spec.Containers[0].Args = append(deployment.Spec.Template.Spec.Containers[0].Args, "--providers.kubernetesingress.namespaces="+namespaces)
			}
		}
		return objects
	}
}

func replaceAudienceConfig(old, new string) func([]runtime.Object) []runtime.Object {
	return func(objects []runtime.Object) []runtime.Object {
		for _, object := range objects {
			if cm, ok := object.(*corev1.ConfigMap); ok {
				cm.Data["dynamic.yml"] = strings.ReplaceAll(cm.Data["dynamic.yml"], old, new)
			}
		}
		return objects
	}
}

func TestExchangeRequiredManifestBlocksUpdateBeforeAnyMutation(t *testing.T) {
	for _, migrated := range []bool{false, true} {
		objects := installed("v0.4.0")
		if migrated {
			objects = append(objects, audienceMigrationFixture(t)...)
		}
		cs := fake.NewClientset(objects...)
		m := manifest(t, "v0.5.0", func(m *platformrelease.Manifest) {
			m.RequiresAudienceExchange = true
			m.ConfigMaps = []platformrelease.ManifestConfigMap{{Namespace: "mcp-platform", Name: "must-not-write", Data: map[string]string{"key": "value"}}}
		})
		plan, err := BuildPlan(t.Context(), cs, m, Selection{})
		if (err == nil) != migrated || migrated && plan == nil {
			t.Fatalf("migrated=%t plan=%v err=%v", migrated, plan, err)
		}
		for _, action := range cs.Actions() {
			if action.GetVerb() != "get" && action.GetVerb() != "list" {
				t.Fatalf("update wrote before preflight: %v", action)
			}
		}
		if !migrated {
			if _, err := BuildPlan(t.Context(), cs, m, Selection{Only: []string{"ui"}}); err == nil {
				t.Fatal("component selection bypassed migration guard")
			}
		}
	}
}

func TestReleaseAudienceRequirementSurvivesStrictRoundTrip(t *testing.T) {
	m, err := platformrelease.GenerateReleaseManifest(platformrelease.ReleaseOptions{Version: "v9.9.9", Commit: strings.Repeat("a", 40), K8sDir: "../../../k8s", WiringPath: "../../../config/release/wiring.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := platformrelease.ParseManifest(bytes.TrimSpace(data))
	if err != nil || !parsed.RequiresAudienceExchange {
		t.Fatalf("marker lost in generated release: %v", err)
	}
}
