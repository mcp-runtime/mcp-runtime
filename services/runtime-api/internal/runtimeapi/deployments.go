package runtimeapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/pkg/apihttp"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/mcpdefaults"
	"mcp-runtime/pkg/metadata"
	"mcp-runtime/pkg/publishscope"
	"mcp-runtime/pkg/sentinel"
	"mcp-runtime/pkg/serviceutil"
)

const (
	platformManagedLabel           = "platform.mcpruntime.org/managed"
	platformUserIDLabel            = "platform.mcpruntime.org/user-id"
	platformTeamIDLabel            = "mcpruntime.org/team-id"
	platformTeamSlugLabel          = "mcpruntime.org/team-slug"
	platformScopeLabel             = "mcpruntime.org/scope"
	createdByLabel                 = "created-by"
	defaultDeployPort              = int32(mcpdefaults.MCPServerPort)
	restrictedRunAsUser            = kubeworkload.RestrictedRunAsUser
	traefikWatchRoleName           = "traefik-watch"
	platformNamespaceOwnerRoleName = "platform-namespace-owner"
	sentinelIngestPort             = 8081
	sentinelOTLPPort               = 4318
	registryNamespace              = "registry"
	registryPort                   = 5000
	podSecurityEnforceLabel        = "pod-security.kubernetes.io/enforce"
	podSecurityAuditLabel          = "pod-security.kubernetes.io/audit"
	podSecurityWarnLabel           = "pod-security.kubernetes.io/warn"
)

var (
	errPrincipalIdentityRequired = errors.New("authenticated user identity required")
	deployImageTagPattern        = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)
)

type managedNamespaceOptions struct {
	ingressFromNamespaces []string
}

type teamTraefikWatchConfig struct {
	mode           string
	namespace      string
	deployment     string
	serviceAccount string
}

type deployRequest struct {
	Name      string `json:"name"`
	Image     string `json:"image"`
	Version   string `json:"version"`
	Port      int32  `json:"port"`
	Replicas  int32  `json:"replicas"`
	Namespace string `json:"namespace,omitempty"`
}

// HandleDeployments lists and applies user-managed Kubernetes deployments for the caller's namespace scope.
func (s *DeploymentService) HandleDeployments(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleDeploymentList(w, r)
	case http.MethodPost:
		s.handleDeploymentApply(w, r)
	default:
		w.Header().Set("allow", "GET, POST")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
	}
}

// HandleDeploymentItem deletes a user-managed Kubernetes deployment and service after namespace authorization.
func (s *DeploymentService) HandleDeploymentItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		w.Header().Set("allow", "DELETE")
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	ns, name, err := extractNamespaceName(r.URL.Path, "/api/deployments/")
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	if p.Role != roleAdmin && (!p.HasNamespace(ns) || ns == sharedCatalogNamespace) {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_delete", "denied", name, ns, "", "forbidden"))
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	client, err := s.clientForPrincipal(p)
	if err != nil {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_delete", "error", name, ns, "", err.Error()))
		if errors.Is(err, errPrincipalIdentityRequired) {
			writeAPIError(w, http.StatusForbidden, "authenticated user identity required")
			return
		}
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if err := client.AppsV1().Deployments(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_delete", "error", name, ns, "", err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to delete deployment")
		return
	}
	if err := client.CoreV1().Services(ns).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_delete", "error", name, ns, "", err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to delete service")
		return
	}
	s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_delete", "success", name, ns, "", ""))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "namespace": ns, "name": name})
}

func (s *DeploymentService) handleDeploymentList(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if s.k8sClients == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	if p.Role != roleAdmin {
		if namespace == "" {
			namespace = strings.TrimSpace(p.Namespace)
		}
		if namespace == "" {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
		if !p.HasNamespace(namespace) || namespace == sharedCatalogNamespace {
			writeAPIError(w, http.StatusForbidden, "forbidden")
			return
		}
	}
	client, err := s.clientForPrincipal(p)
	if err != nil {
		if errors.Is(err, errPrincipalIdentityRequired) {
			writeAPIError(w, http.StatusForbidden, "authenticated user identity required")
			return
		}
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	list, err := client.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{LabelSelector: platformManagedLabel + "=true"})
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": deploymentSummaries(list.Items)})
}

// HandleAdminDeployments lists platform-visible deployments across namespaces for admins.
func (s *DeploymentService) HandleAdminDeployments(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("allow", http.MethodGet)
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	p, ok := principalFromContext(r.Context())
	if !ok || p.Role != roleAdmin {
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	if s.k8sClients == nil {
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}

	namespace := strings.TrimSpace(r.URL.Query().Get("namespace"))
	summaries, err := s.ListAdminDeploymentSummaries(r.Context(), namespace)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "failed to list deployments")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"deployments": summaries})
}

