package credentialapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	cb "mcp-runtime/pkg/credentialbroker"
	"mcp-runtime/pkg/platformauth"
)

func TestProvidersRegistersOnlyKubernetes(t *testing.T) {
	got := Providers(nil)
	if len(got) != 1 || got["kubernetes"] == nil {
		t.Fatalf("only the kubernetes reader may be registered, got %d readers", len(got))
	}
}

func TestManagementRefusesCloudSourcesWithReadableError(t *testing.T) {
	f := newFixture(t, nil)
	for _, body := range []string{
		`{"namespace":"ns","name":"src","provider":"gcp","resource":"projects/p/secrets/s","user_id":"` + hID + `"}`,
		`{"namespace":"ns","name":"src","provider":"aws","resource":"arn:aws:secretsmanager:us-east-1:123456789012:secret:s-Abc123","user_id":"` + hID + `"}`,
		`{"namespace":"ns","name":"src","provider":"azure","resource":"https://v.vault.azure.net/secrets/s","user_id":"` + hID + `"}`,
		`{"namespace":"ns","name":"src","provider":"vault","resource":"secret/a","key":"k","user_id":"` + hID + `"}`,
	} {
		r := httptest.NewRequest(http.MethodPut, cb.ManagementPath+"/ns/sources/src", strings.NewReader(body))
		r = r.WithContext(platformauth.WithPrincipal(r.Context(), platformauth.Principal{Subject: hID}))
		w := httptest.NewRecorder()
		f.handler.Management(w, r)
		if w.Code != http.StatusUnprocessableEntity || !strings.Contains(w.Body.String(), "not yet supported") || f.store.put {
			t.Fatalf("cloud source must be refused before the store, got %d %q", w.Code, w.Body.String())
		}
	}
}
