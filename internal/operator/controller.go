package operator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/kubeworkload"
	"mcp-runtime/pkg/operatorutil"
)

type RegistryConfig struct {
	URL        string
	Username   string
	Password   string
	SecretName string
}

// MCPServerReconciler reconciles a MCPServer object
type MCPServerReconciler struct {
	client.Client
	Scheme *runtime.Scheme

	// APIReader is an uncached reader for objects the operator does not watch
	// (the server's pods). Nil falls back to Client.
	APIReader client.Reader

	// DefaultIngressHost is the default ingress host if not specified in the CR.
	DefaultIngressHost string

	// DefaultIngressEntryPoints is the default Traefik entrypoint annotation for MCP server ingresses.
	DefaultIngressEntryPoints string

	// DefaultIngressTLS enables Traefik TLS routing for MCP server ingresses by default.
	DefaultIngressTLS bool

	// DefaultIngressTLSSecret is the Kubernetes TLS Secret holding the
	// caller-facing (user->Traefik) host certificate for mtls servers. Path-based
	// mtls servers share one host, so this single platform host certificate is
	// published as Traefik's default certificate via a TLSStore named "default"
	// (see reconcileDefaultTLSStore) rather than a per-IngressRoute secretName.
	// Empty falls back to Traefik's built-in default certificate.
	DefaultIngressTLSSecret string

	// DefaultIngressTLSSecretNamespace is the Traefik-watched namespace that holds
	// the DefaultIngressTLSSecret and the single "default" TLSStore. Must be one
	// of the namespaces Traefik's kubernetescrd provider watches.
	DefaultIngressTLSSecretNamespace string

	// IngressReadinessMode controls how ingress readiness is evaluated.
	IngressReadinessMode string

	// ProvisionedRegistry holds the provisioned registry configuration.
	// If nil or URL is empty, provisioned registry features are disabled.
	ProvisionedRegistry *RegistryConfig

	// GatewayProxyImage is the default image used for the optional MCP gateway sidecar.
	GatewayProxyImage string

	// GatewayOTLPEndpoint is the OTLP/HTTP endpoint injected into MCP gateway sidecars.
	GatewayOTLPEndpoint string

	// DefaultAnalyticsIngestURL is the default analytics ingest endpoint used when analytics is enabled.
	DefaultAnalyticsIngestURL string

	// ClusterName is the cluster label attached to policy and audit events.
	ClusterName string

	// OAuthInternalIssuerURL is the in-cluster URL used by gateway sidecars for
	// OAuth metadata and JWKS discovery.
	OAuthInternalIssuerURL string

	// OAuthIssuerURL enables bundled authorization-server resource reconciliation
	// and supplies the default public issuer for OAuth MCPServers.
	OAuthIssuerURL string

	// MTLSClusterIssuer is the pre-existing cert-manager ClusterIssuer used for
	// gateway and adapter workload certificates. It is platform-wide; it does
	// not depend on MCP request authentication.
	MTLSClusterIssuer string

	// AdapterCertificatesEnabled opts OAuth servers into optional adapter
	// client certificates on their route (MCP_ADAPTER_CERTIFICATES).
	AdapterCertificatesEnabled bool

	// AdapterTrustDomain is the platform-wide SPIFFE trust domain used for
	// session-bound adapter certificates and the trusted Traefik identity.
	AdapterTrustDomain              string
	IngressControllerNamespace      string
	IngressControllerServiceAccount string
	IngressControllerPodLabels      map[string]string
}

// Use constants from constants.go
const (
	defaultRequestCPU    = DefaultRequestCPU
	defaultRequestMemory = DefaultRequestMemory
	defaultLimitCPU      = DefaultLimitCPU
	defaultLimitMemory   = DefaultLimitMemory
	defaultGatewayPort   = DefaultGatewayPort
)

const (
	gatewayPolicyVolumeName = "gateway-policy"
	gatewayPolicyMountDir   = "/var/run/mcp-runtime/policy"
	gatewayPolicyFileName   = "policy.json"
	gatewayPolicyFilePath   = gatewayPolicyMountDir + "/" + gatewayPolicyFileName
	// gatewayPolicyRevisionAnnotation is stamped on server pods when the
	// rendered policy changes, forcing the kubelet to refresh the policy volume.
	gatewayPolicyRevisionAnnotation = "mcpruntime.org/gateway-policy-revision"
	restrictedRunAsUser             = kubeworkload.RestrictedRunAsUser
	defaultWorkloadServiceAccount   = kubeworkload.DefaultServiceAccountName
)