// ListAdminDeploymentSummaries returns deployment summaries from all namespaces or one requested namespace.
func (s *DeploymentService) ListAdminDeploymentSummaries(ctx context.Context, namespace string) ([]map[string]any, error) {
	if s == nil || s.k8sClients == nil {
		return nil, errors.New("kubernetes not available")
	}
	listNamespace := metav1.NamespaceAll
	if namespace != "" {
		listNamespace = namespace
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.k8sClients.Clientset.AppsV1().Deployments(listNamespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, err
	}
	return deploymentSummaries(list.Items), nil
}

func (s *DeploymentService) handleDeploymentApply(w http.ResponseWriter, r *http.Request) {
	p, ok := principalFromContext(r.Context())
	if !ok {
		writeAPIError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req deployRequest
	r.Body = http.MaxBytesReader(w, r.Body, deploymentApplyMaxBytes)
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeBodyDecodeError(w, err)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Image = strings.TrimSpace(req.Image)
	req.Version = strings.TrimSpace(req.Version)
	if req.Port == 0 {
		req.Port = defaultDeployPort
	}
	if req.Replicas == 0 {
		req.Replicas = 1
	}
	if req.Name == "" || req.Image == "" {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, firstNonEmpty(strings.TrimSpace(req.Namespace), p.Namespace), req.Image, "name and image are required"))
		writeAPIError(w, http.StatusBadRequest, "name and image are required")
		return
	}
	namespace := p.Namespace
	if p.Role == roleAdmin && strings.TrimSpace(req.Namespace) != "" {
		namespace = strings.TrimSpace(req.Namespace)
	}
	if p.Role != roleAdmin && strings.TrimSpace(req.Namespace) != "" {
		namespace = strings.TrimSpace(req.Namespace)
	}
	if namespace == "" {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, namespace, req.Image, "namespace required"))
		writeAPIError(w, http.StatusBadRequest, "namespace required")
		return
	}
	if p.Role != roleAdmin && (!p.HasNamespace(namespace) || namespace == sharedCatalogNamespace) {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, namespace, req.Image, "forbidden"))
		writeAPIError(w, http.StatusForbidden, "forbidden")
		return
	}
	image := req.Image
	if req.Version != "" && !strings.Contains(image[strings.LastIndex(image, "/")+1:], ":") {
		if !deployImageTagPattern.MatchString(req.Version) {
			s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, namespace, req.Image, "version must be a valid image tag"))
			writeAPIErrorCode(w, http.StatusBadRequest, apihttp.CodeInvalidVersionTag, "version must be a valid image tag")
			return
		}
		image += ":" + req.Version
	}
	team, teamNamespace := p.TeamForNamespace(namespace)
	teamSlug := ""
	if teamNamespace {
		teamSlug = strings.TrimSpace(team.Slug)
	}
	if p.Role != roleAdmin && !teamNamespace {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, namespace, image, "tenant deployments require a team namespace"))
		writeAPIError(w, http.StatusForbidden, "tenant deployments require a team namespace")
		return
	}
	image = ResolveDeployImageReference(image, namespace, teamSlug)
	if err := ValidateDeployImage(image, namespace, teamSlug, p.Role); err != nil {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "denied", req.Name, namespace, image, err.Error()))
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	client, err := s.clientForPrincipal(p)
	if err != nil {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
		if errors.Is(err, errPrincipalIdentityRequired) {
			writeAPIError(w, http.StatusForbidden, "authenticated user identity required")
			return
		}
		writeAPIError(w, http.StatusServiceUnavailable, "kubernetes not available")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if teamNamespace {
		if err := s.ensureTeamNamespace(ctx, teamRecord{
			ID:        team.ID,
			Slug:      team.Slug,
			Name:      team.Name,
			Namespace: team.Namespace,
		}); err != nil {
			s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
			writeAPIError(w, http.StatusInternalServerError, "failed to ensure team namespace")
			return
		}
		if err := s.ensureNamespaceUserWorkloadRBAC(ctx, namespace, p.UserID()); err != nil {
			s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
			writeAPIError(w, http.StatusInternalServerError, "failed to ensure namespace access")
			return
		}
	} else if p.Role == roleAdmin {
		if err := s.ensureAdminPublishNamespace(ctx, namespace); err != nil {
			s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
			writeAPIError(w, http.StatusInternalServerError, "failed to ensure namespace")
			return
		}
	}
	labels := map[string]string{
		"app.kubernetes.io/name":       req.Name,
		"app.kubernetes.io/managed-by": "mcp-runtime",
		platformManagedLabel:           "true",
		platformUserIDLabel:            p.UserID(),
		createdByLabel:                 p.UserID(),
	}
	if teamNamespace {
		labels[platformTeamIDLabel] = team.ID
		labels[platformTeamSlugLabel] = team.Slug
		labels[platformScopeLabel] = namespaceScopeTeam
	}
	dep := desiredDeployment(req.Name, namespace, image, req.Port, req.Replicas, labels)
	applied, err := upsertDeployment(ctx, client, dep)
	if err != nil {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to apply deployment")
		return
	}
	svc := desiredService(req.Name, namespace, req.Port, labels)
	if _, err := upsertService(ctx, client, svc); err != nil {
		s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "error", req.Name, namespace, image, err.Error()))
		writeAPIError(w, http.StatusInternalServerError, "failed to apply service")
		return
	}
	s.writeAudit(r.Context(), deploymentAuditEvent(r, p, "deployment_apply", "success", req.Name, namespace, image, ""))
	writeJSON(w, http.StatusOK, map[string]any{"deployment": deploymentSummary(*applied)})
}

func (s *DeploymentService) clientForPrincipal(p principal) (kubernetes.Interface, error) {
	if s == nil || s.k8sClients == nil {
		return nil, fmt.Errorf("kubernetes not available")
	}
	if p.UserID() == "" {
		return nil, errPrincipalIdentityRequired
	}
	if s.k8sClients.Config == nil {
		return s.k8sClients.Clientset, nil
	}
	cfg := rest.CopyConfig(s.k8sClients.Config)
	cfg.Impersonate = rest.ImpersonationConfig{
		UserName: "platform:user:" + p.UserID(),
		Groups:   []string{"platform:role:" + p.Role},
	}
	return kubernetes.NewForConfig(cfg)
}

// EnsureCatalogNamespace creates or updates a catalog namespace with platform labels, security defaults, and ingress watch access.
func (s *DeploymentService) EnsureCatalogNamespace(ctx context.Context, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if s == nil || s.k8sClients == nil || namespace == "" {
		return nil
	}
	labels := map[string]string{
		platformManagedLabel:    "true",
		platformScopeLabel:      PlatformMode(),
		podSecurityEnforceLabel: "restricted",
		podSecurityAuditLabel:   "restricted",
		podSecurityWarnLabel:    "restricted",
	}
	cfg := platformTeamTraefikWatchConfig()
	opts := managedNamespaceOptions{
		ingressFromNamespaces: teamIngressAllowNamespaces(cfg),
	}
	if err := s.ensureManagedNamespace(ctx, namespace, labels, opts); err != nil {
		return err
	}
	if cfg.mode != "disabled" {
		if err := s.ensureTeamTraefikWatch(ctx, namespace, cfg); err != nil {
			return err
		}
	}
	return nil
}

