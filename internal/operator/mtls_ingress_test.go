package operator

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

func traefikScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := mcpv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := networkingv1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("scheme: %v", err)
	}
	for _, gvk := range []schema.GroupVersionKind{
		ingressRouteGVK, middlewareGVK, tlsOptionGVK, serversTransportGVK, ingressRouteTCPGVK, tlsStoreGVK,
	} {
		scheme.AddKnownTypeWithName(gvk, &unstructured.Unstructured{})
		scheme.AddKnownTypeWithName(gvk.GroupVersion().WithKind(gvk.Kind+"List"), &unstructured.UnstructuredList{})
	}
	return scheme
}

func TestReconcileIngressReplacesPlainIngressWithAdapterCertificateRoute(t *testing.T) {
	scheme := traefikScheme(t)
	server := mtlsServer()
	server.Spec.PublicPathPrefix = "oauth-server"
	plainIngress := &networkingv1.Ingress{ObjectMeta: metav1.ObjectMeta{Name: server.Name, Namespace: server.Namespace}}
	objs := append([]client.Object{server, plainIngress}, mtlsBackendSecretObjects(server)...)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}

	if err := r.reconcileIngress(context.Background(), server); err != nil {
		t.Fatalf("reconcileIngress: %v", err)
	}
	ingress := &networkingv1.Ingress{}
	if err := client.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, ingress); !apierrors.IsNotFound(err) {
		t.Fatalf("plain HTTP Ingress should be removed for the TLS-only gateway, got %v", err)
	}
	getCR(t, client, ingressRouteGVK, server.Name, server.Namespace)
}

func getCR(t *testing.T, c client.Client, gvk schema.GroupVersionKind, name, ns string) *unstructured.Unstructured {
	t.Helper()
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(context.Background(), types.NamespacedName{Name: name, Namespace: ns}, obj); err != nil {
		t.Fatalf("get %s/%s: %v", gvk.Kind, name, err)
	}
	return obj
}

func crFixture(gvk schema.GroupVersionKind, name, ns string) *unstructured.Unstructured {
	o := &unstructured.Unstructured{}
	o.SetGroupVersionKind(gvk)
	o.SetName(name)
	o.SetNamespace(ns)
	return o
}

func mtlsBackendSecretObjects(server *mcpv1alpha1.MCPServer) []client.Object {
	return []client.Object{
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: traefikClientCertSecretName(server), Namespace: server.Namespace},
			Type:       corev1.SecretTypeTLS,
			Data:       map[string][]byte{"tls.crt": []byte("crt"), "tls.key": []byte("key")},
		},
		&corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: mtlsTrustBundleSecretName(server), Namespace: server.Namespace},
			Type:       corev1.SecretTypeOpaque,
			Data:       map[string][]byte{"tls.ca": []byte("ca"), "ca.crt": []byte("ca")},
		},
	}
}

func TestReconcileMTLSIngressDefersUntilBackendSecretsExist(t *testing.T) {
	scheme := traefikScheme(t)
	server := mtlsServer()
	legacy := crFixture(ingressRouteTCPGVK, server.Name, server.Namespace)
	existingRoute := crFixture(ingressRouteGVK, server.Name, server.Namespace)
	existingTransport := crFixture(serversTransportGVK, mtlsServersTransportName(server), server.Namespace)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server, legacy, existingRoute, existingTransport).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}

	if err := r.reconcileMTLSIngress(context.Background(), server); err != nil {
		t.Fatalf("reconcileMTLSIngress: %v", err)
	}
	leftover := &unstructured.Unstructured{}
	leftover.SetGroupVersionKind(ingressRouteTCPGVK)
	if err := c.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, leftover); !apierrors.IsNotFound(err) {
		t.Fatalf("legacy IngressRouteTCP should be deleted, got %v", err)
	}
	// Existing live routes must survive a temporary secret gap (cert rotation).
	getCR(t, c, ingressRouteGVK, server.Name, server.Namespace)
	getCR(t, c, serversTransportGVK, mtlsServersTransportName(server), server.Namespace)

	fresh := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
	r.Client = fresh
	if err := r.reconcileMTLSIngress(context.Background(), server); err != nil {
		t.Fatalf("reconcileMTLSIngress without secrets: %v", err)
	}
	route := &unstructured.Unstructured{}
	route.SetGroupVersionKind(ingressRouteGVK)
	if err := fresh.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, route); !apierrors.IsNotFound(err) {
		t.Fatalf("IngressRoute must not be created before mTLS backend secrets, got %v", err)
	}
}

