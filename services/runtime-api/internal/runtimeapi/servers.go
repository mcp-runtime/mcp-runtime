package runtimeapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtimeaccess "mcp-runtime-api/internal/runtimeapi/access"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/controlplane"
	"mcp-runtime/pkg/k8sclient"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/platformstack"
	"mcp-runtime/pkg/publishscope"
)

var errForbiddenNamespace = errors.New("forbidden namespace")

const (
	defaultAnalyticsCredentialSourceSecretName = "mcp-runtime-ingest-credentials"
	defaultAnalyticsCredentialSourceKey        = "INGEST_API_KEYS"
	defaultAnalyticsCredentialKey              = "api-key"
)

type runtimeServerApplyRequest struct {
	Name      string                    `json:"name"`
	Namespace string                    `json:"namespace,omitempty"`
	Scope     string                    `json:"scope,omitempty"`
	Update    bool                      `json:"update,omitempty"`
	Labels    map[string]string         `json:"labels,omitempty"`
	Spec      mcpv1alpha1.MCPServerSpec `json:"spec"`
}

// HandleRuntimeServers lists and applies MCPServer resources within the caller's readable or publishable namespaces.
func (s *RuntimeServer) HandleRuntimeServers(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRuntimeServerList(w, r)
	case http.MethodPost:
		s.handleRuntimeServerApply(w, r)
	default:
		w.Header().Set("allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *RuntimeServer) handleRuntimeServerList(w http.ResponseWriter, r *http.Request) {
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	servers, err := s.Inventory().visibleServers(ctx, control, p, namespace)
	if errors.Is(err, errForbiddenNamespace) {
		writeAPIError(w, http.StatusForbidden, "forbidden namespace")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list servers")
		return
	}
	if len(servers) == 0 {
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"servers":        []serverInfo{},
			"publish_policy": s.Deployments().publishPolicyStatusForPrincipal(ctx, p),
		})
		return
	}
	sort.SliceStable(servers, func(i, j int) bool {
		if servers[i].Namespace != servers[j].Namespace {
			return servers[i].Namespace < servers[j].Namespace
		}
		return servers[i].Name < servers[j].Name
	})

	writeJSON(w, http.StatusOK, map[string]interface{}{
		"servers":        s.serverInfosWithRuntimeData(ctx, servers, r),
		"publish_policy": s.Deployments().publishPolicyStatusForPrincipal(ctx, p),
	})
}

func catalogNamespacesForPrincipal(p principal) []string {
	return publishNamespacesForPrincipal(p)
}

func dedupeNonEmptyStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func (s *RuntimeServer) handleRuntimeServerApply(w http.ResponseWriter, r *http.Request) {
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var req runtimeServerApplyRequest
	r.Body = http.MaxBytesReader(w, r.Body, accessApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Namespace = strings.TrimSpace(req.Namespace)
	req.Scope = strings.TrimSpace(req.Scope)
	req.Spec.Image = strings.TrimSpace(req.Spec.Image)
	if req.Name == "" || req.Spec.Image == "" {
		writeAPIError(w, http.StatusBadRequest, "name and spec.image are required")
		return
	}
	scope, err := publishscope.Normalize(req.Scope)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	namespace, err := s.scopedNamespaceForServerApply(r.Context(), req.Namespace, scope)
	if err != nil {
		writeAPIError(w, http.StatusForbidden, err.Error())
		return
	}
	if p.Role != roleAdmin && namespace == sharedCatalogNamespace && !sharedCatalogWritableForUsers() {
		writeAPIError(w, http.StatusForbidden, "shared catalog namespace is read-only for team users")
		return
	}
	if p.Role != roleAdmin && !principalCanPublishNamespace(p, namespace) {
		writeAPIError(w, http.StatusForbidden, "forbidden namespace")
		return
	}
	namespaceTeamID := strings.TrimSpace(s.Access().teamIDForPrincipalNamespace(r.Context(), namespace))
	req.Spec.TeamID = strings.TrimSpace(req.Spec.TeamID)
	if req.Spec.TeamID == "" {
		req.Spec.TeamID = namespaceTeamID
	}
	if err := runtimeaccess.ValidateTeamIDValue("spec.teamID", req.Spec.TeamID); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if namespaceTeamID != "" && req.Spec.TeamID != namespaceTeamID {
		writeAPIError(w, http.StatusForbidden, "spec.teamID must match namespace team")
		return
	}
	if p.Role != roleAdmin && namespaceTeamID == "" && req.Spec.TeamID != "" {
		writeAPIError(w, http.StatusForbidden, "spec.teamID is only allowed in a team namespace")
		return
	}
	team, isTeamNamespace := p.TeamForNamespace(namespace)
	teamSlug := ""
	if isTeamNamespace {
		teamSlug = strings.TrimSpace(team.Slug)
	}
	req.Spec.Image = ResolveDeployImageReference(req.Spec.Image, namespace, teamSlug)
	if err := ValidateDeployImage(req.Spec.Image, namespace, teamSlug, p.Role); err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}

	req.Namespace = namespace
	if req.Spec.PublicPathPrefix == "" {
		req.Spec.PublicPathPrefix = req.Name
	}
	if req.Spec.IngressPath == "" {
		req.Spec.IngressPath = "/" + req.Spec.PublicPathPrefix + "/mcp"
	}
	req.Spec.IngressHost = strings.TrimSpace(req.Spec.IngressHost)
	if req.Spec.IngressHost == "" {
		req.Spec.IngressHost = defaultRuntimeServerIngressHost()
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()

	if err := s.ensureServerApplyNamespace(ctx, p, namespace, team, isTeamNamespace); err != nil {
		log.Printf("runtime servers: ensure namespace %q before apply failed: %v", namespace, err)
		writeAPIError(w, http.StatusInternalServerError, serverApplyNamespaceEnsureError(p, namespace, isTeamNamespace))
		return
	}

	current, err := control.GetServer(ctx, namespace, req.Name)
	if err != nil && !apierrors.IsNotFound(err) {
		log.Printf("runtime servers: fetch MCPServer %s/%s before apply failed: %v", namespace, req.Name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to inspect existing server")
		return
	}
	if p.Role != roleAdmin && current != nil && !serverWritableByPrincipal(*current, p) {
		msg := "server already exists and is not owned by this user"
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "denied", req.Name, namespace, req.Spec.Image, msg))
		writeAPIError(w, http.StatusForbidden, msg)
		return
	}
	if current != nil && !req.Update {
		msg := "server already exists; use --update to redeploy it"
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "denied", req.Name, namespace, req.Spec.Image, msg))
		writeAPIError(w, http.StatusConflict, msg)
		return
	}
	// This is an API-layer guard. Strict global quota enforcement under highly
	// concurrent publishes would need a shared reservation/locking mechanism.
	rejection, err := s.Deployments().evaluateServerPublishPolicy(ctx, p, namespace, req.Name, current, time.Now().UTC())
	if err != nil {
		log.Printf("runtime servers: evaluate publish policy for %s/%s failed: %v", namespace, req.Name, err)
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "error", req.Name, namespace, req.Spec.Image, err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to evaluate publish policy")
		return
	}
	if rejection != nil {
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "denied", req.Name, namespace, req.Spec.Image, rejection.message))
		if retryAfter := rejection.retryAfterHeader(); retryAfter != "" {
			w.Header().Set("retry-after", retryAfter)
		}
		writeJSON(w, rejection.status, rejection.payload())
		return
	}
	if err := s.applyPublishedServerDefaults(ctx, namespace, req.Name, &req.Spec); err != nil {
		log.Printf("runtime servers: apply defaults for %s/%s failed: %v", namespace, req.Name, err)
		writeAPIError(w, http.StatusInternalServerError, "failed to configure gateway analytics")
		return
	}

	labels := map[string]string{
		"app.kubernetes.io/managed-by": "mcp-runtime",
	}
	for key, value := range req.Labels {
		labels[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	if current != nil && p.Role == roleAdmin {
		preserveCurrentLabel(labels, current.Labels, platformUserIDLabel)
		preserveCurrentLabel(labels, current.Labels, createdByLabel)
	} else if userID := p.UserID(); userID != "" {
		labels[platformUserIDLabel] = userID
		labels[createdByLabel] = userID
	}
	if namespaceTeamID != "" {
		labels[platformTeamIDLabel] = namespaceTeamID
	}
	if resolvedScope := scopeLabelForNamespace(namespace, scope); resolvedScope != "" {
		labels[platformScopeLabel] = resolvedScope
	}
	annotations := map[string]string{
		platformLastPushAtAnnotation: time.Now().UTC().Format(time.RFC3339),
	}
	if userID := p.UserID(); userID != "" {
		annotations[platformLastPushByAnnotation] = userID
	}

	server := &mcpv1alpha1.MCPServer{
		TypeMeta: metav1.TypeMeta{
			APIVersion: mcpv1alpha1.GroupVersion.String(),
			Kind:       "MCPServer",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        req.Name,
			Namespace:   namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: req.Spec,
	}

	applied, err := control.ApplyServer(ctx, server)
	if err != nil {
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "error", req.Name, namespace, req.Spec.Image, msg))
		writeAPIError(w, code, msg)
		return
	}
	s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_publish", "success", req.Name, namespace, req.Spec.Image, ""))
	writeJSON(w, http.StatusOK, map[string]any{"server": serverInfoFromMCPServer(*applied, serverDeploymentStatus{}, r)})
}