func (s *DeploymentService) ensureTeamNamespace(ctx context.Context, team teamRecord) error {
	if strings.TrimSpace(team.Namespace) == "" {
		return errors.New("team namespace required")
	}
	labels := map[string]string{
		platformManagedLabel:    "true",
		platformTeamIDLabel:     strings.TrimSpace(team.ID),
		platformTeamSlugLabel:   strings.TrimSpace(team.Slug),
		platformScopeLabel:      namespaceScopeTeam,
		podSecurityEnforceLabel: "restricted",
		podSecurityAuditLabel:   "restricted",
		podSecurityWarnLabel:    "restricted",
	}
	cfg := platformTeamTraefikWatchConfig()
	opts := managedNamespaceOptions{
		ingressFromNamespaces: teamIngressAllowNamespaces(cfg),
	}
	if err := s.ensureManagedNamespace(ctx, team.Namespace, labels, opts); err != nil {
		return err
	}
	if cfg.mode == "disabled" {
		return nil
	}
	return s.ensureTeamTraefikWatch(ctx, team.Namespace, cfg)
}

func (s *DeploymentService) ensureAdminPublishNamespace(ctx context.Context, namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if s == nil || s.k8sClients == nil || namespace == "" {
		return nil
	}
	if isModeCatalogNamespace(namespace) {
		return s.EnsureCatalogNamespace(ctx, namespace)
	}
	cfg := platformTeamTraefikWatchConfig()
	labels := map[string]string{
		platformManagedLabel:    "true",
		podSecurityEnforceLabel: "restricted",
		podSecurityAuditLabel:   "restricted",
		podSecurityWarnLabel:    "restricted",
	}
	if namespace == sharedCatalogNamespace {
		labels[platformScopeLabel] = PlatformMode()
	}
	opts := managedNamespaceOptions{
		ingressFromNamespaces: teamIngressAllowNamespaces(cfg),
	}
	if err := s.ensureManagedNamespace(ctx, namespace, labels, opts); err != nil {
		return err
	}
	if namespace != sharedCatalogNamespace || cfg.mode == "disabled" {
		return nil
	}
	return s.ensureTeamTraefikWatch(ctx, namespace, cfg)
}

func (s *DeploymentService) ensureManagedNamespace(ctx context.Context, namespace string, labels map[string]string, opts managedNamespaceOptions) error {
	if s == nil || s.k8sClients == nil || strings.TrimSpace(namespace) == "" {
		return nil
	}
	base := s.k8sClients.Clientset
	current, err := base.CoreV1().Namespaces().Get(ctx, namespace, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name:   namespace,
			Labels: labels,
		}}
		if _, err := base.CoreV1().Namespaces().Create(ctx, ns, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if err != nil {
		return err
	} else if mergeNamespaceLabels(current, labels) {
		if _, err := base.CoreV1().Namespaces().Update(ctx, current, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	if err := ensureResourceQuota(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureLimitRange(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureDefaultDenyNetworkPolicy(ctx, base, namespace, opts.ingressFromNamespaces...); err != nil {
		return err
	}
	if err := kubeworkload.EnsureServiceAccount(ctx, base, namespace); err != nil {
		return err
	}
	if err := ensureNamespacePlatformAPISecretAccess(ctx, base, namespace); err != nil {
		return err
	}
	if err := s.ensureNamespaceRegistryPullSecretAfterBinding(ctx, base, namespace); err != nil {
		return fmt.Errorf("provision registry pull secret for namespace %q: %w", namespace, err)
	}
	return nil
}

const registryPullSecretName = "mcp-runtime-registry-pull" // #nosec G101 -- Kubernetes Secret object name, not credential material.
const platformNamespaceAPISecretAccessName = "mcp-runtime-api-team-secrets"
const platformNamespaceAPIServiceAccountName = "mcp-runtime-api"

// A newly created RoleBinding can be visible before the API server's RBAC
// authorizer observes it. Retry only Forbidden errors in this provisioning
// path, after the namespace-local binding has been successfully ensured.
func (s *DeploymentService) ensureNamespaceRegistryPullSecretAfterBinding(ctx context.Context, client kubernetes.Interface, namespace string) error {
	var lastErr error
	err := wait.PollUntilContextTimeout(ctx, 100*time.Millisecond, 5*time.Second, true, func(ctx context.Context) (bool, error) {
		lastErr = s.ensureNamespaceRegistryPullSecret(ctx, client, namespace)
		if apierrors.IsForbidden(lastErr) {
			return false, nil
		}
		return lastErr == nil, lastErr
	})
	if err != nil && apierrors.IsForbidden(lastErr) {
		return fmt.Errorf("waiting for namespace registry secret access: %w (last error: %v)", err, lastErr)
	}
	return err
}

func (s *DeploymentService) ensureNamespaceRegistryPullSecret(ctx context.Context, client kubernetes.Interface, namespace string) error {
	registryHost := registryPullSecretHost()
	if registryHost == "" {
		return nil
	}
	apiKey := registryPullSecretAPIKey()
	if apiKey == "" {
		if registryPullSecretOptional() {
			return nil
		}
		return fmt.Errorf("registry pull secret required for %q but ADMIN_API_KEYS and UI_API_KEY are unset", registryHost)
	}
	auth := base64.StdEncoding.EncodeToString([]byte("platform-service:" + apiKey))
	dockerconfig := fmt.Sprintf(`{"auths":{%q:{"username":"platform-service","password":%q,"auth":%q}}}`,
		registryHost, apiKey, auth)
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: registryPullSecretName, Namespace: namespace},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{
			corev1.DockerConfigJsonKey: []byte(dockerconfig),
		},
	}
	existing, err := client.CoreV1().Secrets(namespace).Get(ctx, registryPullSecretName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := client.CoreV1().Secrets(namespace).Create(ctx, secret, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return err
		}
	} else if err != nil {
		return err
	} else {
		existing.Data = secret.Data
		existing.Type = secret.Type
		if _, err := client.CoreV1().Secrets(namespace).Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
			return err
		}
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sa, err := client.CoreV1().ServiceAccounts(namespace).Get(ctx, kubeworkload.DefaultServiceAccountName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		for _, ref := range sa.ImagePullSecrets {
			if ref.Name == registryPullSecretName {
				return nil
			}
		}
		sa.ImagePullSecrets = append(sa.ImagePullSecrets, corev1.LocalObjectReference{Name: registryPullSecretName})
		_, err = client.CoreV1().ServiceAccounts(namespace).Update(ctx, sa, metav1.UpdateOptions{})
		return err
	})
}

func registryPullSecretHost() string {
	if host := normalizeImageRegistryHost(metadata.ResolveRegistryHost()); host != "" && host != "registry.local" && host != "localhost" {
		return host
	}
	// Keep the secret useful when a public registry override is a local
	// placeholder but an explicit internal endpoint is configured.
	if host := normalizeImageRegistryHost(metadata.ResolveRegistryEndpoint()); host != "" && host != "registry.local" && host != "localhost" {
		return host
	}
	return ""
}

func registryPullSecretAPIKey() string {
	for _, raw := range strings.Split(strings.TrimSpace(os.Getenv("ADMIN_API_KEYS")), ",") {
		if k := strings.TrimSpace(raw); k != "" {
			return k
		}
	}
	return strings.TrimSpace(os.Getenv("UI_API_KEY"))
}

func registryPullSecretOptional() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MCP_REGISTRY_PULL_SECRET_OPTIONAL"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func mergeNamespaceLabels(ns *corev1.Namespace, labels map[string]string) bool {
	if ns.Labels == nil {
		ns.Labels = map[string]string{}
	}
	changed := false
	for key, value := range labels {
		if strings.TrimSpace(key) == "" {
			continue
		}
		if ns.Labels[key] == value {
			continue
		}
		ns.Labels[key] = value
		changed = true
	}
	return changed
}