func TestReconcileMTLSIngressGeneratesTraefikResources(t *testing.T) {
	scheme := traefikScheme(t)
	server := mtlsServer()
	server.Spec.PublicPathPrefix = "secure-server"
	server.Spec.IngressHost = "mcp.example.com"

	// A leftover passthrough route from the old model must be removed.
	legacy := crFixture(ingressRouteTCPGVK, server.Name, server.Namespace)
	objs := append([]client.Object{server, legacy}, mtlsBackendSecretObjects(server)...)
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: client, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}

	if err := r.reconcileMTLSIngress(context.Background(), server); err != nil {
		t.Fatalf("reconcileMTLSIngress: %v", err)
	}

	leftover := &unstructured.Unstructured{}
	leftover.SetGroupVersionKind(ingressRouteTCPGVK)
	if err := client.Get(context.Background(), types.NamespacedName{Name: server.Name, Namespace: server.Namespace}, leftover); !apierrors.IsNotFound(err) {
		t.Fatalf("legacy IngressRouteTCP should be deleted, got %v", err)
	}

	tlsOpt := getCR(t, client, tlsOptionGVK, mtlsTLSOptionName(server), server.Namespace)
	if authType, _, _ := unstructured.NestedString(tlsOpt.Object, "spec", "clientAuth", "clientAuthType"); authType != "VerifyClientCertIfGiven" {
		t.Fatalf("clientAuthType = %q, want VerifyClientCertIfGiven so OAuth clients without a certificate still connect", authType)
	}
	if secrets, _, _ := unstructured.NestedStringSlice(tlsOpt.Object, "spec", "clientAuth", "secretNames"); len(secrets) != 1 || secrets[0] != mtlsTrustBundleSecretName(server) {
		t.Fatalf("clientAuth secretNames = %v, want trust bundle", secrets)
	}

	mw := getCR(t, client, middlewareGVK, mtlsMiddlewareName(server), server.Namespace)
	if header, _, _ := unstructured.NestedString(mw.Object, "spec", "plugin", spiffeIdentityPluginName, "verifiedHeader"); header != verifiedSPIFFEHeader {
		t.Fatalf("middleware verifiedHeader = %q", header)
	}
	if td, _, _ := unstructured.NestedString(mw.Object, "spec", "plugin", spiffeIdentityPluginName, "trustDomain"); td != "example.org" {
		t.Fatalf("middleware trustDomain = %q", td)
	}

	st := getCR(t, client, serversTransportGVK, mtlsServersTransportName(server), server.Namespace)
	if sn, _, _ := unstructured.NestedString(st.Object, "spec", "serverName"); sn != gatewayServerName(server) {
		t.Fatalf("serversTransport serverName = %q", sn)
	}
	if certs, _, _ := unstructured.NestedStringSlice(st.Object, "spec", "certificatesSecrets"); len(certs) != 1 || certs[0] != traefikClientCertSecretName(server) {
		t.Fatalf("certificatesSecrets = %v", certs)
	}
	if roots, _, _ := unstructured.NestedStringSlice(st.Object, "spec", "rootCAsSecrets"); len(roots) != 1 || roots[0] != mtlsTrustBundleSecretName(server) {
		t.Fatalf("rootCAsSecrets = %v", roots)
	}

	ir := getCR(t, client, ingressRouteGVK, server.Name, server.Namespace)
	routes, _, _ := unstructured.NestedSlice(ir.Object, "spec", "routes")
	// OAuth servers route the MCP path and its protected-resource metadata.
	if len(routes) != 2 {
		t.Fatalf("routes = %d, want 2 (MCP path + OAuth metadata)", len(routes))
	}
	if metadataMatch, _ := routes[1].(map[string]any)["match"].(string); !strings.Contains(metadataMatch, "Path(`/.well-known/oauth-protected-resource/secure-server/mcp`)") {
		t.Fatalf("metadata route match = %q, want the protected-resource metadata path", metadataMatch)
	}
	route0 := routes[0].(map[string]any)
	match, _ := route0["match"].(string)
	if !strings.Contains(match, "Host(`mcp.example.com`)") || !strings.Contains(match, "PathPrefix(`/secure-server/mcp`)") {
		t.Fatalf("match = %q, want host + path prefix", match)
	}
	if tlsName, _, _ := unstructured.NestedString(ir.Object, "spec", "tls", "options", "name"); tlsName != mtlsTLSOptionName(server) {
		t.Fatalf("tls.options.name = %q", tlsName)
	}
	// The IngressRoute must reference the Kubernetes Service port (80), not the
	// gateway container port (8091). Traefik resolves endpoints from the Service
	// and then connects to pod:targetPort — using the container port causes
	// "service port not found" because it never appears in Service.spec.ports.
	services, _, _ := unstructured.NestedSlice(route0, "services")
	if len(services) != 1 {
		t.Fatalf("route services = %d, want 1", len(services))
	}
	svcPort, _, _ := unstructured.NestedFieldNoCopy(services[0].(map[string]any), "port")
	if svcPort != int64(server.Spec.ServicePort) {
		t.Fatalf("IngressRoute service port = %v, want ServicePort (%d) not Gateway.Port (%d)",
			svcPort, server.Spec.ServicePort, server.Spec.Gateway.Port)
	}
}