func (s *RuntimeServer) ensureServerApplyNamespace(ctx context.Context, p principal, namespace string, team principalTeam, isTeamNamespace bool) error {
	if p.Role == roleAdmin {
		return nil
	}
	if sharedCatalogWritableForUsers() && isModeCatalogNamespace(namespace) {
		if err := s.Deployments().EnsureCatalogNamespace(ctx, namespace); err != nil {
			return fmt.Errorf("catalog namespace %q: %w", namespace, err)
		}
		return nil
	}
	if isTeamNamespace {
		if err := s.Deployments().ensureTeamNamespace(ctx, teamRecord{
			ID:        team.ID,
			Slug:      team.Slug,
			Name:      team.Name,
			Namespace: team.Namespace,
		}); err != nil {
			return fmt.Errorf("team namespace %q: %w", namespace, err)
		}
		if err := s.Deployments().ensureNamespaceUserWorkloadRBAC(ctx, namespace, p.UserID()); err != nil {
			return fmt.Errorf("team namespace access %q: %w", namespace, err)
		}
	}
	return nil
}

func serverApplyNamespaceEnsureError(p principal, namespace string, isTeamNamespace bool) string {
	if p.Role != roleAdmin && sharedCatalogWritableForUsers() && isModeCatalogNamespace(namespace) {
		return "failed to ensure catalog namespace"
	}
	if p.Role != roleAdmin && isTeamNamespace {
		return "failed to ensure team namespace"
	}
	return "failed to ensure server namespace"
}

func defaultRuntimeServerIngressHost() string {
	return metadata.ResolveMcpIngressHost()
}

func (s *RuntimeServer) applyPublishedServerDefaults(ctx context.Context, namespace, name string, spec *mcpv1alpha1.MCPServerSpec) error {
	if spec == nil {
		return nil
	}
	if spec.Gateway == nil {
		spec.Gateway = &mcpv1alpha1.GatewayConfig{Enabled: mcpv1alpha1.BoolPtr(true)}
	} else if spec.Gateway.Enabled == nil {
		spec.Gateway.Enabled = mcpv1alpha1.BoolPtr(true)
	}
	if !mcpv1alpha1.GatewayIsEnabled(spec.Gateway) || analyticsDisabled(spec.Analytics) {
		return nil
	}
	if spec.Analytics == nil {
		spec.Analytics = &mcpv1alpha1.AnalyticsConfig{}
	}
	if strings.TrimSpace(spec.Analytics.IngestURL) == "" {
		// The API injects analytics credentials, so it must also supply the
		// configured ingest URL when the operator has no fallback of its own.
		spec.Analytics.IngestURL = strings.TrimSpace(os.Getenv("MCP_SENTINEL_INGEST_URL"))
		if spec.Analytics.IngestURL == "" {
			spec.Analytics.IngestURL = strings.TrimSpace(os.Getenv("MCP_ANALYTICS_INGEST_URL"))
		}
	}
	if spec.Analytics.APIKeySecretRef != nil {
		return nil
	}

	ref, err := s.ensurePublishedServerAnalyticsSecret(ctx, namespace, name)
	if err != nil {
		if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) {
			log.Printf("runtime servers: analytics secret injection skipped for %s/%s: %v", namespace, name, err)
			return nil
		}
		return err
	}
	if ref == nil {
		return nil
	}
	spec.Analytics.APIKeySecretRef = ref
	return nil
}

