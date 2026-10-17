package platform

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

func productionIssuer(secretName string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1", "kind": "ClusterIssuer",
		"metadata": map[string]any{"name": "workload", "generation": int64(2)},
		"spec":     map[string]any{"ca": map[string]any{"secretName": secretName}},
		"status":   map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": int64(2)}}},
	}}
}

func TestProductionWorkloadIssuerCheckedBeforeTLSMutation(t *testing.T) {
	now := time.Now()
	goodC, goodK := caPEM(t, now.AddDate(5, 0, 0))
	otherC, otherK := caPEM(t, now.AddDate(5, 0, 0))
	nearC, nearK := caPEM(t, now.AddDate(0, 0, 30))
	expiredC, expiredK := caPEM(t, now.Add(-time.Hour))
	sharedIntermediate, sharedIntermediateKey := intermediateCA(t, otherC, otherK)
	separateIntermediate, separateIntermediateKey := intermediateCA(t, goodC, goodK)
	resignedIntermediate := selfSignedCAFromKey(t, sharedIntermediateKey)
	cases := []struct {
		name                      string
		issuer                    string
		tlsIssuer                 string
		cert, key                 []byte
		registryCert, registryKey []byte
		modify                    func(*unstructured.Unstructured)
		want                      string
	}{
		{name: "valid separate root", cert: goodC, key: goodK},
		{name: "same issuer as internal registry", tlsIssuer: "workload", cert: goodC, key: goodK, want: "separate signing roots"},
		{name: "selected enterprise registry root copied", tlsIssuer: "registry", cert: goodC, key: goodK, want: "share a signing key"},
		{name: "registry intermediate key reused under independent root", tlsIssuer: "registry", cert: resignedIntermediate, key: sharedIntermediateKey, registryCert: append(append([]byte{}, sharedIntermediate...), otherC...), registryKey: sharedIntermediateKey, want: "share a signing key"},
		{name: "shared name even with healthy root", issuer: "mcp-runtime-ca", cert: goodC, key: goodK, want: "test-only"},
		{name: "alias of registry Secret", cert: goodC, key: goodK, modify: func(i *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(i.Object, "mcp-runtime-ca", "spec", "ca", "secretName")
		}, want: "shares the bundled registry"},
		{name: "renamed copy of registry root", cert: otherC, key: otherK, want: "share a signing key"},
		{name: "shared root through intermediate", cert: append(append([]byte{}, sharedIntermediate...), otherC...), key: sharedIntermediateKey, want: "share a signing key"},
		{name: "omitted intermediate parent cannot hide root", cert: sharedIntermediate, key: sharedIntermediateKey, want: "complete CA chain"},
		{name: "independent full intermediate chain", cert: append(append([]byte{}, separateIntermediate...), goodC...), key: separateIntermediateKey},
		{name: "missing root cannot rekey", want: "read dedicated workload CA"},
		{name: "expired root", cert: expiredC, key: expiredK, want: "invalid"},
		{name: "near expiry", cert: nearC, key: nearK, want: "under 180 days"},
		{name: "key mismatch", cert: goodC, key: otherK, want: "invalid"},
		{name: "issuer not ready", cert: goodC, key: goodK, modify: func(i *unstructured.Unstructured) {
			_ = unstructured.SetNestedSlice(i.Object, nil, "status", "conditions")
		}, want: "must be Ready"},
		{name: "issuer status stale", cert: goodC, key: goodK, modify: func(i *unstructured.Unstructured) { i.SetGeneration(3) }, want: "must be Ready"},
		{name: "managed vault", modify: func(i *unstructured.Unstructured) {
			i.Object["spec"] = map[string]any{"vault": map[string]any{"server": "https://vault.invalid"}}
		}},
		{name: "ACME is for server TLS", modify: func(i *unstructured.Unstructured) {
			i.Object["spec"] = map[string]any{"acme": map[string]any{"server": "https://acme.invalid"}}
		}, want: "not ACME"},
		{name: "self signed is not a managed CA", modify: func(i *unstructured.Unstructured) { i.Object["spec"] = map[string]any{"selfSigned": map[string]any{}} }, want: "not selfSigned"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			issuer := productionIssuer("workload-root")
			if tc.modify != nil {
				tc.modify(issuer)
			}
			registry := caSecret(otherC, otherK)
			enterpriseRegistry := caSecret(goodC, goodK)
			if tc.registryCert != nil {
				enterpriseRegistry = caSecret(tc.registryCert, tc.registryKey)
			}
			enterpriseRegistry.Name = "registry-root"
			objects := []runtime.Object{registry, enterpriseRegistry}
			if tc.cert != nil {
				workload := caSecret(tc.cert, tc.key)
				workload.Name = "workload-root"
				objects = append(objects, workload)
			}
			cs := kubernetesfake.NewSimpleClientset(objects...)
			registryIssuer := productionIssuer("registry-root")
			registryIssuer.SetName("registry")
			dc := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), issuer, registryIssuer)
			prevKC, prevNew := platformSetupKubeconfig, newKubernetesClients
			platformSetupKubeconfig = ""
			newKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: cs, Dynamic: dc}, nil }
			t.Cleanup(func() { platformSetupKubeconfig, newKubernetesClients = prevKC, prevNew })
			issuerName := tc.issuer
			if issuerName == "" {
				issuerName = "workload"
			}
			called := false
			err := setupTLSStep(zap.NewNop(), setupplan.Plan{TLSEnabled: true, MTLSClusterIssuer: issuerName, TLSClusterIssuer: tc.tlsIssuer, RegistryMode: setupplan.RegistryModeBundledHTTPS}, SetupDeps{SetupTLS: func(*zap.Logger, setupplan.Plan) error { called = true; return nil }})
			if tc.want == "" {
				if err != nil || !called {
					t.Fatalf("valid dedicated issuer blocked: called=%v err=%v", called, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.want) || called {
				t.Fatalf("TLS mutation must be blocked with %q: called=%v err=%v", tc.want, called, err)
			}
			for _, action := range append(cs.Actions(), dc.Actions()...) {
				if action.GetVerb() != "get" {
					t.Fatalf("CA precheck mutated state: %s", action.GetVerb())
				}
			}
			unchanged, err := cs.CoreV1().Secrets("cert-manager").Get(context.Background(), "mcp-runtime-ca", metav1.GetOptions{})
			if err != nil || string(unchanged.Data["tls.key"]) != string(otherK) {
				t.Fatal("registry CA changed")
			}
		})
	}
}

