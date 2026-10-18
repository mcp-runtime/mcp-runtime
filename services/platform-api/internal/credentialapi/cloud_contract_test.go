package credentialapi

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	azurepolicy "github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"
	"golang.org/x/oauth2"
	cb "mcp-runtime/pkg/credentialbroker"
)

// Re-route only the exact public data API host to a verified local HTTPS peer.
// Unexpected provider/identity destinations fail instead of touching a cloud.
type cloudPeerTransport struct {
	host string
	peer *url.URL
	base http.RoundTripper
}

func (t cloudPeerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Host != t.host {
		return nil, errors.New("unexpected fixture destination")
	}
	copy := r.Clone(r.Context())
	u := *r.URL
	u.Scheme, u.Host = t.peer.Scheme, t.peer.Host
	copy.URL = &u
	copy.Host = t.peer.Host
	return t.base.RoundTrip(copy)
}

type azureFixtureCredential struct{ t *testing.T }

func (c azureFixtureCredential) GetToken(_ context.Context, options azurepolicy.TokenRequestOptions) (azcore.AccessToken, error) {
	if len(options.Scopes) != 1 || options.Scopes[0] != "https://vault.azure.net/.default" {
		c.t.Error("Azure data token audience changed")
	}
	return azcore.AccessToken{Token: "fixture-azure-access", ExpiresOn: time.Now().Add(time.Hour)}, nil
}

func TestAzureProviderUsesExactVaultSecretAndScopedToken(t *testing.T) {
	var reads atomic.Int64
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" {
			w.Header().Set("WWW-Authenticate", `Bearer authorization="https://login.microsoftonline.com/11111111-1111-1111-1111-111111111111", resource="https://vault.azure.net"`)
			w.WriteHeader(401)
			return
		}
		if r.Header.Get("Authorization") != "Bearer fixture-azure-access" || strings.TrimSuffix(r.URL.Path, "/") != "/secrets/upstream" || r.URL.Query().Get("api-version") == "" {
			t.Error("Azure resource, scope or version changed")
		}
		count := reads.Add(1)
		_, _ = fmt.Fprintf(w, `{"value":"fixture-azure-value-%d","id":"https://fixture.vault.azure.net/secrets/upstream/version%d","attributes":{"enabled":true}}`, count, count)
	}))
	defer peer.Close()
	u, _ := url.Parse(peer.URL)
	client := &http.Client{Transport: cloudPeerTransport{host: "fixture.vault.azure.net", peer: u, base: peer.Client().Transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for i := 1; i <= 2; i++ {
		value, err := azureValue(t.Context(), cb.Source{Resource: "https://fixture.vault.azure.net/secrets/upstream"}, azureFixtureCredential{t}, client)
		if err != nil || string(value.Bytes) != fmt.Sprintf("fixture-azure-value-%d", i) {
			t.Fatal("Azure latest-version read/rotation failed")
		}
	}
}

func TestGCPProviderUsesADCExactVersionAndObservesRotation(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var reads, tokens atomic.Int64
	resource := "projects/fixture-project/secrets/upstream"
	var peer *httptest.Server
	peer = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokens.Add(1)
			if err := r.ParseForm(); err != nil {
				t.Error(err)
			}
			parsed, err := jwt.Parse(r.Form.Get("assertion"), func(token *jwt.Token) (any, error) {
				if token.Method.Alg() != "RS256" {
					return nil, errors.New("wrong signing algorithm")
				}
				return &key.PublicKey, nil
			}, jwt.WithValidMethods([]string{"RS256"}))
			if err != nil || parsed == nil || !parsed.Valid {
				t.Error("ADC assertion was not cryptographically valid")
				http.Error(w, "invalid fixture assertion", http.StatusUnauthorized)
				return
			}
			claims, _ := parsed.Claims.(jwt.MapClaims)
			if !claims.VerifyAudience(peer.URL+"/token", true) || claims["iss"] != "fixture@fixture-project.iam.gserviceaccount.com" {
				t.Error("ADC assertion issuer/audience mismatch")
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"access_token":"fixture-gcp-access","token_type":"Bearer","expires_in":3600}`))
			return
		}
		if r.URL.Path != "/v1/"+resource+"/versions/latest:access" || r.Header.Get("Authorization") != "Bearer fixture-gcp-access" {
			t.Error("GCP reference, version or auth changed")
		}
		count := reads.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"name": resource + fmt.Sprintf("/versions/%d", count), "payload": map[string]string{"data": base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("fixture-gcp-value-%d", count)))}})
	}))
	defer peer.Close()
	credentials := map[string]string{"type": "service_account", "project_id": "fixture-project", "private_key_id": "fixture-key", "private_key": string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})), "client_email": "fixture@fixture-project.iam.gserviceaccount.com", "token_uri": peer.URL + "/token"}
	file := filepath.Join(t.TempDir(), "adc.json")
	raw, _ := json.Marshal(credentials)
	if err = os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOOGLE_APPLICATION_CREDENTIALS", file)
	peerURL, _ := url.Parse(peer.URL)
	client := &http.Client{Transport: cloudPeerTransport{host: "secretmanager.googleapis.com", peer: peerURL, base: peer.Client().Transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider := cloudReaders(client)["gcp"]
	ctx := context.WithValue(t.Context(), oauth2.HTTPClient, peer.Client())
	for i := 1; i <= 2; i++ {
		value, err := provider.Read(ctx, cb.Source{Resource: resource})
		if err != nil || string(value.Bytes) != fmt.Sprintf("fixture-gcp-value-%d", i) {
			t.Fatal("ADC/version read or rotation failed")
		}
	}
	if reads.Load() != 2 || tokens.Load() != 2 {
		t.Fatal("provider reused a secret value instead of reading current version")
	}
}

func TestAWSProviderUsesAmbientSigningExactARNAndSafeFailure(t *testing.T) {
	for name, value := range map[string]string{"AWS_ACCESS_KEY_ID": "fixture-access-key", "AWS_SECRET_ACCESS_KEY": "fixture-signing-material", "AWS_EC2_METADATA_DISABLED": "true", "AWS_PROFILE": "", "AWS_CONFIG_FILE": filepath.Join(t.TempDir(), "absent"), "AWS_SHARED_CREDENTIALS_FILE": filepath.Join(t.TempDir(), "absent"), "AWS_ENDPOINT_URL": "", "AWS_ENDPOINT_URL_SECRETS_MANAGER": ""} {
		t.Setenv(name, value)
	}
	arn := "arn:aws:secretsmanager:us-east-1:123456789012:secret:upstream-Abc123"
	deny := false
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") || !strings.Contains(r.Header.Get("Authorization"), "Credential=fixture-access-key/") || r.Header.Get("X-Amz-Target") != "secretsmanager.GetSecretValue" {
			t.Error("AWS ambient signing missing")
		}
		var input struct {
			SecretID     string `json:"SecretId"`
			VersionStage string `json:"VersionStage"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil || input.SecretID != arn || input.VersionStage != "AWSCURRENT" {
			t.Error("AWS exact resource or version changed")
		}
		if deny {
			w.WriteHeader(403)
			_, _ = w.Write([]byte(`{"__type":"AccessDeniedException","message":"sensitive-provider-body"}`))
			return
		}
		_, _ = w.Write([]byte(`{"SecretString":"fixture-aws-value","VersionId":"fixture-version"}`))
	}))
	defer peer.Close()
	u, _ := url.Parse(peer.URL)
	client := &http.Client{Transport: cloudPeerTransport{host: "secretsmanager.us-east-1.amazonaws.com", peer: u, base: peer.Client().Transport}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	provider := cloudReaders(client)["aws"]
	source := cb.Source{Resource: arn, Version: "AWSCURRENT"}
	value, err := provider.Read(t.Context(), source)
	if err != nil || string(value.Bytes) != "fixture-aws-value" {
		t.Fatal("AWS signed read failed")
	}
	deny = true
	value, err = provider.Read(t.Context(), source)
	if !errors.Is(err, cb.ErrUnavailable) || len(value.Bytes) != 0 || strings.Contains(err.Error(), "sensitive") {
		t.Fatal("AWS failure leaked provider body or value")
	}
}