func ensureNamespacePlatformAPISecretAccess(ctx context.Context, client kubernetes.Interface, namespace string) error {
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: platformNamespaceAPISecretAccessName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     platformNamespaceAPISecretAccessName,
		},
		Subjects: []rbacv1.Subject{
			{
				Kind:      rbacv1.ServiceAccountKind,
				Name:      platformNamespaceAPIServiceAccountName,
				Namespace: sentinel.DefaultNamespace,
			},
		},
	}
	return upsertRoleBinding(ctx, client, binding)
}

func (s *DeploymentService) ensureNamespaceUserWorkloadRBAC(ctx context.Context, namespace, userID string) error {
	if s == nil || s.k8sClients == nil || strings.TrimSpace(namespace) == "" || strings.TrimSpace(userID) == "" {
		return nil
	}
	client := s.k8sClients.Clientset
	role := &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: platformNamespaceOwnerRoleName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{"apps"}, Resources: []string{"deployments"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
			{APIGroups: []string{""}, Resources: []string{"services"}, Verbs: []string{"get", "list", "watch", "create", "update", "patch", "delete"}},
		},
	}
	if err := upsertRole(ctx, client, role); err != nil {
		return err
	}
	binding := &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: platformNamespaceOwnerRoleName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     platformNamespaceOwnerRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: rbacv1.UserKind, Name: "platform:user:" + strings.TrimSpace(userID)},
		},
	}
	return upsertRoleBinding(ctx, client, binding)
}

func ensureResourceQuota(ctx context.Context, client kubernetes.Interface, ns string) error {
	quota := &corev1.ResourceQuota{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-quota", Namespace: ns}, Spec: corev1.ResourceQuotaSpec{Hard: corev1.ResourceList{
		corev1.ResourcePods:                   resource.MustParse("20"),
		corev1.ResourceRequestsCPU:            resource.MustParse("4"),
		corev1.ResourceRequestsMemory:         resource.MustParse("8Gi"),
		corev1.ResourceLimitsCPU:              resource.MustParse("8"),
		corev1.ResourceLimitsMemory:           resource.MustParse("16Gi"),
		corev1.ResourcePersistentVolumeClaims: resource.MustParse("4"),
	}}}
	if _, err := client.CoreV1().ResourceQuotas(ns).Create(ctx, quota, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureLimitRange(ctx context.Context, client kubernetes.Interface, ns string) error {
	limit := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Name: "platform-default-limits", Namespace: ns}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{
		Type: corev1.LimitTypeContainer,
		DefaultRequest: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("128Mi"),
		},
		Default: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("500m"),
			corev1.ResourceMemory: resource.MustParse("512Mi"),
		},
	}}}}
	if _, err := client.CoreV1().LimitRanges(ns).Create(ctx, limit, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return err
	}
	return nil
}

func ensureDefaultDenyNetworkPolicy(ctx context.Context, client kubernetes.Interface, ns string, ingressFromNamespaces ...string) error {
	policy := desiredDefaultDenyNetworkPolicy(ns, ingressFromNamespaces...)
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.NetworkingV1().NetworkPolicies(ns).Get(ctx, policy.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = client.NetworkingV1().NetworkPolicies(ns).Create(ctx, policy, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		updated := policy.DeepCopy()
		updated.ResourceVersion = current.ResourceVersion
		updated.Labels = current.Labels
		updated.Annotations = current.Annotations
		_, err = client.NetworkingV1().NetworkPolicies(ns).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

func desiredDefaultDenyNetworkPolicy(ns string, ingressFromNamespaces ...string) *networkingv1.NetworkPolicy {
	udpProtocol := corev1.ProtocolUDP
	tcpProtocol := corev1.ProtocolTCP
	ingress := make([]networkingv1.NetworkPolicyIngressRule, 0, 2)
	ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{
			{PodSelector: &metav1.LabelSelector{}},
		},
	})
	for _, namespace := range ingressFromNamespaces {
		namespace = strings.TrimSpace(namespace)
		if namespace == "" {
			continue
		}
		ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
			From: []networkingv1.NetworkPolicyPeer{
				{
					NamespaceSelector: &metav1.LabelSelector{
						MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace},
					},
				},
			},
		})
	}
	ingress = append(ingress, networkingv1.NetworkPolicyIngressRule{
		From: []networkingv1.NetworkPolicyPeer{
			{
				NamespaceSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"kubernetes.io/metadata.name": sentinel.DefaultNamespace},
				},
			},
		},
	})
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{Name: "platform-default-deny", Namespace: ns},
		Spec: networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress, networkingv1.PolicyTypeEgress},
			Ingress:     ingress,
			Egress: []networkingv1.NetworkPolicyEgressRule{
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": "kube-system"},
							},
							PodSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"k8s-app": "kube-dns"},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &udpProtocol, Port: intstrPtr(53)},
						{Protocol: &tcpProtocol, Port: intstrPtr(53)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": sentinel.DefaultNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: intstrPtr(sentinelIngestPort)},
						{Protocol: &tcpProtocol, Port: intstrPtr(sentinelOTLPPort)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{
							NamespaceSelector: &metav1.LabelSelector{
								MatchLabels: map[string]string{"kubernetes.io/metadata.name": registryNamespace},
							},
						},
					},
					Ports: []networkingv1.NetworkPolicyPort{
						{Protocol: &tcpProtocol, Port: intstrPtr(registryPort)},
					},
				},
				{
					To: []networkingv1.NetworkPolicyPeer{
						{PodSelector: &metav1.LabelSelector{}},
					},
				},
			},
		},
	}
	return policy
}

