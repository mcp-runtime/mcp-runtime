package operator

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/intstr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/oauthresource"
)

const (
	defaultIngressControllerNamespace      = "traefik"
	defaultIngressControllerServiceAccount = "traefik"
	// operatorFieldManager is the Server-Side Apply field owner for operator-
	// managed cert-manager and Traefik resources.
	operatorFieldManager = "mcp-runtime-operator"
)

const (
	gatewayTLSVolumeName = "gateway-mtls"
	gatewayTLSMountDir   = "/var/run/mcp-runtime/tls"
)

var certificateGVK = schema.GroupVersionKind{
	Group:   "cert-manager.io",
	Version: "v1",
	Kind:    "Certificate",
}

var ingressRouteTCPGVK = schema.GroupVersionKind{
	Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRouteTCP",
}

var (
	ingressRouteGVK     = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "IngressRoute"}
	middlewareGVK       = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "Middleware"}
	tlsOptionGVK        = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "TLSOption"}
	serversTransportGVK = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "ServersTransport"}
	tlsStoreGVK         = schema.GroupVersionKind{Group: "traefik.io", Version: "v1alpha1", Kind: "TLSStore"}
)

// spiffeIdentityPluginName must match the local plugin module name registered in
// Traefik's static configuration (experimental.localPlugins).
const spiffeIdentityPluginName = "spiffe-identity"

// verifiedSPIFFEHeader is the header the spiffe-identity middleware injects and
// the gateway reads. It must match the gateway's defaultVerifiedSPIFFEHeader.
const verifiedSPIFFEHeader = "X-MCP-Verified-SPIFFE-ID"

// usesAdapterCertificates enables optional adapter client-certificate
// validation on a gateway-enabled server route. It moves the route from a plain
// Ingress to a Traefik IngressRoute and puts the gateway behind an mTLS hop,
// so it must be switched on explicitly (MCP_ADAPTER_CERTIFICATES) rather than
// implied by workload PKI being present, and it only applies to servers that
// route through Traefik and have a gateway to validate the hop.
func (r *MCPServerReconciler) usesAdapterCertificates(mcpServer *mcpv1alpha1.MCPServer) bool {
	if !r.AdapterCertificatesEnabled || !gatewayEnabled(mcpServer) {
		return false
	}
	ingressClass := strings.TrimSpace(mcpServer.Spec.IngressClass)
	if ingressClass != "" && ingressClass != DefaultIngressClass {
		return false
	}
	return strings.TrimSpace(r.AdapterTrustDomain) != "" && strings.TrimSpace(r.MTLSClusterIssuer) != ""
}

func gatewayTLSSecretName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-gateway-mtls"
}

func traefikClientCertSecretName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-traefik-client-mtls"
}

func mtlsTrustBundleSecretName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-mtls-ca"
}

// mtlsBackendSecretsReady reports whether Traefik can load the re-encrypt hop
// Secrets referenced by ServersTransport (client cert + trust CA).
func (r *MCPServerReconciler) mtlsBackendSecretsReady(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) (bool, error) {
	var clientSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: traefikClientCertSecretName(mcpServer), Namespace: mcpServer.Namespace}, &clientSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(clientSecret.Data["tls.crt"]) == 0 || len(clientSecret.Data["tls.key"]) == 0 {
		return false, nil
	}
	var caSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: mtlsTrustBundleSecretName(mcpServer), Namespace: mcpServer.Namespace}, &caSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}
	if len(caSecret.Data["tls.ca"]) == 0 && len(caSecret.Data["ca.crt"]) == 0 {
		return false, nil
	}
	return true, nil
}