func analyticsDisabled(cfg *mcpv1alpha1.AnalyticsConfig) bool {
	return cfg != nil && cfg.Disabled
}

func publishedServerAnalyticsSecretName(name string) string {
	const (
		fallback = "analytics-creds"
		suffix   = "-analytics-creds"
		maxLen   = 63
	)

	name = strings.TrimSpace(name)
	if name == "" {
		return fallback
	}
	if len(name)+len(suffix) > maxLen {
		name = name[:maxLen-len(suffix)]
		name = strings.TrimRight(name, "-.")
		if name == "" {
			return fallback
		}
	}
	return name + suffix
}

func (s *RuntimeServer) ensurePublishedServerAnalyticsSecret(ctx context.Context, namespace, serverName string) (*mcpv1alpha1.SecretKeyRef, error) {
	if s.k8sClients == nil {
		return nil, errors.New("kubernetes not available")
	}
	ingestKey, err := s.defaultAnalyticsAPIKey(ctx)
	if err != nil {
		return nil, err
	}
	if ingestKey == "" {
		return nil, nil
	}

	secretName := publishedServerAnalyticsSecretName(serverName)
	secrets := s.k8sClients.Clientset.CoreV1().Secrets(namespace)
	current, err := secrets.Get(ctx, secretName, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}
	if apierrors.IsNotFound(err) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Name:      secretName,
				Namespace: namespace,
				Labels: map[string]string{
					"app.kubernetes.io/managed-by": "mcp-runtime",
				},
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{
				defaultAnalyticsCredentialKey: []byte(ingestKey),
			},
		}
		if _, err := secrets.Create(ctx, secret, metav1.CreateOptions{}); err != nil {
			return nil, err
		}
		return &mcpv1alpha1.SecretKeyRef{Name: secretName, Key: defaultAnalyticsCredentialKey}, nil
	}

	if current.Data == nil {
		current.Data = map[string][]byte{}
	}
	if string(current.Data[defaultAnalyticsCredentialKey]) != ingestKey {
		current.Data[defaultAnalyticsCredentialKey] = []byte(ingestKey)
		if _, err := secrets.Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return nil, err
		}
	}
	return &mcpv1alpha1.SecretKeyRef{Name: secretName, Key: defaultAnalyticsCredentialKey}, nil
}

func (s *RuntimeServer) defaultAnalyticsAPIKey(ctx context.Context) (string, error) {
	if s.k8sClients == nil {
		return "", errors.New("kubernetes not available")
	}
	secret, err := s.k8sClients.Clientset.CoreV1().Secrets(platformstack.PlatformNamespace).Get(ctx, defaultAnalyticsCredentialSourceSecretName, metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return "", nil
		}
		return "", err
	}
	for _, raw := range strings.Split(string(secret.Data[defaultAnalyticsCredentialSourceKey]), ",") {
		if value := strings.TrimSpace(raw); value != "" {
			return value, nil
		}
	}
	return "", nil
}

func (s *RuntimeServer) scopedNamespaceForServerApply(ctx context.Context, requested string, scope publishscope.Scope) (string, error) {
	requested = strings.TrimSpace(requested)
	if scope == "" {
		return s.Access().scopedNamespaceForPrincipal(ctx, requested)
	}

	p, ok := principalFromContext(ctx)
	if !ok {
		return "", errPrincipalIdentityRequired
	}
	if err := publishScopeEnabledError(scope); err != nil {
		return "", err
	}
	switch scope {
	case publishscope.Public, publishscope.Org:
		return scopedModeCatalogNamespaceForApply(p, requested)
	case publishscope.Tenant:
		return scopedTenantNamespaceForApply(p, requested)
	default:
		return "", errors.New("invalid publish scope")
	}
}

