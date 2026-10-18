package credentialapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesProviderExactNamespaceMissingKeyAndRotation(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "team-one", Name: "upstream"}, Data: map[string][]byte{"token": []byte("fixture-value-one")}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "foreign", Name: "upstream"}, Data: map[string][]byte{"token": []byte("must-not-escape")}}).Build()
	provider := Providers(kube)["kubernetes"]
	source := cb.Source{Namespace: "team-one", Resource: "upstream", Key: "token"}
	value, err := provider.Read(t.Context(), source)
	if err != nil || string(value.Bytes) != "fixture-value-one" {
		t.Fatal("same-namespace read failed")
	}
	if err = kube.Get(t.Context(), types.NamespacedName{Namespace: secret.Namespace, Name: secret.Name}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["token"] = []byte("fixture-value-two")
	if err = kube.Update(t.Context(), secret); err != nil {
		t.Fatal(err)
	}
	value, err = provider.Read(t.Context(), source)
	if err != nil || string(value.Bytes) != "fixture-value-two" {
		t.Fatal("rotated provider value was cached")
	}
	for _, mutate := range []func(*cb.Source){func(s *cb.Source) { s.Namespace = "missing" }, func(s *cb.Source) { s.Key = "missing" }, func(s *cb.Source) { s.Resource = "foreign/upstream" }} {
		candidate := source
		mutate(&candidate)
		value, err = provider.Read(t.Context(), candidate)
		if !errors.Is(err, cb.ErrUnavailable) || len(value.Bytes) != 0 || strings.Contains(err.Error(), "fixture") {
			t.Fatal("provider failure leaked or fell back across namespace")
		}
	}
}

func TestCloudTransportRequiresVerifiedHTTPSAndRedactsErrors(t *testing.T) {
	var calls int
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Authorization") != "Bearer fixture-token" {
				t.Error("provider credential missing")
			}
			_, _ = w.Write([]byte(`{"name":"version-2"}`))
		case "/redirect":
			http.Redirect(w, r, "/ok", http.StatusFound)
		default:
			http.Error(w, "raw-sensitive-provider-response", http.StatusForbidden)
		}
	}))
	defer server.Close()
	var result map[string]any
	if err := providerJSON(t.Context(), http.DefaultClient, server.URL+"/ok", "Authorization", "Bearer fixture-token", &result); !errors.Is(err, cb.ErrUnavailable) {
		t.Fatal("untrusted TLS accepted")
	}
	client := server.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if err := providerJSON(t.Context(), client, server.URL+"/ok", "Authorization", "Bearer fixture-token", &result); err != nil || result["name"] != "version-2" {
		t.Fatal("verified provider response failed")
	}
	for _, path := range []string{"/missing", "/redirect"} {
		if err := providerJSON(t.Context(), client, server.URL+path, "Authorization", "Bearer fixture-token", &result); !errors.Is(err, cb.ErrUnavailable) || strings.Contains(err.Error(), "sensitive") {
			t.Fatal("provider error/redirect leaked")
		}
	}
	before := calls
	if providerJSON(context.Background(), client, "http://untrusted.test/secret", "Authorization", "Bearer fixture-token", &result) == nil || calls != before {
		t.Fatal("plaintext provider accepted")
	}
}

func TestManagementUnknownFieldsNeverEchoValues(t *testing.T) {
	h := Handler{Store: &metadataStub{}}
	for _, kind := range []string{"sources", "bindings"} {
		r := httptest.NewRequest(http.MethodPut, cb.ManagementPath+"/team/"+kind+"/upstream", strings.NewReader(`{"value":"sensitive-marker"}`))
		r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Role: "admin", IsService: true}))
		w := httptest.NewRecorder()
		h.Management(w, r)
		if w.Code != http.StatusBadRequest || strings.Contains(w.Body.String(), "sensitive-marker") {
			t.Fatal("unknown raw credential accepted or reflected")
		}
	}
}

type metadataStub struct{}

func (*metadataStub) ListCredentials(context.Context, platformauth.Principal, string, string) ([]json.RawMessage, error) {
	return nil, nil
}
func (*metadataStub) PutCredential(context.Context, platformauth.Principal, *cb.Source, *cb.Binding) (json.RawMessage, error) {
	return nil, nil
}
func (*metadataStub) DeleteCredential(context.Context, platformauth.Principal, string, string, string, int64) error {
	return nil
}
func (*metadataStub) UseCredentials(context.Context, sessionstate.Server, cb.ResolutionRequest, func(sessionstate.Session, []cb.Binding, []cb.Source) error, func(sessionstate.Session, []cb.Binding, []cb.Source) (cb.Delivery, error)) (cb.Delivery, error) {
	return cb.Delivery{}, nil
}