func teamIngressAllowNamespaces(cfg teamTraefikWatchConfig) []string {
	if ns := strings.TrimSpace(envOr("PLATFORM_TRAEFIK_NAMESPACE", "")); ns != "" {
		return []string{ns}
	}
	if cfg.mode == "disabled" {
		// External ingress controllers (for example k3s Traefik in kube-system) are
		// not patched through repo-managed traefik/traefik, but team routes still
		// need ingress from that namespace.
		return []string{"kube-system"}
	}
	if ns := strings.TrimSpace(cfg.namespace); ns != "" {
		return []string{ns}
	}
	return nil
}

func platformTeamTraefikWatchConfig() teamTraefikWatchConfig {
	mode := strings.ToLower(strings.TrimSpace(envOr("PLATFORM_TEAM_TRAEFIK_WATCH", "auto")))
	switch mode {
	case "", "auto":
		mode = "auto"
	case "false", "off", "0", "no", "disabled":
		mode = "disabled"
	case "true", "on", "1", "yes", "required":
		mode = "required"
	default:
		mode = "auto"
	}
	return teamTraefikWatchConfig{
		mode:           mode,
		namespace:      envOr("PLATFORM_TRAEFIK_NAMESPACE", "traefik"),
		deployment:     envOr("PLATFORM_TRAEFIK_DEPLOYMENT", "traefik"),
		serviceAccount: envOr("PLATFORM_TRAEFIK_SERVICE_ACCOUNT", "traefik"),
	}
}