func scopedModeCatalogNamespaceForApply(p principal, requested string) (string, error) {
	namespace := defaultCatalogNamespaceForMode()
	if requested != "" {
		if !isModeCatalogNamespace(requested) {
			return "", errors.New("scope does not match requested namespace")
		}
		namespace = requested
	}
	if p.Role != roleAdmin && !principalCanPublishNamespace(p, namespace) {
		return "", errors.New("forbidden namespace")
	}
	return namespace, nil
}

func scopedTenantNamespaceForApply(p principal, requested string) (string, error) {
	if p.Role == roleAdmin {
		if requested == "" {
			return "", errors.New("namespace is required for tenant scope")
		}
		return requested, nil
	}
	if requested == "" {
		requested = principalDefaultTeamNamespace(p)
	}
	if requested == "" {
		return "", errors.New("tenant scope requires team membership")
	}
	if isModeCatalogNamespace(requested) || requested == sharedCatalogNamespace {
		return "", errors.New("tenant scope requires a team namespace")
	}
	if _, ok := p.TeamForNamespace(requested); !ok {
		return "", errors.New("tenant scope requires a team namespace")
	}
	return requested, nil
}

func principalDefaultTeamNamespace(p principal) string {
	for _, team := range p.Teams {
		if namespace := strings.TrimSpace(team.Namespace); namespace != "" {
			return namespace
		}
	}
	return ""
}

func scopeLabelForNamespace(namespace string, scope publishscope.Scope) string {
	if scope != "" {
		return string(scope)
	}
	if isModeCatalogNamespace(namespace) {
		return PlatformMode()
	}
	if namespace != "" {
		return string(publishscope.Tenant)
	}
	return ""
}

func preserveCurrentLabel(labels, current map[string]string, key string) {
	if value := strings.TrimSpace(current[key]); value != "" {
		labels[key] = value
	}
}

// HandleRuntimeServerItem returns or retires one MCPServer after namespace authorization.
func (s *RuntimeServer) HandleRuntimeServerItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleRuntimeServerGet(w, r)
	case http.MethodDelete:
		s.handleRuntimeServerDelete(w, r)
	default:
		w.Header().Set("allow", "GET, DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

func (s *RuntimeServer) handleRuntimeServerGet(w http.ResponseWriter, r *http.Request) {
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	namespace, name, err := extractNamespaceName(r.URL.Path, "/api/runtime/servers/")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Role != roleAdmin && !principalCanReadNamespace(p, namespace) {
		writeAPIError(w, http.StatusForbidden, "forbidden namespace")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	info, err := control.GetServerInfo(ctx, namespace, name)
	if apierrors.IsNotFound(err) {
		writeAPIError(w, http.StatusNotFound, "server not found")
		return
	}
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to inspect server")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"server": s.serverInfoWithRuntimeData(ctx, info, r)})
}

func (s *RuntimeServer) handleRuntimeServerDelete(w http.ResponseWriter, r *http.Request) {
	control := s.controlPlane()
	if control == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	namespace, name, err := extractNamespaceName(r.URL.Path, "/api/runtime/servers/")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Role != roleAdmin && !principalCanPublishNamespace(p, namespace) {
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_retire", "denied", name, namespace, "", "forbidden namespace"))
		writeAPIError(w, http.StatusForbidden, "forbidden namespace")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	current, err := control.GetServer(ctx, namespace, name)
	if apierrors.IsNotFound(err) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "namespace": namespace, "name": name})
		return
	}
	if err != nil {
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_retire", "error", name, namespace, "", err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to inspect server")
		return
	}
	if p.Role != roleAdmin && !serverWritableByPrincipal(*current, p) {
		msg := "server is not owned by this user"
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_retire", "denied", name, namespace, "", msg))
		writeAPIError(w, http.StatusForbidden, msg)
		return
	}
	if err := control.DeleteServer(ctx, namespace, name); err != nil {
		s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_retire", "error", name, namespace, current.Spec.Image, err.Error()))
		code, msg := k8sclient.HTTPStatusFromK8sError(err)
		writeAPIError(w, code, msg)
		return
	}
	s.writeAudit(r.Context(), serverPublishAuditEvent(r, p, "server_retire", "success", name, namespace, current.Spec.Image, ""))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "namespace": namespace, "name": name})
}