// traefikProxySPIFFEID is the identity the ingress presents to the gateway over
// the re-encrypted hop. The gateway pins this via TRUSTED_PROXY_SPIFFE_ID so a
// non-ingress holder of an identity-CA cert cannot impersonate the ingress.
func (r *MCPServerReconciler) traefikProxySPIFFEID(mcpServer *mcpv1alpha1.MCPServer) string {
	trustDomain := strings.TrimSpace(r.AdapterTrustDomain)
	if trustDomain == "" {
		return ""
	}
	return fmt.Sprintf("spiffe://%s/ns/%s/sa/%s", trustDomain, r.ingressControllerNamespace(), r.ingressControllerServiceAccount())
}

func (r *MCPServerReconciler) ingressControllerNamespace() string {
	if namespace := strings.TrimSpace(r.IngressControllerNamespace); namespace != "" {
		return namespace
	}
	return defaultIngressControllerNamespace
}

func (r *MCPServerReconciler) ingressControllerServiceAccount() string {
	if account := strings.TrimSpace(r.IngressControllerServiceAccount); account != "" {
		return account
	}
	return defaultIngressControllerServiceAccount
}

func (r *MCPServerReconciler) ingressControllerPodLabels() map[string]string {
	if len(r.IngressControllerPodLabels) > 0 {
		return r.IngressControllerPodLabels
	}
	return map[string]string{"app": "traefik"}
}

func (r *MCPServerReconciler) reconcileGatewayCertificate(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	certificate := &unstructured.Unstructured{}
	certificate.SetGroupVersionKind(certificateGVK)
	certificate.SetName(gatewayTLSSecretName(mcpServer))
	certificate.SetNamespace(mcpServer.Namespace)

	if !r.usesAdapterCertificates(mcpServer) {
		if err := r.Delete(ctx, certificate); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
		if err := r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: gatewayTLSSecretName(mcpServer), Namespace: mcpServer.Namespace}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	issuer := strings.TrimSpace(r.MTLSClusterIssuer)
	if issuer == "" {
		return fmt.Errorf("adapter certificate validation requires MCP_MTLS_CLUSTER_ISSUER on the operator")
	}

	dnsNames := []any{
		mcpServer.Name,
		mcpServer.Name + "." + mcpServer.Namespace,
		mcpServer.Name + "." + mcpServer.Namespace + ".svc",
		mcpServer.Name + "." + mcpServer.Namespace + ".svc.cluster.local",
	}
	if host := effectiveIngressHost(mcpServer); host != "" {
		dnsNames = append(dnsNames, host)
	}
	certificate.Object["spec"] = map[string]any{
		"secretName":  gatewayTLSSecretName(mcpServer),
		"duration":    "24h",
		"renewBefore": "8h",
		"dnsNames":    dnsNames,
		"usages":      []any{"digital signature", "key encipherment", "server auth"},
		"issuerRef": map[string]any{
			"group": "cert-manager.io",
			"kind":  "ClusterIssuer",
			"name":  issuer,
		},
	}
	certificate.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "mcp-runtime",
		"mcpruntime.org/server":        mcpServer.Name,
	})
	certificate.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(mcpServer, mcpv1alpha1.GroupVersion.WithKind("MCPServer"))})

	return r.applyUnstructured(ctx, certificate)
}

// applyUnstructured idempotently reconciles an unstructured object (cert-manager
// or Traefik CRD) via Server-Side Apply. SSA is used instead of a read +
// reflect.DeepEqual + update because those CRDs have fields defaulted by their
// own controllers/webhooks (e.g. cert-manager populates privateKey settings);
// a structural diff against our desired spec would never match, producing an
// update on every reconcile (a hot loop). With SSA the operator owns only the
// fields it sets, so defaulted fields are left untouched and reconciles are
// no-ops once converged. The object must carry its GVK, name, and namespace.
func (r *MCPServerReconciler) applyUnstructured(ctx context.Context, obj *unstructured.Unstructured) error {
	force := true
	return r.Apply(ctx, client.ApplyConfigurationFromUnstructured(obj),
		&client.ApplyOptions{FieldManager: operatorFieldManager, Force: &force})
}