func (s *DeploymentService) ensureTeamTraefikWatch(ctx context.Context, namespace string, cfg teamTraefikWatchConfig) error {
	if s == nil || s.k8sClients == nil || strings.TrimSpace(namespace) == "" {
		return nil
	}
	if cfg.mode == "disabled" {
		return nil
	}
	// k3s Traefik watches ingresses cluster-wide without namespace-scoped args.
	if cfg.mode == "auto" && strings.TrimSpace(cfg.namespace) == "kube-system" {
		return nil
	}
	if cfg.mode == "auto" {
		managed, err := traefikDeploymentHasNamespaceWatchArg(ctx, s.k8sClients.Clientset, cfg)
		if err != nil {
			return err
		}
		if !managed {
			return nil
		}
	}
	if err := ensureTraefikWatchRBAC(ctx, s.k8sClients.Clientset, namespace, cfg); err != nil {
		return err
	}
	if err := ensureTraefikDeploymentWatchesNamespace(ctx, s.k8sClients.Clientset, namespace, cfg); err != nil {
		if cfg.mode == "auto" && apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func ensureTraefikWatchRBAC(ctx context.Context, client kubernetes.Interface, namespace string, cfg teamTraefikWatchConfig) error {
	role := desiredTraefikWatchRole(namespace)
	if err := ensureTraefikWatchRole(ctx, client, role); err != nil {
		return err
	}
	binding := desiredTraefikWatchRoleBinding(namespace, cfg)
	return ensureTraefikWatchRoleBinding(ctx, client, binding)
}

func desiredTraefikWatchRole(namespace string) *rbacv1.Role {
	return &rbacv1.Role{
		ObjectMeta: metav1.ObjectMeta{Name: traefikWatchRoleName, Namespace: namespace},
		Rules: []rbacv1.PolicyRule{
			{APIGroups: []string{""}, Resources: []string{"services", "endpoints", "secrets"}, Verbs: []string{"get", "list", "watch"}},
			{APIGroups: []string{"networking.k8s.io"}, Resources: []string{"ingresses"}, Verbs: []string{"get", "list", "watch"}},
		},
	}
}

func ensureTraefikWatchRole(ctx context.Context, client kubernetes.Interface, role *rbacv1.Role) error {
	current, err := client.RbacV1().Roles(role.Namespace).Get(ctx, role.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().Roles(role.Namespace).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if roleRulesMatch(current, role) {
		return nil
	}
	role.ResourceVersion = current.ResourceVersion
	role.Labels = current.Labels
	role.Annotations = current.Annotations
	_, err = client.RbacV1().Roles(role.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	return err
}

func roleRulesMatch(current, desired *rbacv1.Role) bool {
	if current == nil || desired == nil {
		return false
	}
	if len(current.Rules) != len(desired.Rules) {
		return false
	}
	for i := range desired.Rules {
		if !policyRuleMatches(current.Rules[i], desired.Rules[i]) {
			return false
		}
	}
	return true
}

func policyRuleMatches(current, desired rbacv1.PolicyRule) bool {
	return slicesEqual(current.APIGroups, desired.APIGroups) &&
		slicesEqual(current.Resources, desired.Resources) &&
		slicesEqual(current.Verbs, desired.Verbs)
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func desiredTraefikWatchRoleBinding(namespace string, cfg teamTraefikWatchConfig) *rbacv1.RoleBinding {
	return &rbacv1.RoleBinding{
		ObjectMeta: metav1.ObjectMeta{Name: traefikWatchRoleName, Namespace: namespace},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "Role",
			Name:     traefikWatchRoleName,
		},
		Subjects: []rbacv1.Subject{
			{Kind: "ServiceAccount", Name: cfg.serviceAccount, Namespace: cfg.namespace},
		},
	}
}

func ensureTraefikWatchRoleBinding(ctx context.Context, client kubernetes.Interface, binding *rbacv1.RoleBinding) error {
	current, err := client.RbacV1().RoleBindings(binding.Namespace).Get(ctx, binding.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().RoleBindings(binding.Namespace).Create(ctx, binding, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	if roleBindingMatches(current, binding) {
		return nil
	}
	binding.ResourceVersion = current.ResourceVersion
	binding.Labels = current.Labels
	binding.Annotations = current.Annotations
	_, err = client.RbacV1().RoleBindings(binding.Namespace).Update(ctx, binding, metav1.UpdateOptions{})
	return err
}

func roleBindingMatches(current, desired *rbacv1.RoleBinding) bool {
	if current == nil || desired == nil {
		return false
	}
	if current.RoleRef != desired.RoleRef || len(current.Subjects) != len(desired.Subjects) {
		return false
	}
	for i := range desired.Subjects {
		if current.Subjects[i] != desired.Subjects[i] {
			return false
		}
	}
	return true
}

func upsertRole(ctx context.Context, client kubernetes.Interface, role *rbacv1.Role) error {
	current, err := client.RbacV1().Roles(role.Namespace).Get(ctx, role.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		_, err = client.RbacV1().Roles(role.Namespace).Create(ctx, role, metav1.CreateOptions{})
		return err
	}
	if err != nil {
		return err
	}
	role.ResourceVersion = current.ResourceVersion
	role.Labels = current.Labels
	role.Annotations = current.Annotations
	_, err = client.RbacV1().Roles(role.Namespace).Update(ctx, role, metav1.UpdateOptions{})
	return err
}

func upsertRoleBinding(ctx context.Context, client kubernetes.Interface, binding *rbacv1.RoleBinding) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.RbacV1().RoleBindings(binding.Namespace).Get(ctx, binding.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			_, err = client.RbacV1().RoleBindings(binding.Namespace).Create(ctx, binding, metav1.CreateOptions{})
			return err
		}
		if err != nil {
			return err
		}
		if roleBindingMatches(current, binding) {
			return nil
		}
		updated := binding.DeepCopy()
		updated.ResourceVersion = current.ResourceVersion
		updated.Labels = current.Labels
		updated.Annotations = current.Annotations
		_, err = client.RbacV1().RoleBindings(binding.Namespace).Update(ctx, updated, metav1.UpdateOptions{})
		return err
	})
}

func traefikDeploymentHasNamespaceWatchArg(ctx context.Context, client kubernetes.Interface, cfg teamTraefikWatchConfig) (bool, error) {
	deployment, err := client.AppsV1().Deployments(cfg.namespace).Get(ctx, cfg.deployment, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	_, _, _, ok := traefikNamespaceWatchArg(deployment)
	return ok, nil
}

func ensureTraefikDeploymentWatchesNamespace(ctx context.Context, client kubernetes.Interface, namespace string, cfg teamTraefikWatchConfig) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := client.AppsV1().Deployments(cfg.namespace).Get(ctx, cfg.deployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		containerIndex, argIndex, argValue, ok := traefikNamespaceWatchArg(deployment)
		if !ok {
			if cfg.mode == "auto" {
				return nil
			}
			return errors.New("traefik deployment does not expose --providers.kubernetesingress.namespaces")
		}
		watched := splitCSV(strings.TrimPrefix(argValue, traefikNamespaceWatchArgPrefix))
		for _, watchedNamespace := range watched {
			if watchedNamespace == namespace {
				return nil
			}
		}
		watched = append(watched, namespace)
		updated := deployment.DeepCopy()
		updated.Spec.Template.Spec.Containers[containerIndex].Args[argIndex] = traefikNamespaceWatchArgPrefix + strings.Join(watched, ",")
		appendTraefikNamespaceWatch(updated, traefikCRDNamespaceWatchArgPrefix, namespace)
		_, err = client.AppsV1().Deployments(cfg.namespace).Update(ctx, updated, metav1.UpdateOptions{})
		if apierrors.IsConflict(err) {
			return err
		}
		if err != nil {
			return err
		}
		return nil
	})
}

const traefikNamespaceWatchArgPrefix = "--providers.kubernetesingress.namespaces="
const traefikCRDNamespaceWatchArgPrefix = "--providers.kubernetescrd.namespaces="

func appendTraefikNamespaceWatch(deployment *appsv1.Deployment, prefix, namespace string) {
	if deployment == nil {
		return
	}
	for ci := range deployment.Spec.Template.Spec.Containers {
		for ai, arg := range deployment.Spec.Template.Spec.Containers[ci].Args {
			if !strings.HasPrefix(arg, prefix) {
				continue
			}
			watched := splitCSV(strings.TrimPrefix(arg, prefix))
			for _, watchedNamespace := range watched {
				if watchedNamespace == namespace {
					return
				}
			}
			watched = append(watched, namespace)
			deployment.Spec.Template.Spec.Containers[ci].Args[ai] = prefix + strings.Join(watched, ",")
			return
		}
	}
}

func traefikNamespaceWatchArg(deployment *appsv1.Deployment) (int, int, string, bool) {
	if deployment == nil {
		return -1, -1, "", false
	}
	containerIndex := -1
	argIndex := -1
	argValue := ""
	for ci, container := range deployment.Spec.Template.Spec.Containers {
		for ai, arg := range container.Args {
			if strings.HasPrefix(arg, traefikNamespaceWatchArgPrefix) {
				containerIndex = ci
				argIndex = ai
				argValue = arg
				break
			}
		}
		if containerIndex >= 0 {
			break
		}
	}
	if containerIndex < 0 {
		return -1, -1, "", false
	}
	return containerIndex, argIndex, argValue, true
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func desiredDeployment(name, namespace, image string, port, replicas int32, labels map[string]string) *appsv1.Deployment {
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: appsv1.DeploymentSpec{
		Replicas: &replicas,
		Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app.kubernetes.io/name": name}},
		Template: corev1.PodTemplateSpec{
			ObjectMeta: metav1.ObjectMeta{Labels: labels},
			Spec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:            "server",
					Image:           image,
					Ports:           []corev1.ContainerPort{{ContainerPort: port}},
					SecurityContext: kubeworkload.RestrictedContainerSecurityContext(),
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("100m"),
							corev1.ResourceMemory: resource.MustParse("128Mi"),
						},
						Limits: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("500m"),
							corev1.ResourceMemory: resource.MustParse("512Mi"),
						},
					},
					ReadinessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstrFromInt32(port)},
						},
						InitialDelaySeconds: 5,
						PeriodSeconds:       10,
					},
					LivenessProbe: &corev1.Probe{
						ProbeHandler: corev1.ProbeHandler{
							TCPSocket: &corev1.TCPSocketAction{Port: intstrFromInt32(port)},
						},
						InitialDelaySeconds: 30,
						PeriodSeconds:       20,
					},
				}},
			},
		},
	}}
	kubeworkload.ApplyRestrictedPodDefaults(&deployment.Spec.Template.Spec)
	return deployment
}

