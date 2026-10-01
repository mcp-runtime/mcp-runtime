package runtimeapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"mcp-runtime/pkg/certauth"
	"mcp-runtime/pkg/identity"
)

const adapterCertificateRequestMaxBytes = 64 << 10

var certificateRequestGVR = schema.GroupVersionResource{
	Group: "cert-manager.io", Version: "v1", Resource: "certificaterequests",
}

var adapterMCPServerGVR = schema.GroupVersionResource{
	Group: "mcpruntime.org", Version: "v1alpha1", Resource: "mcpservers",
}

type adapterCertificateRequest struct {
	Namespace string `json:"namespace"`
	Session   string `json:"session"`
	CSR       string `json:"csr"`
}

type adapterCertificateResponse struct {
	Certificate string    `json:"certificate"`
	CABundle    string    `json:"caBundle"`
	SPIFFEID    string    `json:"spiffeID"`
	ExpiresAt   time.Time `json:"expiresAt"`
}

// HandleAdapterCertificate signs a client-generated CSR after verifying that
// its SPIFFE URI names a session owned by the authenticated principal.
func (s *AccessService) HandleAdapterCertificate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeAPIError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if s.k8sClients == nil || s.k8sClients.Dynamic == nil || s.accessMgr == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	issuer := strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER"))
	if issuer == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "workload certificate issuer is not configured")
		return
	}

	var req adapterCertificateRequest
	r.Body = http.MaxBytesReader(w, r.Body, adapterCertificateRequestMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Session = strings.TrimSpace(req.Session)
	if req.Namespace == "" || req.Session == "" || strings.TrimSpace(req.CSR) == "" {
		writeAPIError(w, http.StatusBadRequest, "namespace, session, and csr are required")
		return
	}

	principal, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "no principal on request")
		return
	}
	session, err := s.accessMgr.GetSession(r.Context(), req.Session, req.Namespace)
	if err != nil || session == nil {
		writeAPIError(w, http.StatusNotFound, "adapter session not found")
		return
	}
	humanID := strings.TrimSpace(principal.Subject)
	if humanID == "" {
		humanID = strings.TrimSpace(principal.Email)
	}
	if humanID == "" || humanID != string(session.Spec.Subject.HumanID) {
		writeAPIError(w, http.StatusForbidden, "adapter session is not owned by the authenticated principal")
		return
	}
	if session.Spec.ExpiresAt == nil || !session.Spec.ExpiresAt.After(time.Now()) {
		writeAPIError(w, http.StatusForbidden, "adapter session is expired")
		return
	}
	if session.Spec.Revoked {
		writeAPIError(w, http.StatusForbidden, "adapter session is revoked")
		return
	}
	if err := requireActiveAgent(r.Context(), s.identity, string(session.Spec.Subject.AgentID), string(session.Spec.Subject.TeamID)); err != nil {
		writeAgentDirectoryError(w, err)
		return
	}
	serverName := string(session.Spec.ServerRef.Name)
	serverNamespace := string(session.Spec.ServerRef.Namespace)
	if serverNamespace == "" {
		serverNamespace = req.Namespace
	}
	_, err = s.k8sClients.Dynamic.Resource(adapterMCPServerGVR).Namespace(serverNamespace).Get(
		r.Context(), serverName, metav1.GetOptions{},
	)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "read target MCPServer", err)
		return
	}
	trustDomain := strings.TrimSpace(os.Getenv("MCP_TRUST_DOMAIN"))
	if trustDomain == "" {
		writeAPIError(w, http.StatusServiceUnavailable, "platform SPIFFE trust domain is not configured")
		return
	}

	expectedSPIFFEID := identity.SessionSPIFFEID(trustDomain, req.Namespace, req.Session)
	csrDER, err := certauth.ValidateCSRPEM(req.CSR, expectedSPIFFEID)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	expiresAt := session.Spec.ExpiresAt.Time.UTC()
	duration := time.Until(expiresAt)
	if duration > adapterSessionMaxTTL {
		duration = adapterSessionMaxTTL
	}
	if duration < time.Minute {
		writeAPIError(w, http.StatusForbidden, "adapter session is too close to expiry")
		return
	}
	certificate, caBundle, err := s.issueSessionCertificateDER(r.Context(), req.Namespace, req.Session, csrDER, expectedSPIFFEID, duration)
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "issue adapter certificate", err)
		return
	}
	if err := requireActiveAgent(r.Context(), s.identity, string(session.Spec.Subject.AgentID), string(session.Spec.Subject.TeamID)); err != nil {
		writeAgentDirectoryError(w, err)
		return
	}
	latest, err := s.accessMgr.GetSession(r.Context(), req.Session, req.Namespace)
	if err != nil || latest == nil || latest.Spec.Revoked {
		writeAPIError(w, http.StatusForbidden, "adapter session was revoked during certificate enrollment")
		return
	}
	writeJSON(w, http.StatusCreated, adapterCertificateResponse{
		Certificate: certificate,
		CABundle:    caBundle,
		SPIFFEID:    expectedSPIFFEID,
		ExpiresAt:   expiresAt,
	})
}