// deleteUnstructured removes an object by GVK/name/namespace, tolerating absent
// objects and missing CRDs (so cleanup works even when Traefik CRDs are absent).
func (r *MCPServerReconciler) deleteUnstructured(ctx context.Context, gvk schema.GroupVersionKind, name, namespace string) error {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	obj.SetNamespace(namespace)
	if err := r.Delete(ctx, obj); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
		return err
	}
	return nil
}

// reconcileTraefikClientCertificate issues the client certificate the ingress
// presents to the gateway over the re-encrypted hop. It is signed by the same
// identity CA as user and gateway certificates and carries the pinned ingress
// SPIFFE identity (see traefikProxySPIFFEID).
func (r *MCPServerReconciler) reconcileTraefikClientCertificate(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	cert := &unstructured.Unstructured{}
	cert.SetGroupVersionKind(certificateGVK)
	cert.SetName(traefikClientCertSecretName(mcpServer))
	cert.SetNamespace(mcpServer.Namespace)
	if !r.usesAdapterCertificates(mcpServer) {
		if err := r.Delete(ctx, cert); err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err) {
			return err
		}
		if err := r.Delete(ctx, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: traefikClientCertSecretName(mcpServer), Namespace: mcpServer.Namespace}}); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}
	issuer := strings.TrimSpace(r.MTLSClusterIssuer)
	if issuer == "" {
		return fmt.Errorf("adapter certificate validation requires MCP_MTLS_CLUSTER_ISSUER on the operator")
	}
	spiffeID := r.traefikProxySPIFFEID(mcpServer)
	if spiffeID == "" {
		return fmt.Errorf("adapter certificate validation requires MCP_TRUST_DOMAIN on the operator")
	}
	cert.Object["spec"] = map[string]any{
		"secretName":  traefikClientCertSecretName(mcpServer),
		"duration":    "24h",
		"renewBefore": "8h",
		"uris":        []any{spiffeID},
		"usages":      []any{"digital signature", "key encipherment", "client auth"},
		"issuerRef": map[string]any{
			"group": "cert-manager.io",
			"kind":  "ClusterIssuer",
			"name":  issuer,
		},
	}
	cert.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "mcp-runtime",
		"mcpruntime.org/server":        mcpServer.Name,
	})
	cert.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(mcpServer, mcpv1alpha1.GroupVersion.WithKind("MCPServer"))})
	return r.applyUnstructured(ctx, cert)
}

// reconcileMTLSTrustBundle materializes the identity CA bundle as a Secret in
// the server namespace, keyed for both Traefik (tls.ca) and cert-manager-style
// (ca.crt) consumers, so the TLSOption (verify client certs) and the
// ServersTransport (verify the gateway) can reference it.
//
// The CA is sourced from the gateway certificate's ca.crt, which is
// issuer-agnostic: cert-manager populates ca.crt regardless of which
// ClusterIssuer signed the certificate. When the gateway certificate has not
// been issued yet, the bundle is skipped and a later reconcile (driven by
// Deployment/ingress readiness waiting on the gateway TLS Secret) materializes it.
func (r *MCPServerReconciler) reconcileMTLSTrustBundle(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mtlsTrustBundleSecretName(mcpServer),
			Namespace: mcpServer.Namespace,
		},
	}
	if !r.usesAdapterCertificates(mcpServer) {
		if err := r.Delete(ctx, secret); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	var gwSecret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: gatewayTLSSecretName(mcpServer), Namespace: mcpServer.Namespace}, &gwSecret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // gateway certificate not issued yet; retry on a later reconcile
		}
		return err
	}
	ca := gwSecret.Data["ca.crt"]
	if len(ca) == 0 {
		// The secret exists but cert-manager has not populated the CA yet. Return
		// an error so the controller requeues rather than leaving the trust bundle
		// permanently unwritten (the deployment could otherwise become ready and
		// stop reconciliation in an incomplete state).
		return fmt.Errorf("gateway secret %q has no ca.crt yet", gatewayTLSSecretName(mcpServer))
	}

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"tls.ca": ca, "ca.crt": ca}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels["app.kubernetes.io/managed-by"] = "mcp-runtime"
		secret.Labels["mcpruntime.org/server"] = mcpServer.Name
		return ctrl.SetControllerReference(mcpServer, secret, r.Scheme)
	})
	if err != nil {
		return err
	}
	return r.reconcilePlatformClientAuthCA(ctx, ca)
}

