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
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	kubernetesfake "k8s.io/client-go/kubernetes/fake"

	"mcp-runtime/internal/cli/core"
	setupplan "mcp-runtime/internal/cli/setup/plan"
	"mcp-runtime/pkg/k8sclient"
)

func caPEM(t *testing.T, notAfter time.Time) (cert, key []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "t"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true, IsCA: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, _ := x509.MarshalPKCS8PrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}

func useFakeCAClient(t *testing.T, secret *corev1.Secret) {
	t.Helper()
	cs := kubernetesfake.NewSimpleClientset()
	if secret != nil {
		if _, err := cs.CoreV1().Secrets("cert-manager").Create(context.Background(), secret, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	prevKC, prevNew := platformSetupKubeconfig, newKubernetesClients
	platformSetupKubeconfig = ""
	newKubernetesClients = func() (*k8sclient.Clients, error) { return &k8sclient.Clients{Clientset: cs}, nil }
	t.Cleanup(func() { platformSetupKubeconfig, newKubernetesClients = prevKC, prevNew })
}

func caSecret(cert, key []byte) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mcp-runtime-ca", Namespace: "cert-manager"},
		Data:       map[string][]byte{"tls.crt": cert, "tls.key": key},
	}
}

func TestEnsureManagedWorkloadCA(t *testing.T) {
	now := time.Now()
	goodC, goodK := caPEM(t, now.AddDate(5, 0, 0))
	nearC, nearK := caPEM(t, now.AddDate(0, 0, 30))
	expC, expK := caPEM(t, now.Add(-time.Minute))
	_, otherK := caPEM(t, now.AddDate(5, 0, 0))

	cases := []struct {
		name     string
		secret   *corev1.Secret
		testMode bool
		base     error
	}{
		{"prod healthy", caSecret(goodC, goodK), false, nil},
		{"prod missing refuses to generate", nil, false, core.ErrCASecretNotFound},
		{"prod near expiry fails", caSecret(nearC, nearK), false, core.ErrCANearExpiry},
		{"prod expired fails", caSecret(expC, expK), false, core.ErrCASecretInvalid},
		{"prod key mismatch fails", caSecret(goodC, otherK), false, core.ErrCASecretInvalid},
		{"test mode near expiry warns", caSecret(nearC, nearK), true, nil},
		{"test mode expired still fails", caSecret(expC, expK), true, core.ErrCASecretInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			useFakeCAClient(t, tc.secret)
			err := ensureManagedWorkloadCA(setupplan.Plan{TestMode: tc.testMode}, now)
			if tc.base == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tc.base) {
				t.Fatalf("want %v, got %v", tc.base, err)
			}
		})
	}
}
