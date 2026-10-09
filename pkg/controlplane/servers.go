package controlplane

import (
	"context"
	"errors"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/credentialheaders"
	"mcp-runtime/pkg/metadata"
)

const stableDeploymentSelector = "app.kubernetes.io/managed-by=mcp-runtime,mcpruntime.org/rollout-track=stable"

// MCPServerGVR is the dynamic-client resource identity for MCPServer objects.
var MCPServerGVR = mcpv1alpha1.GroupVersion.WithResource(mcpv1alpha1.MCPServerResource)

type ListServersOptions struct {
	LabelSelector        string
	SkipDeploymentStatus bool
}

// ListServers lists MCPServer resources in namespace and joins them with the
// readiness of their stable backing Deployments. If the MCPServer resource is
// unavailable, it falls back to legacy managed Deployments.
func (m *Manager) ListServers(ctx context.Context, namespace string) (ListServersResult, error) {
	return m.ListServersWithOptions(ctx, namespace, ListServersOptions{})
}

// ListServersWithOptions lists MCPServer resources in namespace using optional
// Kubernetes list filters.
func (m *Manager) ListServersWithOptions(ctx context.Context, namespace string, opts ListServersOptions) (ListServersResult, error) {
	clients, err := m.requireClients()
	if err != nil {
		return ListServersResult{}, err
	}
	namespace = strings.TrimSpace(namespace)
	opts.LabelSelector = strings.TrimSpace(opts.LabelSelector)

	serverObjects, crdErr := clients.Dynamic.Resource(MCPServerGVR).Namespace(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: opts.LabelSelector,
	})
	if crdErr == nil {
		// There is no deployment status to join when the catalog is empty. Apart
		// from avoiding an unnecessary API call, this keeps an empty catalog
		// healthy when a caller can list MCPServers but cannot list Deployments.
		if len(serverObjects.Items) == 0 {
			return ListServersResult{Servers: []ServerInfo{}}, nil
		}
		deploymentStatus := map[string]ServerDeploymentStatus{}
		if !opts.SkipDeploymentStatus {
			deployments, deployErr := clients.Clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
				LabelSelector: combineLabelSelectors(stableDeploymentSelector, opts.LabelSelector),
			})
			if deployErr == nil {
				for _, d := range deployments.Items {
					deploymentStatus[d.Name] = StatusForDeployment(d)
				}
			}
		}

		servers := make([]ServerInfo, 0, len(serverObjects.Items))
		for _, obj := range serverObjects.Items {
			var mcpServer mcpv1alpha1.MCPServer
			if convertErr := apiruntime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &mcpServer); convertErr != nil {
				continue
			}
			servers = append(servers, ServerInfoFromMCPServer(mcpServer, deploymentStatus[mcpServer.Name]))
		}
		return ListServersResult{Servers: servers}, nil
	}

	deployments, err := clients.Clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: combineLabelSelectors(stableDeploymentSelector, opts.LabelSelector),
	})
	if err != nil {
		return ListServersResult{CRDError: crdErr, UsedDeploymentFallback: true}, err
	}

	servers := make([]ServerInfo, 0, len(deployments.Items))
	for _, d := range deployments.Items {
		deploymentStatus := StatusForDeployment(d)
		servers = append(servers, ServerInfo{
			Name:      d.Name,
			Namespace: d.Namespace,
			UID:       string(d.UID),
			Ready:     deploymentStatus.Ready,
			Status:    deploymentStatus.Status,
			Labels:    d.Labels,
			Age:       d.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
			Prompts:   []mcpv1alpha1.InventoryItem{},
			Resources: []mcpv1alpha1.InventoryItem{},
			Tasks:     []mcpv1alpha1.InventoryItem{},
		})
	}

	return ListServersResult{
		Servers:                servers,
		CRDError:               crdErr,
		UsedDeploymentFallback: true,
	}, nil
}

func combineLabelSelectors(selectors ...string) string {
	out := make([]string, 0, len(selectors))
	for _, selector := range selectors {
		selector = strings.TrimSpace(selector)
		if selector != "" {
			out = append(out, selector)
		}
	}
	return strings.Join(out, ",")
}

