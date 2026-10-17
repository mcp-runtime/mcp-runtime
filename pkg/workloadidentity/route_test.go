package workloadidentity

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestSelectedTLSOptionFollowsActualRoute(t *testing.T) {
	for _, tc := range []struct {
		name                        string
		tls                         any
		defaultNS, wantNS, wantName string
		wantError                   bool
	}{
		{name: "explicit override", tls: map[string]any{"options": map[string]any{"name": "no-client-auth", "namespace": "other"}}, defaultNS: "traefik", wantNS: "other", wantName: "no-client-auth"},
		{name: "local option", tls: map[string]any{"options": map[string]any{"name": "server-mtls"}}, wantNS: "team", wantName: "server-mtls"},
		{name: "global default", tls: map[string]any{}, defaultNS: "traefik", wantNS: "traefik", wantName: "default"},
		{name: "unknown default", tls: map[string]any{}, wantError: true},
		{name: "plain route", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": "team"}, "spec": map[string]any{}}}
			if tc.tls != nil {
				obj.Object["spec"].(map[string]any)["tls"] = tc.tls
			}
			ns, name, err := SelectedTLSOption(obj, tc.defaultNS)
			if (err != nil) != tc.wantError || (err == nil && (ns != tc.wantNS || name != tc.wantName)) {
				t.Fatalf("option=%s/%s err=%v", ns, name, err)
			}
		})
	}
	for _, mode := range []string{"NoClientCert", "RequestClientCert", "RequireAnyClientCert", "VerifyClientCertIfGiven", "RequireAndVerifyClientCert"} {
		option := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"clientAuth": map[string]any{"clientAuthType": mode, "secretNames": []any{"bundle"}}}}}
		_, err := ClientAuthCASecrets(option)
		want := mode == "VerifyClientCertIfGiven" || mode == "RequireAndVerifyClientCert"
		if (err == nil) != want {
			t.Fatalf("mode=%s error=%v", mode, err)
		}
	}
}