func mtlsNetworkPolicyName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-mtls-gateway"
}

// reconcileMTLSNetworkPolicy restricts ingress to the gateway port so the
// verified-identity header can only originate from the TLS-terminating ingress.
// Without it, any pod could connect directly to the gateway and forge the
// header. It is defense-in-depth on top of the gateway's own requirement that
// the connection be a verified mTLS hop (see authenticateMTLS). The gateway
// metrics port stays open so Prometheus scraping is unaffected.
func (r *MCPServerReconciler) reconcileMTLSNetworkPolicy(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	policy := &networkingv1.NetworkPolicy{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mtlsNetworkPolicyName(mcpServer),
			Namespace: mcpServer.Namespace,
		},
	}
	if !r.usesAdapterCertificates(mcpServer) {
		if err := r.Delete(ctx, policy); err != nil && !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	}

	gatewayPort := mcpServer.Spec.Gateway.Port
	metricsPort := int32(DefaultGatewayMetricsPort)
	tcp := corev1.ProtocolTCP

	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, policy, func() error {
		gatewayTarget := intstr.FromInt32(gatewayPort)
		metricsTarget := intstr.FromInt32(metricsPort)
		policy.Spec = networkingv1.NetworkPolicySpec{
			PodSelector: metav1.LabelSelector{
				MatchLabels: map[string]string{"app": mcpServer.Name},
			},
			PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress},
			Ingress: []networkingv1.NetworkPolicyIngressRule{
				{
					// Only the ingress controller may reach the gateway port.
					From: []networkingv1.NetworkPolicyPeer{{
						NamespaceSelector: &metav1.LabelSelector{
							MatchLabels: map[string]string{"kubernetes.io/metadata.name": r.ingressControllerNamespace()},
						},
						PodSelector: &metav1.LabelSelector{
							MatchLabels: r.ingressControllerPodLabels(),
						},
					}},
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &gatewayTarget}},
				},
				{
					// Metrics scraping is allowed from any source.
					Ports: []networkingv1.NetworkPolicyPort{{Protocol: &tcp, Port: &metricsTarget}},
				},
			},
		}
		return ctrl.SetControllerReference(mcpServer, policy, r.Scheme)
	})
	return err
}

func mtlsMiddlewareName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-spiffe-identity"
}

func mtlsTLSOptionName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-mtls"
}

func mtlsServersTransportName(mcpServer *mcpv1alpha1.MCPServer) string {
	return mcpServer.Name + "-gateway"
}

// gatewayServerName is the SNI/verification name Traefik uses for the gateway
// over the re-encrypted hop. It matches a dnsName on the gateway certificate.
func gatewayServerName(mcpServer *mcpv1alpha1.MCPServer) string {
	return fmt.Sprintf("%s.%s.svc.cluster.local", mcpServer.Name, mcpServer.Namespace)
}

// deleteMTLSIngress removes every Traefik resource the mtls path creates,
// including the legacy passthrough IngressRouteTCP from the previous model.
func (r *MCPServerReconciler) deleteMTLSIngress(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	ns := mcpServer.Namespace
	for _, target := range []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{ingressRouteGVK, mcpServer.Name},
		{middlewareGVK, mtlsMiddlewareName(mcpServer)},
		{tlsOptionGVK, mtlsTLSOptionName(mcpServer)},
		{serversTransportGVK, mtlsServersTransportName(mcpServer)},
		{ingressRouteTCPGVK, mcpServer.Name}, // legacy passthrough route
	} {
		if err := r.deleteUnstructured(ctx, target.gvk, target.name, ns); err != nil {
			return err
		}
	}
	return nil
}