func (s *AccessService) issueSessionCertificateDER(
	ctx context.Context,
	namespace, sessionName string,
	csrDER []byte,
	expectedSPIFFEID string,
	duration time.Duration,
) (string, string, error) {
	if s.k8sClients == nil || s.k8sClients.Dynamic == nil {
		return "", "", fmt.Errorf("kubernetes not available")
	}
	issuer := strings.TrimSpace(os.Getenv("MCP_MTLS_CLUSTER_ISSUER"))
	if issuer == "" {
		return "", "", fmt.Errorf("workload certificate issuer is not configured")
	}
	resource := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "cert-manager.io/v1",
		"kind":       "CertificateRequest",
		"metadata": map[string]any{
			"generateName": "adapter-" + sessionName + "-",
			"namespace":    namespace,
			"labels": map[string]any{
				"app.kubernetes.io/managed-by": "mcp-runtime",
				"mcpruntime.org/session":       sessionName,
			},
		},
		"spec": map[string]any{
			// cert-manager's CertificateRequest.spec.request must be a PEM-encoded
			// CSR; sending raw DER makes the admission webhook reject it with
			// "error decoding certificate request PEM block".
			"request":  base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})),
			"duration": duration.Round(time.Second).String(),
			"usages":   []any{"digital signature", "client auth"},
			"issuerRef": map[string]any{
				"group": "cert-manager.io",
				"kind":  "ClusterIssuer",
				"name":  issuer,
			},
		},
	}}
	created, err := s.k8sClients.Dynamic.Resource(certificateRequestGVR).Namespace(namespace).Create(
		ctx, resource, metav1.CreateOptions{},
	)
	if err != nil {
		return "", "", fmt.Errorf("create certificate request: %w", err)
	}

	waitCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	certificate, caBundle, err := waitForIssuedAdapterCertificate(waitCtx, s, namespace, created.GetName())
	if err != nil {
		return "", "", err
	}
	// Defense in depth: never hand back a certificate the workload issuer
	// signed with a different identity, key, usage, or lifetime than the
	// session-bound CSR we submitted, whatever the issuer's approval policy.
	if err := certauth.ValidateIssuedCertificatePEM(certificate, csrDER, expectedSPIFFEID, duration, time.Now()); err != nil {
		return "", "", fmt.Errorf("reject issued certificate: %w", err)
	}
	return certificate, caBundle, nil
}

func waitForIssuedAdapterCertificate(ctx context.Context, s *AccessService, namespace, name string) (string, string, error) {
	if s == nil || s.accessMgr == nil || s.k8sClients == nil {
		return "", "", fmt.Errorf("kubernetes not available")
	}
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := s.k8sClients.Dynamic.Resource(certificateRequestGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			return "", "", err
		}
		if err := adapterCertificateRequestFailure(request); err != nil {
			return "", "", err
		}
		certificate, _, _ := unstructured.NestedString(request.Object, "status", "certificate")
		ca, _, _ := unstructured.NestedString(request.Object, "status", "ca")
		if certificate != "" {
			certPEM, err := base64.StdEncoding.DecodeString(certificate)
			if err != nil {
				return "", "", fmt.Errorf("decode issued certificate: %w", err)
			}
			caPEM, err := base64.StdEncoding.DecodeString(ca)
			if err != nil {
				return "", "", fmt.Errorf("decode issued CA bundle: %w", err)
			}
			return string(certPEM), string(caPEM), nil
		}
		select {
		case <-ctx.Done():
			return "", "", ctx.Err()
		case <-ticker.C:
		}
	}
}

func adapterCertificateRequestFailure(request *unstructured.Unstructured) error {
	conditions, found, _ := unstructured.NestedSlice(request.Object, "status", "conditions")
	if !found {
		return nil
	}
	for _, condition := range conditions {
		conditionMap, ok := condition.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _, _ := unstructured.NestedString(conditionMap, "type")
		conditionStatus, _, _ := unstructured.NestedString(conditionMap, "status")
		if conditionType != "Ready" || conditionStatus != "False" {
			continue
		}
		reason, _, _ := unstructured.NestedString(conditionMap, "reason")
		if reason != "Failed" && reason != "Denied" {
			continue
		}
		message, _, _ := unstructured.NestedString(conditionMap, "message")
		if strings.TrimSpace(message) == "" {
			return fmt.Errorf("certificate request failed: %s", reason)
		}
		return fmt.Errorf("certificate request failed: %s (%s)", reason, message)
	}
	return nil
}
