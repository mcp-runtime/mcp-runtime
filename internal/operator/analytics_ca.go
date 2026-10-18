package operator

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/url"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func analyticsCAName(server *mcpv1alpha1.MCPServer) string { return server.Name + "-analytics-ca" }

func (r *MCPServerReconciler) reconcileAnalyticsCA(ctx context.Context, server *mcpv1alpha1.MCPServer) error {
	if r.AnalyticsCAConfigMap == "" || !gatewayEnabled(server) || !r.analyticsEnabled(server) {
		return nil
	}
	endpoint := r.DefaultAnalyticsIngestURL
	if server.Spec.Analytics != nil && strings.TrimSpace(server.Spec.Analytics.IngestURL) != "" {
		endpoint = server.Spec.Analytics.IngestURL
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil {
		return fmt.Errorf("managed analytics transport requires an HTTPS ingest URL without embedded credentials")
	}
	reader := r.APIReader
	if reader == nil {
		reader = r.Client
	}
	source := &corev1.ConfigMap{}
	if err := reader.Get(ctx, types.NamespacedName{Namespace: "mcp-runtime", Name: r.AnalyticsCAConfigMap}, source); err != nil {
		return fmt.Errorf("read setup-managed public analytics CA: %w", err)
	}
	// Re-encode certificates only; never replicate arbitrary ConfigMap fields.
	remaining, public := []byte(source.Data["ca.crt"]), []byte{}
	if len(remaining) > 1<<20 {
		return fmt.Errorf("analytics public trust is oversized")
	}
	count := 0
	for len(strings.TrimSpace(string(remaining))) > 0 {
		count++
		if count > 16 {
			return fmt.Errorf("analytics public trust has too many certificates")
		}
		block, rest := pem.Decode(remaining)
		if block == nil || block.Type != "CERTIFICATE" {
			return fmt.Errorf("analytics public trust contains non-certificate material")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.IsCA || !cert.BasicConstraintsValid || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return fmt.Errorf("analytics public trust contains an invalid signing CA")
		}
		public = append(public, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: block.Bytes})...)
		remaining = rest
	}
	if len(public) == 0 || len(public) > 1<<20 {
		return fmt.Errorf("analytics public trust is empty or oversized")
	}
	current := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: analyticsCAName(server), Namespace: server.Namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, current, func() error {
		if current.UID != "" && !metav1.IsControlledBy(current, server) {
			return apierrors.NewConflict(corev1.Resource("configmaps"), current.Name, fmt.Errorf("analytics trust is owned by another object"))
		}
		current.Data = map[string]string{"ca.crt": string(public)}
		return controllerutil.SetControllerReference(server, current, r.Scheme)
	})
	return err
}