// reconcileMTLSIngress generates the Traefik resources for gateway routes with
// optional adapter client certificates. Traefik validates a certificate when
// presented. The middleware forwards only the verified adapter SPIFFE identity
// for certificate-authenticated requests.
func (r *MCPServerReconciler) reconcileMTLSIngress(ctx context.Context, mcpServer *mcpv1alpha1.MCPServer) error {
	// Drop the legacy passthrough route from the previous (gateway-terminates) model.
	if err := r.deleteUnstructured(ctx, ingressRouteTCPGVK, mcpServer.Name, mcpServer.Namespace); err != nil {
		return err
	}

	// Do not publish ServersTransport/IngressRoute until Traefik can load the
	// referenced Secrets. Creating them early makes Traefik return 502 until
	// cert-manager finishes issuing. Skip create/update only — never delete an
	// existing route here, or cert rotation / brief Secret gaps would take a
	// live server offline.
	secretsReady, err := r.mtlsBackendSecretsReady(ctx, mcpServer)
	if err != nil {
		return err
	}
	if !secretsReady {
		return nil
	}

	clientAuthType := "VerifyClientCertIfGiven"
	tlsOption := r.traefikResource(mcpServer, tlsOptionGVK, mtlsTLSOptionName(mcpServer), map[string]any{
		"minVersion": "VersionTLS12",
		"clientAuth": map[string]any{
			"secretNames":    []any{mtlsTrustBundleSecretName(mcpServer)},
			"clientAuthType": clientAuthType,
		},
	})
	middleware := r.traefikResource(mcpServer, middlewareGVK, mtlsMiddlewareName(mcpServer), map[string]any{
		"plugin": map[string]any{
			spiffeIdentityPluginName: map[string]any{
				"verifiedHeader":           verifiedSPIFFEHeader,
				"trustDomain":              strings.TrimSpace(r.AdapterTrustDomain),
				"rejectInvalidCertificate": true,
			},
		},
	})
	serversTransport := r.traefikResource(mcpServer, serversTransportGVK, mtlsServersTransportName(mcpServer), map[string]any{
		"serverName":          gatewayServerName(mcpServer),
		"certificatesSecrets": []any{traefikClientCertSecretName(mcpServer)},
		"rootCAsSecrets":      []any{mtlsTrustBundleSecretName(mcpServer)},
	})

	match := fmt.Sprintf("PathPrefix(`%s`)", effectiveIngressPath(mcpServer))
	if host := effectiveIngressHost(mcpServer); host != "" {
		match = fmt.Sprintf("Host(`%s`) && %s", host, match)
	}
	// tls.options enforces client-cert verification (the mTLS half). The
	// caller-facing server certificate comes from the cluster-wide default
	// (a TLSStore named "default"; see reconcileDefaultTLSStore), never a
	// per-IngressRoute secretName — Traefik resolves secretName only in the
	// IngressRoute's own (tenant) namespace, where the shared platform host
	// certificate does not exist.
	service := map[string]any{
		"name":             mcpServer.Name,
		"port":             int64(mcpServer.Spec.ServicePort),
		"scheme":           "https",
		"serversTransport": mtlsServersTransportName(mcpServer),
	}
	route := map[string]any{
		"match":       match,
		"kind":        "Rule",
		"middlewares": []any{map[string]any{"name": mtlsMiddlewareName(mcpServer)}},
		"services":    []any{service},
	}
	routes := []any{route}
	if serverUsesOAuth(mcpServer) {
		metadataMatch := fmt.Sprintf("Path(`%s`)", oauthresource.ProtectedResourceMetadataPath(effectiveIngressPath(mcpServer)))
		if host := effectiveIngressHost(mcpServer); host != "" {
			metadataMatch = fmt.Sprintf("Host(`%s`) && %s", host, metadataMatch)
		}
		routes = append(routes, map[string]any{
			"match":       metadataMatch,
			"kind":        "Rule",
			"middlewares": []any{map[string]any{"name": mtlsMiddlewareName(mcpServer)}},
			"services":    []any{service},
		})
	}
	// Path-based servers share one host, and Traefik falls back to its default
	// TLS options when routers on the same host name different ones, so
	// per-server options would never ask for a client certificate. With a
	// platform TLS namespace every route uses the single platform "default"
	// TLSOption (see reconcileDefaultClientAuthTLSOption); the per-server
	// option remains only as a fallback when no platform namespace is set.
	tls := map[string]any{}
	objects := []*unstructured.Unstructured{middleware, serversTransport}
	if r.platformClientAuthNamespace() == "" {
		tls["options"] = map[string]any{"name": mtlsTLSOptionName(mcpServer)}
		objects = append(objects, tlsOption)
	} else if err := r.deleteUnstructured(ctx, tlsOptionGVK, mtlsTLSOptionName(mcpServer), mcpServer.Namespace); err != nil {
		return err
	}
	ingressRoute := r.traefikResource(mcpServer, ingressRouteGVK, mcpServer.Name, map[string]any{
		"entryPoints": r.adapterCertificateEntryPoints(),
		"routes":      routes,
		"tls":         tls,
	})
	objects = append(objects, ingressRoute)

	if err := r.reconcileDefaultTLSStore(ctx); err != nil {
		return err
	}
	if err := r.reconcileDefaultClientAuthTLSOption(ctx); err != nil {
		return err
	}
	for _, obj := range objects {
		if err := r.applyUnstructured(ctx, obj); err != nil {
			return err
		}
	}
	return nil
}

