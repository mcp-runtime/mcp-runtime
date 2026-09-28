package operator

import (
	"context"
	"net/url"
	"path"
	"reflect"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
)

const (
	bundledOAuthNamespace  = "mcp-sentinel"
	bundledOAuthDeployment = "mcp-auth-server"
)

// The list is delivered as Deployment env, so each change to the set of
// audiences rolls the mcp-auth-server pod; adding or removing an OAuth server
// therefore interrupts logins in flight. Moving the list to a watched
// ConfigMap needs mcp-auth support for reloading it.
//
// reconcileBundledOAuthResources keeps the optional bundled authorization
// server's accepted resource list aligned with the audiences advertised by
// OAuth MCPServers. It runs for every MCPServer event, including a deleted
// object's final reconcile, so additions, changes, and removals are reflected.
func (r *MCPServerReconciler) reconcileBundledOAuthResources(ctx context.Context) error {
	if strings.TrimSpace(r.OAuthIssuerURL) == "" {
		return nil
	}

	var servers mcpv1alpha1.MCPServerList
	if err := r.List(ctx, &servers); err != nil {
		return err
	}
	defaulted := make([]mcpv1alpha1.MCPServer, 0, len(servers.Items))
	for i := range servers.Items {
		defaulted = append(defaulted, *r.defaultedMCPServerForReconcile(&servers.Items[i]))
	}
	resources := make([]string, 0, len(defaulted))
	seen := make(map[string]struct{}, len(defaulted))
	for i := range defaulted {
		server := &defaulted[i]
		// A server refused its public route must not publish that route's
		// audience either, or it would share tokens with the route's owner.
		if publicRouteOwner(server, defaulted) != nil {
			continue
		}
		if server.Spec.Auth == nil {
			continue
		}
		if strings.TrimRight(strings.TrimSpace(server.Spec.Auth.IssuerURL), "/") != strings.TrimRight(strings.TrimSpace(r.OAuthIssuerURL), "/") {
			continue
		}
		audience := strings.TrimSpace(server.Spec.Auth.Audience)
		if audience == "" || !r.audienceServedByPlatform(server, audience) {
			continue
		}
		if _, ok := seen[audience]; ok {
			continue
		}
		seen[audience] = struct{}{}
		resources = append(resources, audience)
	}
	sort.Strings(resources)

	deployment := &appsv1.Deployment{}
	key := types.NamespacedName{Name: bundledOAuthDeployment, Namespace: bundledOAuthNamespace}
	if err := r.Get(ctx, key, deployment); err != nil {
		if apierrors.IsNotFound(err) {
			return nil
		}
		return err
	}
	if len(deployment.Spec.Template.Spec.Containers) == 0 {
		return nil
	}

	before := deployment.DeepCopy()
	envs := deployment.Spec.Template.Spec.Containers[0].Env
	upsertEnv := func(name, value string) {
		for i := range envs {
			if envs[i].Name == name {
				envs[i].Value = value
				envs[i].ValueFrom = nil
				return
			}
		}
		envs = append(envs, corev1.EnvVar{Name: name, Value: value})
	}
	removeEnv := func(name string) {
		filtered := envs[:0]
		for _, env := range envs {
			if env.Name != name {
				filtered = append(filtered, env)
			}
		}
		envs = filtered
	}
	upsertEnv("MCP_AUTH_RESOURCES", strings.Join(resources, ","))
	if len(resources) == 0 {
		removeEnv("MCP_AUTH_RESOURCE")
	} else {
		upsertEnv("MCP_AUTH_RESOURCE", resources[0])
	}
	deployment.Spec.Template.Spec.Containers[0].Env = envs
	if reflect.DeepEqual(before.Spec.Template.Spec.Containers[0].Env, envs) {
		return nil
	}
	return r.Patch(ctx, deployment, client.MergeFrom(before))
}

// audienceServedByPlatform reports whether an MCPServer's audience names the
// route that server is actually served on. MCPServer objects are tenant
// input, and the bundled authorization server mints tokens for every listed
// resource, so an audience is only published when its path is the server's
// own public path and its host is one the platform serves it on: the
// server's ingress host, the operator default MCP host, or, when neither is
// known (local test mode), the issuer's host. A server cannot enlist another
// host, an external URL, or a different route.
func (r *MCPServerReconciler) audienceServedByPlatform(server *mcpv1alpha1.MCPServer, audience string) bool {
	parsed, err := url.Parse(audience)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return false
	}
	// An https issuer is a production authorization server; setup refuses
	// http resources for it, so a mis-detected TLS setting must not slip one
	// in here either.
	if issuer, err := url.Parse(strings.TrimSpace(r.OAuthIssuerURL)); err == nil && issuer.Scheme == "https" && parsed.Scheme != "https" {
		return false
	}
	publicPath := strings.TrimSpace(server.EffectivePublicPath())
	if publicPath == "" || path.Clean("/"+strings.TrimLeft(parsed.Path, "/")) != path.Clean("/"+strings.TrimLeft(publicPath, "/")) {
		return false
	}
	hosts := []string{strings.TrimSpace(server.Spec.IngressHost), strings.TrimSpace(r.DefaultIngressHost)}
	if hosts[0] == "" && hosts[1] == "" {
		if issuer, err := url.Parse(strings.TrimSpace(r.OAuthIssuerURL)); err == nil {
			hosts = append(hosts, issuer.Host)
		}
	}
	for _, host := range hosts {
		if host != "" && strings.EqualFold(host, parsed.Host) {
			return true
		}
	}
	return false
}

// bundledOAuthResourcesKey is the single work item for the bundled
// authorization server's resource list; every trigger collapses onto it.
var bundledOAuthResourcesKey = ctrl.Request{NamespacedName: types.NamespacedName{Namespace: bundledOAuthNamespace, Name: bundledOAuthDeployment}}

// bundledOAuthResourcesReconciler keeps the resource list in its own
// controller: it runs once per burst of MCPServer changes instead of inside
// every server's reconcile, a failure to reach the auth Deployment no longer
// blocks unrelated servers, and a Deployment rewritten by setup is corrected.
type bundledOAuthResourcesReconciler struct {
	*MCPServerReconciler
}

func (b bundledOAuthResourcesReconciler) Reconcile(ctx context.Context, _ ctrl.Request) (ctrl.Result, error) {
	return ctrl.Result{}, b.reconcileBundledOAuthResources(ctx)
}

func (r *MCPServerReconciler) setupBundledOAuthResourcesController(mgr ctrl.Manager) error {
	if strings.TrimSpace(r.OAuthIssuerURL) == "" {
		return nil
	}
	toKey := handler.EnqueueRequestsFromMapFunc(func(context.Context, client.Object) []ctrl.Request {
		return []ctrl.Request{bundledOAuthResourcesKey}
	})
	isAuthDeployment := predicate.NewPredicateFuncs(func(obj client.Object) bool {
		return obj.GetNamespace() == bundledOAuthNamespace && obj.GetName() == bundledOAuthDeployment
	})
	return ctrl.NewControllerManagedBy(mgr).
		Named("bundled-oauth-resources").
		Watches(&mcpv1alpha1.MCPServer{}, toKey).
		Watches(&appsv1.Deployment{}, toKey, builder.WithPredicates(isAuthDeployment)).
		Complete(bundledOAuthResourcesReconciler{r})
}