func TestReconcileMTLSIngressNeverSetsPerRouteSecretName(t *testing.T) {
	// The caller-facing host cert is the cluster-wide default (TLSStore), never a
	// per-IngressRoute secretName — which Traefik would resolve only in the
	// tenant namespace, where the shared platform host secret does not exist.
	scheme := traefikScheme(t)
	server := mtlsServer()
	server.Spec.IngressHost = "mcp.example.com"
	server.Spec.PublicPathPrefix = "secure-demo"

	objs := append([]client.Object{server}, mtlsBackendSecretObjects(server)...)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, DefaultIngressTLSSecret: "platform-host-tls", DefaultIngressTLSSecretNamespace: "mcp-servers", AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
	if err := r.reconcileMTLSIngress(context.Background(), server); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	ir := getCR(t, c, ingressRouteGVK, server.Name, server.Namespace)
	if _, found, _ := unstructured.NestedString(ir.Object, "spec", "tls", "secretName"); found {
		t.Fatal("tls.secretName must never be set on the IngressRoute (cross-namespace); use the default TLSStore")
	}
	// With a platform TLS namespace every route uses the shared default
	// TLSOption; a per-route option would make Traefik fall back to defaults
	// on a host shared by several servers.
	if _, found, _ := unstructured.NestedMap(ir.Object, "spec", "tls", "options"); found {
		t.Fatal("tls.options must be unset so the route uses the platform default TLSOption")
	}
}

func TestReconcileDefaultClientAuthTLSOption(t *testing.T) {
	scheme := traefikScheme(t)
	caSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: platformClientAuthCASecret, Namespace: "traefik"},
		Data:       map[string][]byte{"ca.crt": []byte("ca")},
	}
	reconciler := func(objects ...client.Object) (MCPServerReconciler, client.Client) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
		return MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, DefaultIngressTLSSecretNamespace: "traefik"}, c
	}

	t.Run("requests client certificates on every host once the CA exists", func(t *testing.T) {
		r, c := reconciler(caSecret.DeepCopy())
		if err := r.reconcileDefaultClientAuthTLSOption(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		option := getCR(t, c, tlsOptionGVK, "default", "traefik")
		if authType, _, _ := unstructured.NestedString(option.Object, "spec", "clientAuth", "clientAuthType"); authType != "VerifyClientCertIfGiven" {
			t.Fatalf("clientAuthType = %q, want VerifyClientCertIfGiven", authType)
		}
		if names, _, _ := unstructured.NestedStringSlice(option.Object, "spec", "clientAuth", "secretNames"); len(names) != 1 || names[0] != platformClientAuthCASecret {
			t.Fatalf("secretNames = %v", names)
		}
	})

	t.Run("waits for a pre-created empty trust bundle", func(t *testing.T) {
		placeholder := caSecret.DeepCopy()
		placeholder.Data = nil
		r, c := reconciler(placeholder)
		if err := r.reconcileDefaultClientAuthTLSOption(context.Background()); err != nil {
			t.Fatal(err)
		}
		option := &unstructured.Unstructured{}
		option.SetGroupVersionKind(tlsOptionGVK)
		if err := c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "traefik"}, option); !apierrors.IsNotFound(err) {
			t.Fatalf("TLSOption created before trust roots: %v", err)
		}
	})

	t.Run("waits for the CA before creating the option", func(t *testing.T) {
		r, c := reconciler()
		if err := r.reconcileDefaultClientAuthTLSOption(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		option := &unstructured.Unstructured{}
		option.SetGroupVersionKind(tlsOptionGVK)
		if err := c.Get(context.Background(), types.NamespacedName{Name: "default", Namespace: "traefik"}, option); !apierrors.IsNotFound(err) {
			t.Fatalf("default TLSOption should wait for the CA, got %v", err)
		}
	})

	t.Run("leaves a cluster-owned default TLSOption alone", func(t *testing.T) {
		owned := crFixture(tlsOptionGVK, "default", "traefik")
		r, _ := reconciler(caSecret.DeepCopy(), owned)
		err := r.reconcileDefaultClientAuthTLSOption(context.Background())
		if err == nil || !strings.Contains(err.Error(), "not managed by mcp-runtime") {
			t.Fatalf("err = %v, want a refusal to overwrite the cluster's TLSOption", err)
		}
	})
}