// platformClientAuthCASecret holds the workload CA in the platform TLS
// namespace. Every gateway certificate is issued by MTLSClusterIssuer, so any
// server's CA is the platform's.
// #nosec G101 -- This is a Kubernetes Secret resource name, not a credential.
const platformClientAuthCASecret = "mcp-adapter-client-ca"

func (r *MCPServerReconciler) platformClientAuthNamespace() string {
	return strings.TrimSpace(r.DefaultIngressTLSSecretNamespace)
}

// reconcilePlatformClientAuthCA copies the workload CA into the platform TLS
// namespace for the default TLSOption. It carries no owner reference: it is
// shared by every adapter-certificate server.
func (r *MCPServerReconciler) reconcilePlatformClientAuthCA(ctx context.Context, ca []byte) error {
	namespace := r.platformClientAuthNamespace()
	if namespace == "" || len(ca) == 0 {
		return nil
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: platformClientAuthCASecret, Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"tls.ca": ca, "ca.crt": ca}
		if secret.Labels == nil {
			secret.Labels = map[string]string{}
		}
		secret.Labels["app.kubernetes.io/managed-by"] = "mcp-runtime"
		return nil
	})
	return err
}

// reconcileDefaultClientAuthTLSOption makes Traefik's global default TLS
// options request (never require) a client certificate verified against the
// workload CA, so adapter certificates are seen on every shared host while
// clients without one are unaffected. A "default" TLSOption the operator did
// not create is left alone and reported, since it is the cluster's own TLS
// policy.
func (r *MCPServerReconciler) reconcileDefaultClientAuthTLSOption(ctx context.Context) error {
	namespace := r.platformClientAuthNamespace()
	if namespace == "" {
		return nil
	}
	var ca corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Name: platformClientAuthCASecret, Namespace: namespace}, &ca); err != nil {
		if apierrors.IsNotFound(err) {
			return nil // the CA is copied once a gateway certificate is issued
		}
		return err
	}
	existing := &unstructured.Unstructured{}
	existing.SetGroupVersionKind(tlsOptionGVK)
	err := r.Get(ctx, types.NamespacedName{Name: "default", Namespace: namespace}, existing)
	switch {
	case err == nil && existing.GetLabels()["app.kubernetes.io/managed-by"] != "mcp-runtime":
		return fmt.Errorf("TLSOption %s/default exists and is not managed by mcp-runtime; add clientAuth (VerifyClientCertIfGiven, secret %s) to it or remove it to enable adapter certificates", namespace, platformClientAuthCASecret)
	case err != nil && !apierrors.IsNotFound(err) && !meta.IsNoMatchError(err):
		return err
	}
	option := &unstructured.Unstructured{}
	option.SetGroupVersionKind(tlsOptionGVK)
	option.SetName("default")
	option.SetNamespace(namespace)
	option.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"})
	option.Object["spec"] = map[string]any{
		"minVersion": "VersionTLS12",
		"clientAuth": map[string]any{
			"secretNames":    []any{platformClientAuthCASecret},
			"clientAuthType": "VerifyClientCertIfGiven",
		},
	}
	return r.applyUnstructured(ctx, option)
}