type serverInfo struct {
	controlplane.ServerInfo
	LiveInventory      *liveInventory              `json:"liveInventory"`
	LiveInventoryError string                      `json:"liveInventoryError,omitempty"`
	AccessJSON         map[string]any              `json:"access_json,omitempty"`
	Observability      *observabilityLinksResponse `json:"observability,omitempty"`
}

type serverDeploymentStatus = controlplane.ServerDeploymentStatus

func serverInfoFromMCPServer(mcpServer mcpv1alpha1.MCPServer, deploymentStatus serverDeploymentStatus, r *http.Request) serverInfo {
	return serverInfoWithAccessJSON(controlplane.ServerInfoFromMCPServer(mcpServer, deploymentStatus), r)
}

func serverInfosWithAccessJSON(items []controlplane.ServerInfo, r *http.Request) []serverInfo {
	out := make([]serverInfo, 0, len(items))
	for _, item := range items {
		out = append(out, serverInfoWithAccessJSON(item, r))
	}
	return out
}

func serverInfoWithAccessJSON(info controlplane.ServerInfo, r *http.Request) serverInfo {
	info.Image = metadata.DisplayImageReference(info.Image)
	out := serverInfo{ServerInfo: info}
	connectEndpoint := publicMCPConnectEndpoint(info.Endpoint, r)
	if connectEndpoint != "" {
		out.AccessJSON = map[string]any{
			"mcpServers": map[string]any{
				info.Name: map[string]any{
					"type": "http",
					"url":  connectEndpoint,
				},
			},
		}
	}
	if p, ok := principalFromContext(r.Context()); ok && serverInfoObservableByPrincipal(info, p) {
		links := observabilityLinksForServerInfo(info, p, r)
		out.Observability = &links
	}
	return out
}

func (s *RuntimeServer) serverInfosWithRuntimeData(ctx context.Context, items []controlplane.ServerInfo, r *http.Request) []serverInfo {
	out := make([]serverInfo, 0, len(items))
	for _, item := range items {
		out = append(out, s.serverInfoWithRuntimeData(ctx, item, r))
	}
	return out
}

func (s *RuntimeServer) serverInfoWithRuntimeData(ctx context.Context, info controlplane.ServerInfo, r *http.Request) serverInfo {
	out := serverInfoWithAccessJSON(info, r)
	cache := s.Inventory().liveInventory()
	if cache == nil {
		out.LiveInventoryError = "live inventory unavailable"
		return out
	}
	inventory, reason := cache.getOrStart(ctx, info)
	out.LiveInventory = inventory
	out.LiveInventoryError = reason
	return out
}

func publicMCPEndpoint(mcpServer mcpv1alpha1.MCPServer) string {
	return controlplane.PublicMCPEndpoint(mcpServer)
}

func publicMCPConnectEndpoint(endpoint string, r *http.Request) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" || strings.HasPrefix(endpoint, "http://") || strings.HasPrefix(endpoint, "https://") {
		return endpoint
	}
	path := endpoint
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := forwardedHost(r)
	if host == "" {
		return path
	}
	if strings.HasPrefix(strings.ToLower(host), "platform.") {
		host = "mcp." + host[len("platform."):]
	}
	return forwardedScheme(r) + "://" + strings.TrimRight(host, "/") + path
}

func forwardedHost(r *http.Request) string {
	if r == nil {
		return ""
	}
	return firstForwardedValue(r.Header.Get("X-Forwarded-Host"))
}

func forwardedScheme(r *http.Request) string {
	if r == nil {
		return "http"
	}
	proto := strings.ToLower(firstForwardedValue(r.Header.Get("X-Forwarded-Proto")))
	if proto == "https" {
		return "https"
	}
	return "http"
}

func firstForwardedValue(value string) string {
	if idx := strings.IndexByte(value, ','); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}