func TestReconcileDefaultTLSStore(t *testing.T) {
	scheme := traefikScheme(t)
	server := mtlsServer()
	key := types.NamespacedName{Name: "default", Namespace: "mcp-servers"}

	t.Run("creates a single default store in the configured namespace", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, DefaultIngressTLSSecret: "platform-host-tls", DefaultIngressTLSSecretNamespace: "mcp-servers", AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}
		if err := r.reconcileDefaultTLSStore(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		store := getCR(t, c, tlsStoreGVK, "default", "mcp-servers")
		if sn, _, _ := unstructured.NestedString(store.Object, "spec", "defaultCertificate", "secretName"); sn != "platform-host-tls" {
			t.Fatalf("defaultCertificate.secretName = %q, want platform-host-tls", sn)
		}
	})

	t.Run("no-op when host secret/namespace unset", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(server).Build()
		r := MCPServerReconciler{
			GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"} // no DefaultIngressTLSSecret*
		if err := r.reconcileDefaultTLSStore(context.Background()); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		store := &unstructured.Unstructured{}
		store.SetGroupVersionKind(tlsStoreGVK)
		if err := c.Get(context.Background(), key, store); !apierrors.IsNotFound(err) {
			t.Fatalf("expected no TLSStore when unconfigured, got %v", err)
		}
	})
}

func TestDeleteMTLSIngressRemovesAllResources(t *testing.T) {
	scheme := traefikScheme(t)
	server := mtlsServer()

	targets := []struct {
		gvk  schema.GroupVersionKind
		name string
	}{
		{ingressRouteGVK, server.Name},
		{middlewareGVK, mtlsMiddlewareName(server)},
		{tlsOptionGVK, mtlsTLSOptionName(server)},
		{serversTransportGVK, mtlsServersTransportName(server)},
		{ingressRouteTCPGVK, server.Name},
	}
	objs := []client.Object{server}
	for _, target := range targets {
		objs = append(objs, crFixture(target.gvk, target.name, server.Namespace))
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
	r := MCPServerReconciler{
		GatewayProxyImage: "example.com/mcp-gateway:test", Client: c, Scheme: scheme, AdapterCertificatesEnabled: true, AdapterTrustDomain: "example.org", MTLSClusterIssuer: "mcp-runtime-ca"}

	if err := r.deleteMTLSIngress(context.Background(), server); err != nil {
		t.Fatalf("deleteMTLSIngress: %v", err)
	}
	for _, target := range targets {
		obj := &unstructured.Unstructured{}
		obj.SetGroupVersionKind(target.gvk)
		if err := c.Get(context.Background(), types.NamespacedName{Name: target.name, Namespace: server.Namespace}, obj); !apierrors.IsNotFound(err) {
			t.Fatalf("%s/%s should be deleted, got %v", target.gvk.Kind, target.name, err)
		}
	}
}