// ApplyServer creates or updates an MCPServer resource.
func (m *Manager) ApplyServer(ctx context.Context, server *mcpv1alpha1.MCPServer) (*mcpv1alpha1.MCPServer, error) {
	clients, err := m.requireClients()
	if err != nil {
		return nil, err
	}
	if server == nil {
		return nil, errors.New("server cannot be nil")
	}
	if strings.TrimSpace(server.Name) == "" {
		return nil, errors.New("server name is required")
	}
	if strings.TrimSpace(server.Namespace) == "" {
		return nil, errors.New("server namespace is required")
	}

	payload, err := apiruntime.DefaultUnstructuredConverter.ToUnstructured(server)
	if err != nil {
		return nil, fmt.Errorf("encode MCPServer: %w", err)
	}
	resource := &unstructured.Unstructured{Object: payload}

	current, err := clients.Dynamic.Resource(MCPServerGVR).Namespace(server.Namespace).Get(ctx, server.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		applied, createErr := clients.Dynamic.Resource(MCPServerGVR).Namespace(server.Namespace).Create(ctx, resource, metav1.CreateOptions{})
		if createErr != nil {
			return nil, createErr
		}
		return decodeMCPServer(applied, "created")
	}
	if err != nil {
		return nil, err
	}

	resource.SetResourceVersion(current.GetResourceVersion())
	applied, err := clients.Dynamic.Resource(MCPServerGVR).Namespace(server.Namespace).Update(ctx, resource, metav1.UpdateOptions{})
	if err != nil {
		return nil, err
	}
	return decodeMCPServer(applied, "updated")
}

// GetServer retrieves one MCPServer resource.
func (m *Manager) GetServer(ctx context.Context, namespace, name string) (*mcpv1alpha1.MCPServer, error) {
	clients, err := m.requireClients()
	if err != nil {
		return nil, err
	}
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" {
		return nil, errors.New("server namespace is required")
	}
	if name == "" {
		return nil, errors.New("server name is required")
	}
	current, err := clients.Dynamic.Resource(MCPServerGVR).Namespace(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return nil, err
	}
	return decodeMCPServer(current, "fetched")
}

// GetServerInfo retrieves one MCPServer and joins it with its backing
// Deployment readiness when the Deployment is available.
func (m *Manager) GetServerInfo(ctx context.Context, namespace, name string) (ServerInfo, error) {
	current, err := m.GetServer(ctx, namespace, name)
	if err != nil {
		return ServerInfo{}, err
	}
	clients, err := m.requireClients()
	if err != nil {
		return ServerInfo{}, err
	}
	deploymentStatus := ServerDeploymentStatus{}
	deployment, err := clients.Clientset.AppsV1().Deployments(namespace).Get(ctx, name, metav1.GetOptions{})
	if err == nil {
		deploymentStatus = StatusForDeployment(*deployment)
	}
	return ServerInfoFromMCPServer(*current, deploymentStatus), nil
}

// DeleteServer deletes one MCPServer resource.
func (m *Manager) DeleteServer(ctx context.Context, namespace, name string) error {
	clients, err := m.requireClients()
	if err != nil {
		return err
	}
	namespace = strings.TrimSpace(namespace)
	name = strings.TrimSpace(name)
	if namespace == "" {
		return errors.New("server namespace is required")
	}
	if name == "" {
		return errors.New("server name is required")
	}
	err = clients.Dynamic.Resource(MCPServerGVR).Namespace(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}

func decodeMCPServer(obj *unstructured.Unstructured, action string) (*mcpv1alpha1.MCPServer, error) {
	var out mcpv1alpha1.MCPServer
	if err := apiruntime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &out); err != nil {
		return nil, fmt.Errorf("decode %s MCPServer: %w", action, err)
	}
	return &out, nil
}

// StatusForDeployment summarizes Deployment readiness in the format used by
// runtime server listings.
func StatusForDeployment(d appsv1.Deployment) ServerDeploymentStatus {
	status := "NotReady"
	desiredReplicas := DeploymentDesiredReplicas(d, 0)
	ready := fmt.Sprintf("%d/%d", d.Status.ReadyReplicas, desiredReplicas)
	if d.Status.ReadyReplicas == desiredReplicas && desiredReplicas > 0 {
		status = "Ready"
	} else if d.Status.ReadyReplicas > 0 {
		status = "Degraded"
	}
	return ServerDeploymentStatus{Ready: ready, Status: status}
}

// DeploymentDesiredReplicas returns the desired replica count, using
// defaultReplicas when the Deployment does not set spec.replicas.
func DeploymentDesiredReplicas(d appsv1.Deployment, defaultReplicas int32) int32 {
	if d.Spec.Replicas != nil {
		return *d.Spec.Replicas
	}
	return defaultReplicas
}

// DeploymentReady reports whether the Deployment has the desired number of
// ready replicas.
func DeploymentReady(d appsv1.Deployment, defaultReplicas int32) bool {
	return d.Status.ReadyReplicas == DeploymentDesiredReplicas(d, defaultReplicas)
}