func intermediateCA(t *testing.T, rootPEM, rootKeyPEM []byte) ([]byte, []byte) {
	t.Helper()
	rootBlock, _ := pem.Decode(rootPEM)
	root, err := x509.ParseCertificate(rootBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	keyBlock, _ := pem.Decode(rootKeyPEM)
	rootKey, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "workload-intermediate"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func selfSignedCAFromKey(t *testing.T, keyPEM []byte) []byte {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	key := parsed.(*ecdsa.PrivateKey)
	template := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "resigned-workload-root"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(2, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestBundledCAReadFailureCannotGenerateRoot(t *testing.T) {
	cs := kubernetesfake.NewSimpleClientset()
	denied := apierrors.NewForbidden(schema.GroupResource{Resource: "secrets"}, "mcp-runtime-ca", errors.New("denied"))
	cs.PrependReactor("get", "secrets", func(k8stesting.Action) (bool, runtime.Object, error) { return true, nil, denied })
	previous := newKubernetesClients
	previousConfig := platformSetupKubeconfig
	platformSetupKubeconfig = ""
	newKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: cs}, nil }
	t.Cleanup(func() { newKubernetesClients, platformSetupKubeconfig = previous, previousConfig })
	created, err := ensureCASecretClientGo()
	if created || !apierrors.IsForbidden(err) {
		t.Fatalf("read failure must fail closed, created=%v err=%v", created, err)
	}
	for _, action := range cs.Actions() {
		if action.GetVerb() != "get" {
			t.Fatal("read failure attempted a write")
		}
	}
}