func desiredService(name, namespace string, port int32, labels map[string]string) *corev1.Service {
	return &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: labels}, Spec: corev1.ServiceSpec{
		Selector: map[string]string{"app.kubernetes.io/name": name},
		Ports:    []corev1.ServicePort{{Name: "http", Port: 80, TargetPort: intstrFromInt32(port)}},
		Type:     corev1.ServiceTypeClusterIP,
	}}
}

func intstrPtr(port int) *intstr.IntOrString {
	v := intstr.FromInt(port)
	return &v
}

func upsertDeployment(ctx context.Context, client kubernetes.Interface, dep *appsv1.Deployment) (*appsv1.Deployment, error) {
	existing, err := client.AppsV1().Deployments(dep.Namespace).Get(ctx, dep.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return client.AppsV1().Deployments(dep.Namespace).Create(ctx, dep, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	existing.Labels = dep.Labels
	existing.Spec = dep.Spec
	return client.AppsV1().Deployments(dep.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
}

func upsertService(ctx context.Context, client kubernetes.Interface, svc *corev1.Service) (*corev1.Service, error) {
	existing, err := client.CoreV1().Services(svc.Namespace).Get(ctx, svc.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return client.CoreV1().Services(svc.Namespace).Create(ctx, svc, metav1.CreateOptions{})
	}
	if err != nil {
		return nil, err
	}
	existing.Labels = svc.Labels
	existing.Spec.Ports = svc.Spec.Ports
	existing.Spec.Selector = svc.Spec.Selector
	return client.CoreV1().Services(svc.Namespace).Update(ctx, existing, metav1.UpdateOptions{})
}

// ValidateDeployImage enforces registry allow-list and namespace-scoped repository rules for deployment images.
func ValidateDeployImage(image, namespace, teamSlug, role string) error {
	parts := strings.Split(image, "/")
	if len(parts) < 2 {
		return fmt.Errorf("image must include a registry/repository path")
	}
	if approved := approvedRegistries(); len(approved) > 0 {
		host := parts[0]
		if _, ok := approved[host]; !ok {
			return fmt.Errorf("registry %q is not approved", host)
		}
	}
	if role != roleAdmin {
		expected := allowedImageRepositoryScopes(namespace, teamSlug)
		if len(parts) < 3 || !stringInSlice(parts[1], expected) {
			return fmt.Errorf("image repository must be scoped to %s", strings.Join(quoteStrings(expected), " or "))
		}
	}
	return nil
}

// ResolveDeployImageReference expands short image names into the platform registry and namespace repository scope.
func ResolveDeployImageReference(image, namespace, teamSlug string) string {
	image = strings.TrimSpace(image)
	if image == "" {
		return image
	}
	registry := defaultPlatformRegistryHost()
	if registry == "" {
		return image
	}
	if imageReferenceHasRegistry(image) {
		return rewritePlatformRegistryImageReference(image, registry)
	}
	parts := strings.Split(image, "/")
	allowedScopes := allowedImageRepositoryScopes(namespace, teamSlug)
	if len(parts) > 1 && stringInSlice(parts[0], allowedScopes) {
		return registry + "/" + image
	}
	scope := defaultImageRepositoryScope(namespace, teamSlug)
	if scope == "" {
		return registry + "/" + image
	}
	return registry + "/" + scope + "/" + image
}

func imageReferenceHasRegistry(image string) bool {
	image = strings.TrimSpace(image)
	if image == "" {
		return false
	}
	first, _, found := strings.Cut(image, "/")
	if !found {
		return false
	}
	return strings.Contains(first, ".") || strings.Contains(first, ":") || first == "localhost"
}

func defaultPlatformRegistryHost() string {
	for _, key := range []string{
		"MCP_REGISTRY_PULL_HOST",
		"MCP_REGISTRY_ENDPOINT",
	} {
		if host := normalizeImageRegistryHost(os.Getenv(key)); host != "" {
			return host
		}
	}
	return "registry.registry.svc.cluster.local:5000"
}

func normalizeImageRegistryHost(value string) string {
	value = strings.TrimSpace(value)
	value = strings.TrimSuffix(value, "/")
	if value == "" {
		return ""
	}
	if scheme := strings.Index(value, "://"); scheme >= 0 {
		value = value[scheme+3:]
	}
	if before, _, found := strings.Cut(value, "/"); found {
		value = before
	}
	return strings.TrimSpace(value)
}

// bundledRegistryServiceHost is the cluster DNS name of the bundled registry
// Service. It resolves only inside the cluster network, not on nodes.
const bundledRegistryServiceHost = "registry.registry.svc.cluster.local"

func isBundledRegistryServiceHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if name, _, found := strings.Cut(host, ":"); found {
		host = name
	}
	return host == bundledRegistryServiceHost || host == "registry.registry.svc"
}

func rewritePlatformRegistryImageReference(image, internalRegistry string) string {
	internalRegistry = normalizeImageRegistryHost(internalRegistry)
	if internalRegistry == "" {
		return image
	}
	currentRegistry, rest, found := strings.Cut(strings.TrimSpace(image), "/")
	if !found {
		return image
	}
	currentRegistry = normalizeImageRegistryHost(currentRegistry)
	if currentRegistry == "" || currentRegistry == internalRegistry {
		return image
	}
	// The bundled in-cluster registry DNS name is only pullable where the
	// node runtime mirrors it (Kind test-mode). When the platform pulls from a
	// different host, a ref naming the bundled service can never be pulled by
	// the kubelet, so map it onto the platform pull host instead of deploying
	// an ImagePullBackOff.
	if isBundledRegistryServiceHost(currentRegistry) {
		return internalRegistry + "/" + rest
	}
	for _, host := range []string{
		os.Getenv("PLATFORM_REGISTRY_URL"),
		os.Getenv("PROVISIONED_REGISTRY_URL"),
		os.Getenv("MCP_REGISTRY_INGRESS_HOST"),
		os.Getenv("MCP_REGISTRY_HOST"),
	} {
		if normalizeImageRegistryHost(host) == currentRegistry {
			return internalRegistry + "/" + rest
		}
	}
	if domain := normalizeImageRegistryHost(os.Getenv("MCP_PLATFORM_DOMAIN")); domain != "" {
		if currentRegistry == "registry."+strings.TrimPrefix(domain, "registry.") {
			return internalRegistry + "/" + rest
		}
	}
	return image
}

func defaultImageRepositoryScope(namespace, teamSlug string) string {
	teamSlug = strings.TrimSpace(teamSlug)
	if teamSlug != "" {
		return teamSlug
	}
	if isModeCatalogNamespace(namespace) {
		switch PlatformMode() {
		case platformModePublic:
			return publishscope.PublicRegistryAlias
		case platformModeOrg:
			return publishscope.OrgRegistryAlias
		}
	}
	return strings.TrimSpace(namespace)
}

func allowedImageRepositoryScopes(namespace, teamSlug string) []string {
	expected := strings.TrimSpace(namespace)
	if strings.TrimSpace(teamSlug) != "" {
		expected = strings.TrimSpace(teamSlug)
	}
	values := []string{}
	if expected != "" {
		values = append(values, expected)
	}
	if isModeCatalogNamespace(namespace) {
		switch PlatformMode() {
		case platformModePublic:
			values = append(values, publishscope.PublicRegistryAlias)
		case platformModeOrg:
			values = append(values, publishscope.OrgRegistryAlias)
		}
	}
	return dedupeNonEmptyStrings(values)
}

func stringInSlice(value string, allowed []string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func quoteStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		out = append(out, fmt.Sprintf("%q", value))
	}
	return out
}

func approvedRegistries() map[string]struct{} {
	raw := strings.TrimSpace(envOr("APPROVED_REGISTRIES", ""))
	if raw == "" {
		return nil
	}
	out := map[string]struct{}{}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out[p] = struct{}{}
		}
	}
	return out
}