// ServerInfoFromMCPServer projects an MCPServer plus optional Deployment status
// into the control-plane server summary shape.
func ServerInfoFromMCPServer(mcpServer mcpv1alpha1.MCPServer, deploymentStatus ServerDeploymentStatus) ServerInfo {
	if deploymentStatus.Ready == "" {
		deploymentStatus = ServerDeploymentStatus{Ready: "0/0", Status: strings.TrimSpace(mcpServer.Status.Phase)}
		if deploymentStatus.Status == "" {
			deploymentStatus.Status = "Unknown"
		}
	}
	status := deploymentStatus.Status
	// Replica readiness alone is not enough for gateway servers: pods can be
	// Ready while Traefik mTLS Secrets/IngressRoute are still forming. Do not
	// claim Ready until the operator phase is Ready so CLI deploy waits and
	// the UI does not advertise an unusable public route.
	if status == "Ready" && mcpv1alpha1.GatewayIsEnabled(mcpServer.Spec.Gateway) {
		switch phase := strings.TrimSpace(mcpServer.Status.Phase); phase {
		case "Ready":
			// keep Ready
		case "":
			status = "Pending"
		default:
			status = phase
		}
	}
	// An operator rejection is terminal even if an older backing Deployment
	// still has ready replicas from a previous spec.
	if strings.EqualFold(strings.TrimSpace(mcpServer.Status.Phase), "Error") {
		status = "Error"
	}
	return ServerInfo{
		Name:               mcpServer.Name,
		Namespace:          mcpServer.Namespace,
		UID:                string(mcpServer.UID),
		TeamID:             strings.TrimSpace(mcpServer.Spec.TeamID),
		Image:              strings.TrimSpace(mcpServer.Spec.Image),
		ImageTag:           strings.TrimSpace(mcpServer.Spec.ImageTag),
		Description:        mcpServer.Spec.Description,
		Ready:              deploymentStatus.Ready,
		Status:             status,
		Message:            strings.TrimSpace(mcpServer.Status.Message),
		Conditions:         append([]metav1.Condition(nil), mcpServer.Status.Conditions...),
		Labels:             mcpServer.Labels,
		Age:                mcpServer.CreationTimestamp.Format("2006-01-02T15:04:05Z"),
		Endpoint:           PublicMCPEndpoint(mcpServer),
		AuthMode:           headerAuthMode(mcpServer.Spec.Auth),
		AuthHeaders:        headerAuthNames(mcpServer.Spec.Auth),
		CredentialPresence: headerAuthPresence(mcpServer.Spec.Auth),
		GatewayEnabled:     mcpv1alpha1.GatewayIsEnabled(mcpServer.Spec.Gateway),
		ServicePort:        mcpServer.Spec.ServicePort,
		Generation:         mcpServer.Generation,
		Tools:              mcpServer.Spec.Tools,
		Prompts:            inventoryItemsOrEmpty(mcpServer.Spec.Prompts),
		Resources:          inventoryItemsOrEmpty(mcpServer.Spec.MCPResources),
		Tasks:              inventoryItemsOrEmpty(mcpServer.Spec.Tasks),
	}
}

func headerAuthMode(auth *mcpv1alpha1.AuthConfig) string {
	if !mcpv1alpha1.AuthUsesHeaderMode(auth) {
		return ""
	}
	return "header"
}

func headerAuthNames(auth *mcpv1alpha1.AuthConfig) []string {
	if !mcpv1alpha1.AuthUsesHeaderMode(auth) {
		return nil
	}
	names := make([]string, 0, len(auth.Headers))
	seen := make(map[string]struct{}, len(auth.Headers))
	for _, name := range auth.Headers {
		name = strings.TrimSpace(name)
		if credentialheaders.ValidateName(name) != nil {
			continue
		}
		key := strings.ToLower(name)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	return names
}

func headerAuthPresence(auth *mcpv1alpha1.AuthConfig) string {
	if !mcpv1alpha1.AuthUsesHeaderMode(auth) {
		return ""
	}
	presence, err := credentialheaders.NormalizePresence(auth.CredentialPresence)
	if err != nil {
		return ""
	}
	return presence
}

func inventoryItemsOrEmpty(items []mcpv1alpha1.InventoryItem) []mcpv1alpha1.InventoryItem {
	if len(items) == 0 {
		return []mcpv1alpha1.InventoryItem{}
	}
	return items
}

// PublicMCPEndpoint returns the public MCP endpoint path or URL for a server.
// The operator publishes the canonical URL in status.url, derived with the
// same scheme, host, and path rules the ingress and gateway use; that value
// wins. The fallback covers servers the operator has not reconciled yet.
func PublicMCPEndpoint(mcpServer mcpv1alpha1.MCPServer) string {
	if published := strings.TrimSpace(mcpServer.Status.URL); published != "" {
		return published
	}
	path := strings.TrimSpace(mcpServer.EffectivePublicPath())
	if path == "" {
		path = "/" + mcpServer.Name + "/mcp"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	host := strings.TrimSpace(mcpServer.Spec.IngressHost)
	if host == "" {
		host = metadata.ResolveMcpIngressHost()
	}
	if host == "" {
		return path
	}
	scheme := "https"
	if strings.HasPrefix(host, "localhost") || strings.HasPrefix(host, "127.0.0.1") {
		scheme = "http"
	}
	return scheme + "://" + strings.TrimRight(host, "/") + path
}