func TestVaultProviderUsesProjectedWorkloadLoginExactKVPathAndRotation(t *testing.T) {
	var logins, reads atomic.Int64
	peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/kubernetes/login":
			logins.Add(1)
			var input map[string]string
			if json.NewDecoder(r.Body).Decode(&input) != nil || input["role"] != "broker" || input["jwt"] != "fixture-vault-workload-jwt" {
				t.Error("Vault workload binding missing")
			}
			_, _ = w.Write([]byte(`{"auth":{"client_token":"fixture-vault-access"}}`))
		case "/v1/secret/data/team/upstream":
			if r.Header.Get("X-Vault-Token") != "fixture-vault-access" || r.URL.Query().Get("version") != "" {
				t.Error("Vault exact source authentication/version changed")
			}
			count := reads.Add(1)
			_, _ = fmt.Fprintf(w, `{"data":{"data":{"token":"fixture-vault-value-%d"},"metadata":{"version":%d,"destroyed":false,"deletion_time":""}}}`, count, count)
		default:
			t.Error("unexpected Vault endpoint")
			http.NotFound(w, r)
		}
	}))
	defer peer.Close()
	file := filepath.Join(t.TempDir(), "projected-jwt")
	_ = os.WriteFile(file, []byte("fixture-vault-workload-jwt"), 0600)
	t.Setenv("CREDENTIAL_VAULT_ADDR", peer.URL)
	t.Setenv("CREDENTIAL_VAULT_ROLE", "broker")
	t.Setenv("CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE", file)
	client := peer.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	provider := cloudReaders(client)["vault"]
	for i := 1; i <= 2; i++ {
		value, err := provider.Read(t.Context(), cb.Source{Resource: "secret/team/upstream", Key: "token"})
		if err != nil || string(value.Bytes) != fmt.Sprintf("fixture-vault-value-%d", i) {
			t.Fatal("Vault version/rotation failed")
		}
	}
	if logins.Load() != 2 || reads.Load() != 2 {
		t.Fatal("Vault provider did not refresh workload auth/current value")
	}
	t.Setenv("CREDENTIAL_VAULT_ROLE", "")
	if _, err := provider.Read(t.Context(), cb.Source{Resource: "secret/team/upstream", Key: "token"}); !errors.Is(err, cb.ErrUnavailable) {
		t.Fatal("missing workload role accepted")
	}
}