func deploymentSummaries(items []appsv1.Deployment) []map[string]any {
	out := make([]map[string]any, 0, len(items))
	for _, item := range items {
		out = append(out, deploymentSummary(item))
	}
	return out
}

func deploymentSummary(d appsv1.Deployment) map[string]any {
	replicas := int32(0)
	if d.Spec.Replicas != nil {
		replicas = *d.Spec.Replicas
	}
	return map[string]any{
		"name":       d.Name,
		"namespace":  d.Namespace,
		"replicas":   replicas,
		"ready":      d.Status.ReadyReplicas,
		"image":      firstContainerImage(d),
		"user_id":    d.Labels[platformUserIDLabel],
		"created_by": d.Labels[createdByLabel],
		"labels":     d.Labels,
		"created_at": d.CreationTimestamp.Time,
	}
}

func firstContainerImage(d appsv1.Deployment) string {
	if len(d.Spec.Template.Spec.Containers) == 0 {
		return ""
	}
	return d.Spec.Template.Spec.Containers[0].Image
}

func deploymentAuditEvent(r *http.Request, p principal, action, status, name, namespace, image, message string) auditEvent {
	target := strings.Trim(strings.TrimSpace(namespace)+"/"+strings.TrimSpace(name), "/")
	return auditEvent{
		UserID:           p.UserID(),
		Action:           action,
		Resource:         strings.TrimSpace(name),
		Namespace:        strings.TrimSpace(namespace),
		Status:           status,
		Message:          strings.TrimSpace(message),
		ActorIP:          requestIP(r),
		Source:           auditSource(r, p),
		AuthIdentity:     auditIdentityLabel(p),
		ImageRef:         strings.TrimSpace(image),
		ServerName:       strings.TrimSpace(name),
		DeploymentTarget: target,
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func extractNamespaceName(path, prefix string) (string, string, error) {
	path = serviceutil.NormalizePublicAPIPath(path)
	prefix = serviceutil.NormalizePublicAPIPath(prefix)
	trimmed := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.Split(trimmed, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("expected /namespace/name")
	}
	return parts[0], parts[1], nil
}

func intstrFromInt32(v int32) intstr.IntOrString {
	return intstr.FromInt(int(v))
}

// ReconcileTeamNamespaceNetworkPolicies refreshes default-deny NetworkPolicies for
// existing team namespaces so ingress controller namespace changes take effect
// without recreating teams.
func (s *DeploymentService) ReconcileTeamNamespaceNetworkPolicies(ctx context.Context) {
	if s == nil || s.k8sClients == nil || s.k8sClients.Clientset == nil {
		return
	}
	cfg := platformTeamTraefikWatchConfig()
	ingress := teamIngressAllowNamespaces(cfg)
	list, err := s.k8sClients.Clientset.CoreV1().Namespaces().List(ctx, metav1.ListOptions{
		LabelSelector: platformScopeLabel + "=" + namespaceScopeTeam,
	})
	if err != nil {
		log.Printf("reconcile team namespace network policies: list namespaces: %v", err)
		return
	}
	for _, ns := range list.Items {
		if err := ensureDefaultDenyNetworkPolicy(ctx, s.k8sClients.Clientset, ns.Name, ingress...); err != nil {
			log.Printf("reconcile team namespace network policies: namespace %q: %v", ns.Name, err)
		}
	}
	s.purgeExpiredRegistryPushTransfers(ctx)
}
