package operator

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func analyticsTestCA(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestAnalyticsTrustCopiesOnlyPublicCAAndWiresGateway(t *testing.T) {
	for _, scenario := range []string{"valid", "missing", "private-block", "foreign-owner", "http-url"} {
		t.Run(scenario, func(t *testing.T) {
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)
			_ = mcpv1alpha1.AddToScheme(scheme)
			server := mtlsServer()
			server.UID = "current-server"
			public := analyticsTestCA(t)
			source := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "mcp-analytics-ca", Namespace: "mcp-runtime"}, Data: map[string]string{"ca.crt": public, "unrelated": "must never copy"}}
			if scenario == "private-block" {
				source.Data["ca.crt"] += "-----BEGIN PRIVATE KEY-----\nYQ==\n-----END PRIVATE KEY-----\n"
			}
			objects := []runtime.Object{server}
			if scenario != "missing" {
				objects = append(objects, source)
			}
			if scenario == "foreign-owner" {
				objects = append(objects, &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: analyticsCAName(server), Namespace: server.Namespace, UID: "foreign-existing"}, Data: map[string]string{"ca.crt": "foreign"}})
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build()
			r := &MCPServerReconciler{Client: c, Scheme: scheme, AnalyticsCAConfigMap: source.Name, DefaultAnalyticsIngestURL: "https://mcp-ingest.mcp-observability.svc.cluster.local:8081/events", GatewayProxyImage: "example.com/gateway:test"}
			if scenario == "http-url" {
				r.DefaultAnalyticsIngestURL = strings.Replace(r.DefaultAnalyticsIngestURL, "https:", "http:", 1)
			}
			err := r.reconcileAnalyticsCA(context.Background(), server)
			if scenario != "valid" {
				if err == nil {
					t.Fatal("invalid trust/owner/transport accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var copied corev1.ConfigMap
			if err := c.Get(context.Background(), types.NamespacedName{Name: analyticsCAName(server), Namespace: server.Namespace}, &copied); err != nil {
				t.Fatal(err)
			}
			if len(copied.Data) != 1 || copied.Data["ca.crt"] != public || !metav1.IsControlledBy(&copied, server) {
				t.Fatal("public trust/ownership contract changed")
			}
			containers, volumes, err := r.buildDeploymentContainers(server, server.Spec.Image)
			if err != nil {
				t.Fatal(err)
			}
			mounted, configured, copiedVolume := false, false, false
			for _, container := range containers {
				for _, mount := range container.VolumeMounts {
					mounted = mounted || mount.Name == "analytics-ca" && mount.ReadOnly
				}
				for _, env := range container.Env {
					configured = configured || env.Name == "ANALYTICS_TLS_CA_FILE" && env.Value == "/var/run/mcp-runtime/analytics-ca/ca.crt"
				}
			}
			for _, volume := range volumes {
				copiedVolume = copiedVolume || volume.ConfigMap != nil && volume.ConfigMap.Name == copied.Name
			}
			if !mounted || !configured || !copiedVolume {
				t.Fatal("gateway does not consume its public trust copy")
			}
		})
	}
}