// reconcileDefaultTLSStore provisions the cluster-wide caller-facing server
// certificate for mtls hosts as Traefik's default certificate.
//
// Traefik supports exactly one TLSStore named "default", and it (with its
// referenced secret) must live in a namespace Traefik watches. The operator
// therefore converges every mtls server on a SINGLE TLSStore in the configured
// DefaultIngressTLSSecretNamespace — never one per tenant namespace, which would
// both create conflicting "default" stores and reference a host secret that does
// not exist there. It carries no owner reference (it is a shared platform object
// not owned by any one MCPServer); Server-Side Apply keeps concurrent reconciles
// idempotent. A no-op when the host secret/namespace is not configured.
func (r *MCPServerReconciler) reconcileDefaultTLSStore(ctx context.Context) error {
	secret := strings.TrimSpace(r.DefaultIngressTLSSecret)
	namespace := strings.TrimSpace(r.DefaultIngressTLSSecretNamespace)
	if secret == "" || namespace == "" {
		return nil
	}
	store := &unstructured.Unstructured{}
	store.SetGroupVersionKind(tlsStoreGVK)
	store.SetName("default")
	store.SetNamespace(namespace)
	store.Object["spec"] = map[string]any{
		"defaultCertificate": map[string]any{"secretName": secret},
	}
	store.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime"})
	return r.applyUnstructured(ctx, store)
}

// traefikResource builds an owned, labelled unstructured Traefik CR.
func (r *MCPServerReconciler) traefikResource(mcpServer *mcpv1alpha1.MCPServer, gvk schema.GroupVersionKind, name string, spec map[string]any) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	obj.SetName(name)
	obj.SetNamespace(mcpServer.Namespace)
	obj.Object["spec"] = spec
	obj.SetLabels(map[string]string{
		"app.kubernetes.io/managed-by": "mcp-runtime",
		"mcpruntime.org/server":        mcpServer.Name,
	})
	obj.SetOwnerReferences([]metav1.OwnerReference{*metav1.NewControllerRef(mcpServer, mcpv1alpha1.GroupVersion.WithKind("MCPServer"))})
	return obj
}

// adapterCertificateEntryPoints follows the operator's configured ingress
// entrypoints, so the IngressRoute is served where the plain Ingress was.
// Client certificates need TLS, so the fallback is websecure.
func (r *MCPServerReconciler) adapterCertificateEntryPoints() []any {
	var entryPoints []any
	for _, entryPoint := range strings.Split(r.DefaultIngressEntryPoints, ",") {
		if trimmed := strings.TrimSpace(entryPoint); trimmed != "" {
			entryPoints = append(entryPoints, trimmed)
		}
	}
	if len(entryPoints) == 0 {
		return []any{"websecure"}
	}
	return entryPoints
}
