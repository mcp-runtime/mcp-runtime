package sessionapi

import (
	"context"
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	authv1 "k8s.io/api/authentication/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/rest"
	mcpv1 "mcp-runtime/api/v1alpha1"
	"mcp-runtime/pkg/sessionstate"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type KubernetesIdentity struct{ Client client.Client }

func NewKubernetesIdentity() (*KubernetesIdentity, error) {
	config, err := rest.InClusterConfig()
	if err != nil {
		return nil, err
	}
	config.Timeout = 5_000_000_000
	scheme := runtime.NewScheme()
	if err = corev1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err = appsv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err = authv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err = mcpv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := client.New(config, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	return &KubernetesIdentity{Client: c}, nil
}

func controller(meta metav1.Object, kind, apiVersion string) (metav1.OwnerReference, error) {
	for _, owner := range meta.GetOwnerReferences() {
		if owner.Controller != nil && *owner.Controller && owner.Kind == kind && owner.APIVersion == apiVersion && owner.UID != "" && owner.Name != "" {
			return owner, nil
		}
	}
	return metav1.OwnerReference{}, fmt.Errorf("missing %s controller", kind)
}

// Authenticate verifies the audience and live Pod binding, then follows UID
// checked controller references. A tenant's shared service-account username or
// caller-supplied server label is insufficient to select a server.
func (k KubernetesIdentity) Authenticate(ctx context.Context, token string) (sessionstate.Server, error) {
	if k.Client == nil || token == "" {
		return sessionstate.Server{}, fmt.Errorf("identity unavailable")
	}
	review := &authv1.TokenReview{Spec: authv1.TokenReviewSpec{Token: token, Audiences: []string{sessionstate.GatewayAudience}}}
	if err := k.Client.Create(ctx, review); err != nil {
		return sessionstate.Server{}, err
	}
	if !review.Status.Authenticated || review.Status.Error != "" || !slices.Contains(review.Status.Audiences, sessionstate.GatewayAudience) {
		return sessionstate.Server{}, fmt.Errorf("token audience rejected")
	}
	parts := strings.Split(review.Status.User.Username, ":")
	if len(parts) != 4 || parts[0] != "system" || parts[1] != "serviceaccount" || parts[2] == "" || parts[3] == "" {
		return sessionstate.Server{}, fmt.Errorf("service-account identity required")
	}
	boundName := review.Status.User.Extra["authentication.kubernetes.io/pod-name"]
	boundUID := review.Status.User.Extra["authentication.kubernetes.io/pod-uid"]
	if len(boundName) != 1 || len(boundUID) != 1 || boundName[0] == "" || boundUID[0] == "" {
		return sessionstate.Server{}, fmt.Errorf("Pod-bound token required")
	}
	namespace := parts[2]
	pod := &corev1.Pod{}
	if err := k.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: boundName[0]}, pod); err != nil {
		return sessionstate.Server{}, err
	}
	if string(pod.UID) != boundUID[0] || pod.Spec.ServiceAccountName != parts[3] || pod.DeletionTimestamp != nil {
		return sessionstate.Server{}, fmt.Errorf("Pod binding mismatch")
	}
	owner, err := controller(pod, "ReplicaSet", "apps/v1")
	if err != nil {
		return sessionstate.Server{}, err
	}
	rs := &appsv1.ReplicaSet{}
	if err = k.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: owner.Name}, rs); err != nil {
		return sessionstate.Server{}, err
	}
	if rs.UID != owner.UID || rs.DeletionTimestamp != nil {
		return sessionstate.Server{}, fmt.Errorf("ReplicaSet generation mismatch")
	}
	owner, err = controller(rs, "Deployment", "apps/v1")
	if err != nil {
		return sessionstate.Server{}, err
	}
	deployment := &appsv1.Deployment{}
	if err = k.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: owner.Name}, deployment); err != nil {
		return sessionstate.Server{}, err
	}
	if deployment.UID != owner.UID || deployment.DeletionTimestamp != nil {
		return sessionstate.Server{}, fmt.Errorf("Deployment generation mismatch")
	}
	owner, err = controller(deployment, "MCPServer", mcpv1.GroupVersion.String())
	if err != nil {
		return sessionstate.Server{}, err
	}
	server := &mcpv1.MCPServer{}
	if err = k.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: owner.Name}, server); err != nil {
		return sessionstate.Server{}, err
	}
	if server.UID != owner.UID || server.DeletionTimestamp != nil || server.Annotations["mcpruntime.org/retiring"] == "true" {
		return sessionstate.Server{}, fmt.Errorf("server generation retired")
	}
	if deployment.Name != server.Name && deployment.Name != server.Name+"-canary" {
		return sessionstate.Server{}, fmt.Errorf("unexpected server deployment")
	}
	return sessionstate.Server{Namespace: namespace, Name: server.Name, UID: string(server.UID)}, nil
}