// resourceReadiness tracks the readiness state of different resources.
type resourceReadiness = operatorutil.ResourceReadiness

//+kubebuilder:rbac:groups=mcpruntime.org,resources=mcpservers,verbs=get;list;watch;update;patch
//+kubebuilder:rbac:groups=mcpruntime.org,resources=mcpservers/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=mcpruntime.org,resources=mcpservers/finalizers,verbs=update
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;delete
//+kubebuilder:rbac:groups="",resources=pods,verbs=list;patch
//+kubebuilder:rbac:groups="",resources=serviceaccounts,verbs=get;list;watch;create;update;patch
//+kubebuilder:rbac:groups=core,resources=services,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingresses,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=networking.k8s.io,resources=ingressclasses,verbs=get;list;watch
//+kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups="",resources=events,verbs=create;patch;update
//+kubebuilder:rbac:groups=mcpruntime.org,resources=mcpaccessgrants,verbs=get;list;watch
//+kubebuilder:rbac:groups=mcpruntime.org,resources=mcpagentsessions,verbs=get;list;watch
//+kubebuilder:rbac:groups=cert-manager.io,resources=certificates,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=traefik.io,resources=ingressroutetcps;ingressroutes;middlewares;tlsoptions;tlsstores;serverstransports,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop
func (r *MCPServerReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	logger := log.FromContext(ctx)

	mcpServer, found, err := r.fetchMCPServer(ctx, req)
	if err != nil {
		return ctrl.Result{}, err
	}
	if !found {
		return ctrl.Result{}, nil
	}

	logger.Info("Reconciling MCPServer", "name", mcpServer.Name, "namespace", mcpServer.Namespace)

	mcpServer = r.defaultedMCPServerForReconcile(mcpServer)
	if err := r.validateMCPServerSpec(ctx, mcpServer, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.checkPublicRouteOwnership(ctx, mcpServer); err != nil {
		r.updateStatus(ctx, mcpServer, "Error", err.Error(), resourceReadiness{})
		logOperatorError(logger, err, "MCPServer public route conflict")
		// The owner's deletion does not enqueue this server, so check again.
		return ctrl.Result{RequeueAfter: time.Minute}, nil
	}
	if err := r.validateIngressConfig(ctx, mcpServer, logger); err != nil {
		return ctrl.Result{}, err
	}
	if err := r.validateGatewayConfig(ctx, mcpServer, logger); err != nil {
		return ctrl.Result{}, err
	}

	if err := r.reconcileResources(ctx, mcpServer, logger); err != nil {
		return ctrl.Result{}, err
	}

	readiness, err := r.checkResourceReadiness(ctx, mcpServer)
	if err != nil {
		return ctrl.Result{}, err
	}

	phase, allReady := determinePhase(readiness, mcpServer)
	r.updateStatus(ctx, mcpServer, phase, "All resources reconciled", readiness)

	logger.Info("Successfully reconciled MCPServer", "name", mcpServer.Name, "phase", phase)

	// If not all resources are ready, requeue with a short delay to check again
	if !allReady {
		return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
	}
	return ctrl.Result{}, nil
}

func (r *MCPServerReconciler) fetchMCPServer(ctx context.Context, req ctrl.Request) (*mcpv1alpha1.MCPServer, bool, error) {
	var mcpServer mcpv1alpha1.MCPServer
	if err := r.Get(ctx, req.NamespacedName, &mcpServer); err != nil {
		if errors.IsNotFound(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &mcpServer, true, nil
}

func (r *MCPServerReconciler) validateMCPServerSpec(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, logger logr.Logger) error {
	if _, err := mcpServer.ValidateCreate(); err != nil {
		r.updateStatus(ctx, mcpServer, "Error", err.Error(), resourceReadiness{})
		logOperatorError(logger, err, "Invalid MCPServer specification")
		return err
	}
	if err := mcpServer.ValidateResolvedAuth(); err != nil {
		r.updateStatus(ctx, mcpServer, "Error", err.Error(), resourceReadiness{})
		logOperatorError(logger, err, "Invalid MCPServer specification")
		return err
	}
	return nil
}

func (r *MCPServerReconciler) validateIngressConfig(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, logger logr.Logger) error {
	if strings.TrimSpace(mcpServer.Spec.PublicPathPrefix) == "" {
		if err := r.requireSpecField(ctx, mcpServer, logger, "ingress path", effectiveIngressPath(mcpServer),
			"ingressPath is required; set spec.ingressPath or ensure metadata.name is set"); err != nil {
			return err
		}
		if err := r.requireSpecField(ctx, mcpServer, logger, "ingress host", effectiveIngressHost(mcpServer),
			"ingressHost is required; set spec.ingressHost, set MCP_DEFAULT_INGRESS_HOST on the operator, or use spec.publicPathPrefix for hostless routing"); err != nil {
			return err
		}
	}
	return nil
}

func (r *MCPServerReconciler) validateGatewayConfig(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, logger logr.Logger) error {
	if gatewayEnabled(mcpServer) {
		if _, err := r.resolveGatewayImage(mcpServer); err != nil {
			r.updateStatus(ctx, mcpServer, "Error", err.Error(), resourceReadiness{})
			logOperatorError(logger, err, "Missing gateway image")
			return err
		}
	}

	// Only surface a missing-URL error when the user explicitly attached an
	// AnalyticsConfig (and did not opt out via Disabled) while neither the
	// server spec nor the operator fallback provides an ingest URL. With the
	// default-on behavior, a missing URL elsewhere means "no analytics
	// available" — a non-error state.
	if mcpServer.Spec.Analytics != nil && !mcpServer.Spec.Analytics.Disabled {
		specURL := strings.TrimSpace(mcpServer.Spec.Analytics.IngestURL)
		opURL := strings.TrimSpace(r.DefaultAnalyticsIngestURL)
		if specURL == "" && opURL == "" {
			if err := r.requireSpecField(ctx, mcpServer, logger, "analytics ingest URL", "",
				"analytics.ingestURL is required when spec.analytics is set; set spec.analytics.ingestURL, configure MCP_SENTINEL_INGEST_URL on the operator, or set spec.analytics.disabled to true"); err != nil {
				return err
			}
		}
	}

	return nil
}

func (r *MCPServerReconciler) requireSpecField(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, logger logr.Logger, field, value, message string) error {
	if value != "" {
		return nil
	}
	contextMap := map[string]any{
		"mcpServer": mcpServer.Name,
		"namespace": mcpServer.Namespace,
		"field":     field,
	}
	err := newOperatorError(message, contextMap)
	r.updateStatus(ctx, mcpServer, "Error", err.Error(), resourceReadiness{})
	logOperatorError(logger, err, "Missing "+field)
	return err
}

func (r *MCPServerReconciler) reconcileResources(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer, logger logr.Logger) error {
	contextMap := map[string]any{
		"mcpServer": mcpServer.Name,
		"namespace": mcpServer.Namespace,
	}

	if err := r.reconcilePolicyConfigMap(ctx, mcpServer); err != nil {
		contextMap["resource"] = "configmap"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile policy ConfigMap", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile policy ConfigMap")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile policy ConfigMap: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileGatewayCertificate(ctx, mcpServer); err != nil {
		contextMap["resource"] = "certificate"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile gateway Certificate", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile gateway Certificate")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile gateway Certificate: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileTraefikClientCertificate(ctx, mcpServer); err != nil {
		contextMap["resource"] = "traefik-client-certificate"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile Traefik client Certificate", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile Traefik client Certificate")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile Traefik client Certificate: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileDeployment(ctx, mcpServer); err != nil {
		contextMap["resource"] = "deployment"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile Deployment", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile Deployment")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile Deployment: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileCanaryDeployment(ctx, mcpServer); err != nil {
		contextMap["resource"] = "canary-deployment"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile canary Deployment", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile canary Deployment")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile canary Deployment: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileService(ctx, mcpServer); err != nil {
		contextMap["resource"] = "service"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile Service", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile Service")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile Service: %v", err), resourceReadiness{})
		return wrappedErr
	}
	// Trust bundle must exist before Traefik ServersTransport/IngressRoute so
	// Traefik never loads a transport that references a missing CA secret.
	if err := r.reconcileMTLSTrustBundle(ctx, mcpServer); err != nil {
		contextMap["resource"] = "trust-bundle"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile mTLS trust bundle", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile mTLS trust bundle")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile mTLS trust bundle: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileIngress(ctx, mcpServer); err != nil {
		contextMap["resource"] = "ingress"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile Ingress", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile Ingress")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile Ingress: %v", err), resourceReadiness{})
		return wrappedErr
	}
	if err := r.reconcileMTLSNetworkPolicy(ctx, mcpServer); err != nil {
		contextMap["resource"] = "networkpolicy"
		wrappedErr := wrapOperatorError(err, "Failed to reconcile mTLS NetworkPolicy", contextMap)
		logOperatorError(logger, wrappedErr, "Failed to reconcile mTLS NetworkPolicy")
		r.updateStatus(ctx, mcpServer, "Error", fmt.Sprintf("Failed to reconcile mTLS NetworkPolicy: %v", err), resourceReadiness{})
		return wrappedErr
	}
	return nil
}

func (r *MCPServerReconciler) checkResourceReadiness(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (resourceReadiness, error) {
	deploymentReady, err := r.checkDeploymentReady(ctx, mcpServer)
	if err != nil {
		return resourceReadiness{}, err
	}
	serviceReady, err := r.checkServiceReady(ctx, mcpServer)
	if err != nil {
		return resourceReadiness{}, err
	}
	ingressReady, err := r.checkIngressReady(ctx, mcpServer)
	if err != nil {
		return resourceReadiness{}, err
	}
	policyReady, err := r.checkPolicyConfigMapReady(ctx, mcpServer)
	if err != nil {
		return resourceReadiness{}, err
	}
	canaryReady, err := r.checkCanaryDeploymentReady(ctx, mcpServer)
	if err != nil {
		return resourceReadiness{}, err
	}

	gatewayReady := false
	if gatewayEnabled(mcpServer) {
		gatewayReady = deploymentReady
	}
	if !canaryEnabled(mcpServer) {
		canaryReady = false
	}

	return resourceReadiness{
		Deployment: deploymentReady,
		Service:    serviceReady,
		Ingress:    ingressReady,
		Gateway:    gatewayReady,
		Policy:     policyReady,
		Canary:     canaryReady,
	}, nil
}

func determinePhase(readiness resourceReadiness, mcpServer *mcpv1alpha1.MCPServer) (string, bool) {
	allReady := readiness.Deployment && readiness.Service && readiness.Ingress
	if gatewayEnabled(mcpServer) {
		allReady = allReady && readiness.Gateway && readiness.Policy
	}
	if canaryEnabled(mcpServer) {
		allReady = allReady && readiness.Canary
	}
	if allReady {
		return "Ready", true
	}
	if readiness.Deployment || readiness.Service || readiness.Ingress || readiness.Gateway || readiness.Policy || readiness.Canary {
		return "PartiallyReady", false
	}
	return "Pending", false
}

func (r *MCPServerReconciler) defaultedMCPServerForReconcile(mcpServer *mcpv1alpha1.MCPServer) *mcpv1alpha1.MCPServer {
	defaulted := mcpServer.DeepCopy()
	options := mcpv1alpha1.MCPServerDefaultOptions{
		DefaultIngressHost:        r.DefaultIngressHost,
		DefaultIngressTLS:         r.DefaultIngressTLS,
		DefaultAnalyticsIngestURL: r.DefaultAnalyticsIngestURL,
		DefaultOAuthIssuerURL:     r.OAuthIssuerURL,
	}
	defaulted.DefaultWithOptions(options)
	defaulted.ResolveDerivedAuth(options)
	return defaulted
}

func (r *MCPServerReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if err := r.setupBundledOAuthResourcesController(mgr); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&mcpv1alpha1.MCPServer{}).
		Owns(&appsv1.Deployment{}).
		Owns(&corev1.ConfigMap{}).
		Owns(&corev1.Service{}).
		Owns(&networkingv1.Ingress{}).
		Watches(&mcpv1alpha1.MCPAccessGrant{}, handler.EnqueueRequestsFromMapFunc(r.requestsForReferencedServer)).
		Watches(&mcpv1alpha1.MCPAgentSession{}, handler.EnqueueRequestsFromMapFunc(r.requestsForReferencedServer)).
		Complete(r)
}

func (r *MCPServerReconciler) requestsForReferencedServer(_ context.Context, obj client.Object) []ctrl.Request {
	switch resource := obj.(type) {
	case *mcpv1alpha1.MCPAccessGrant:
		namespace := resource.Spec.ServerRef.Namespace
		if namespace == "" {
			namespace = resource.Namespace
		}
		if resource.Spec.ServerRef.Name == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: resource.Spec.ServerRef.Name, Namespace: namespace}}}
	case *mcpv1alpha1.MCPAgentSession:
		namespace := resource.Spec.ServerRef.Namespace
		if namespace == "" {
			namespace = resource.Namespace
		}
		if resource.Spec.ServerRef.Name == "" {
			return nil
		}
		return []ctrl.Request{{NamespacedName: types.NamespacedName{Name: resource.Spec.ServerRef.Name, Namespace: namespace}}}
	default:
		return nil
	}
}
