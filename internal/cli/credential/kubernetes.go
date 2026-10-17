package credential

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
	"mcp-runtime/internal/cli/platformapi"
	cb "mcp-runtime/pkg/credentialbroker"
)

func selectedKubernetes(kubeconfig, kubeContext string) (kubernetes.Interface, error) {
	if kubeconfig == "" || kubeContext == "" {
		return nil, errors.New("--kubeconfig and --context are required")
	}
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	rules.ExplicitPath = kubeconfig
	config, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext}).ClientConfig()
	if err != nil {
		return nil, errors.New("selected Kubernetes context unavailable")
	}
	return kubernetes.NewForConfig(config)
}

func authorizeKubernetesCommand() *cobra.Command {
	var ns, kubeconfig, kubeContext string
	var revoke, yes bool
	cmd := &cobra.Command{Use: "authorize-kubernetes NAME", Short: "Grant or revoke broker GET permission for one approved Secret", Long: "Use an explicit cluster administrator context to grant only GET on the exact Secret referenced by an approved Kubernetes source. Existing unrelated RBAC is never replaced. Revoke this grant before removing the source metadata.", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if !yes {
			return errors.New("--yes is required to change provider permissions")
		}
		api, err := platformapi.NewPlatformClient()
		if err != nil {
			return err
		}
		raw, err := api.CredentialMetadata(cmd.Context(), http.MethodGet, ns, "sources", args[0], nil, 0)
		if err != nil {
			return err
		}
		var source cb.Source
		if json.Unmarshal(raw, &source) != nil || source.Validate() != nil || source.Provider != "kubernetes" {
			return errors.New("an approved Kubernetes source is required")
		}
		kube, err := selectedKubernetes(kubeconfig, kubeContext)
		if err != nil {
			return err
		}
		if err = authorizeSecret(cmd.Context(), kube, source, revoke); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Exact Secret provider permission reconciled")
		return nil
	}}
	cmd.Flags().StringVar(&ns, "namespace", "", "Approved source namespace (required)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	cmd.Flags().BoolVar(&revoke, "revoke", false, "Revoke this source's exact Secret read permission")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the provider permission change")
	return cmd
}

func authorizeSecret(ctx context.Context, kube kubernetes.Interface, source cb.Source, revoke bool) error {
	name := "mcp-credential-" + source.Name
	labels := map[string]string{"app.kubernetes.io/managed-by": "mcp-runtime", "mcpruntime.org/credential-source": source.Name}
	rules := []rbacv1.PolicyRule{{APIGroups: []string{""}, Resources: []string{"secrets"}, ResourceNames: []string{source.Resource}, Verbs: []string{"get"}}}
	role := &rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: source.Namespace, Labels: labels}, Rules: rules}
	binding := &rbacv1.RoleBinding{ObjectMeta: role.ObjectMeta, RoleRef: rbacv1.RoleRef{APIGroup: rbacv1.GroupName, Kind: "Role", Name: name}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "mcp-platform-api", Namespace: "mcp-platform"}}}
	oldRole, err := kube.RbacV1().Roles(source.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	if err == nil && (!reflect.DeepEqual(oldRole.Rules, role.Rules) || oldRole.Labels["mcpruntime.org/credential-source"] != source.Name) {
		return errors.New("existing provider Role differs; refusing replacement")
	}
	oldBinding, bindingErr := kube.RbacV1().RoleBindings(source.Namespace).Get(ctx, name, metav1.GetOptions{})
	if bindingErr != nil && !apierrors.IsNotFound(bindingErr) {
		return bindingErr
	}
	if bindingErr == nil && (!reflect.DeepEqual(oldBinding.RoleRef, binding.RoleRef) || !reflect.DeepEqual(oldBinding.Subjects, binding.Subjects) || oldBinding.Labels["mcpruntime.org/credential-source"] != source.Name) {
		return errors.New("existing provider RoleBinding differs; refusing replacement")
	}
	if revoke {
		if bindingErr == nil {
			uid := oldBinding.UID
			if err = kube.RbacV1().RoleBindings(source.Namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}}); err != nil {
				return err
			}
		}
		if oldRole != nil {
			uid := oldRole.UID
			return kube.RbacV1().Roles(source.Namespace).Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid}})
		}
		return nil
	}
	if apierrors.IsNotFound(err) {
		if _, err = kube.RbacV1().Roles(source.Namespace).Create(ctx, role, metav1.CreateOptions{}); err != nil {
			return err
		}
	}
	if apierrors.IsNotFound(bindingErr) {
		_, err = kube.RbacV1().RoleBindings(source.Namespace).Create(ctx, binding, metav1.CreateOptions{})
		return err
	}
	return nil
}

func configureCommand() *cobra.Command {
	var origin, kubeconfig, kubeContext, vaultURL, vaultRole, gcpAccount, awsRole, azureClient string
	var yes bool
	cmd := &cobra.Command{Use: "configure", Short: "Configure the operator's verified HTTPS broker origin", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if gcpAccount != "" && !regexp.MustCompile(`^[a-z0-9-]+@[a-z0-9-]+\.iam\.gserviceaccount\.com$`).MatchString(gcpAccount) {
			return errors.New("invalid GCP workload service-account email")
		}
		if awsRole != "" && !regexp.MustCompile(`^arn:aws[a-z-]*:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`).MatchString(awsRole) {
			return errors.New("invalid AWS workload role ARN")
		}
		if azureClient != "" {
			if _, err := uuid.Parse(azureClient); err != nil {
				return errors.New("invalid Azure workload client UUID")
			}
		}
		if (vaultURL == "") != (vaultRole == "") {
			return errors.New("--vault-url and --vault-role must be supplied together")
		}
		if vaultURL != "" {
			v, err := url.Parse(vaultURL)
			if err != nil || v.Scheme != "https" || v.Host == "" || v.User != nil || v.RawQuery != "" || v.Fragment != "" || v.Path != "" || !cb.NameValid(vaultRole) {
				return errors.New("configure Vault with an HTTPS origin and a valid workload role name")
			}
		}
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return errors.New("--broker-url must be a verified HTTPS platform origin")
		}
		if !yes {
			return errors.New("--yes is required to configure credential delivery")
		}
		kube, err := selectedKubernetes(kubeconfig, kubeContext)
		if err != nil {
			return err
		}
		err = configureOperator(cmd.Context(), kube, origin)
		if err != nil {
			return err
		}
		if vaultURL != "" {
			if err = configureVault(cmd.Context(), kube, vaultURL, vaultRole); err != nil {
				return err
			}
		}
		if gcpAccount != "" || awsRole != "" || azureClient != "" {
			if err = configureWorkloadIdentity(cmd.Context(), kube, gcpAccount, awsRole, azureClient); err != nil {
				return err
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Credential broker origin configured; credential-enabled gateways will reconcile")
		return nil
	}}
	cmd.Flags().StringVar(&origin, "broker-url", "", "Public HTTPS platform origin trusted by gateway system roots")
	cmd.Flags().StringVar(&vaultURL, "vault-url", "", "Optional verified HTTPS Vault origin for Kubernetes workload authentication")
	cmd.Flags().StringVar(&vaultRole, "vault-role", "", "Vault Kubernetes auth role bound to the platform API service account and mcp-runtime-vault audience")
	cmd.Flags().StringVar(&gcpAccount, "gcp-service-account", "", "GKE workload identity service-account email (cloud IAM federation must already be configured)")
	cmd.Flags().StringVar(&awsRole, "aws-role-arn", "", "EKS workload identity role ARN (cloud IAM federation must already be configured)")
	cmd.Flags().StringVar(&azureClient, "azure-client-id", "", "Azure workload identity client UUID (cloud federation must already be configured)")
	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	cmd.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	cmd.Flags().BoolVar(&yes, "yes", false, "Confirm the operator configuration change")
	return cmd
}

func configureWorkloadIdentity(ctx context.Context, kube kubernetes.Interface, gcpAccount, awsRole, azureClient string) error {
	var identityRevision string
	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		sa, err := kube.CoreV1().ServiceAccounts("mcp-platform").Get(ctx, "mcp-platform-api", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if sa.Annotations == nil {
			sa.Annotations = map[string]string{}
		}
		keys := []string{"iam.gke.io/gcp-service-account", "eks.amazonaws.com/role-arn", "azure.workload.identity/client-id"}
		values := []string{gcpAccount, awsRole, azureClient}
		var parts []string
		for i, key := range keys {
			if values[i] != "" {
				sa.Annotations[key] = values[i]
			}
			parts = append(parts, sa.Annotations[key])
		}
		identityRevision = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(parts, "\n"))))
		_, err = kube.CoreV1().ServiceAccounts(sa.Namespace).Update(ctx, sa, metav1.UpdateOptions{})
		return err
	})
	if err != nil {
		return err
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := kube.AppsV1().Deployments("mcp-platform").Get(ctx, "mcp-platform-api", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if deployment.Spec.Template.Annotations == nil {
			deployment.Spec.Template.Annotations = map[string]string{}
		}
		deployment.Spec.Template.Annotations["mcpruntime.org/credential-workload-identity"] = identityRevision
		if azureClient != "" {
			if deployment.Spec.Template.Labels == nil {
				deployment.Spec.Template.Labels = map[string]string{}
			}
			deployment.Spec.Template.Labels["azure.workload.identity/use"] = "true"
		}
		_, err = kube.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
}

func configureVault(ctx context.Context, kube kubernetes.Interface, origin, role string) error {
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := kube.AppsV1().Deployments("mcp-platform").Get(ctx, "mcp-platform-api", metav1.GetOptions{})
		if err != nil {
			return err
		}
		var target *corev1.Container
		for i := range deployment.Spec.Template.Spec.Containers {
			if deployment.Spec.Template.Spec.Containers[i].Name == "platform-api" {
				target = &deployment.Spec.Template.Spec.Containers[i]
			}
		}
		if target == nil {
			return errors.New("platform API container unavailable")
		}
		for _, setting := range []corev1.EnvVar{{Name: "CREDENTIAL_VAULT_ADDR", Value: origin}, {Name: "CREDENTIAL_VAULT_ROLE", Value: role}, {Name: "CREDENTIAL_VAULT_KUBERNETES_TOKEN_FILE", Value: "/var/run/mcp-runtime/vault/token"}} {
			found := false
			for i := range target.Env {
				if target.Env[i].Name == setting.Name {
					target.Env[i] = setting
					found = true
				}
			}
			if !found {
				target.Env = append(target.Env, setting)
			}
		}
		const volumeName = "credential-vault-identity"
		expiry := int64(600)
		volume := corev1.Volume{Name: volumeName, VolumeSource: corev1.VolumeSource{Projected: &corev1.ProjectedVolumeSource{Sources: []corev1.VolumeProjection{{ServiceAccountToken: &corev1.ServiceAccountTokenProjection{Audience: "mcp-runtime-vault", ExpirationSeconds: &expiry, Path: "token"}}}}}}
		found := false
		for _, existing := range deployment.Spec.Template.Spec.Volumes {
			if existing.Name == volumeName {
				if !reflect.DeepEqual(existing.VolumeSource, volume.VolumeSource) {
					return errors.New("existing Vault identity volume differs; refusing replacement")
				}
				found = true
			}
		}
		if !found {
			deployment.Spec.Template.Spec.Volumes = append(deployment.Spec.Template.Spec.Volumes, volume)
		}
		mount := corev1.VolumeMount{Name: volumeName, MountPath: "/var/run/mcp-runtime/vault", ReadOnly: true}
		found = false
		for _, existing := range target.VolumeMounts {
			if existing.Name == volumeName {
				if !reflect.DeepEqual(existing, mount) {
					return errors.New("existing Vault identity mount differs; refusing replacement")
				}
				found = true
			}
		}
		if !found {
			target.VolumeMounts = append(target.VolumeMounts, mount)
		}
		_, err = kube.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
}

func configureOperator(ctx context.Context, kube kubernetes.Interface, origin string) error {
	// Feature enablement cannot send workload credentials behind old routes.
	// Refreshing setup with the original options installs the shipped contract.
	ingress, err := kube.NetworkingV1().Ingresses("mcp-platform").Get(ctx, "mcp-platform-gateway-api", metav1.GetOptions{})
	if err != nil {
		return errors.New("credential routes unavailable; refresh setup with this release and the original setup options before configuring the broker")
	}
	found := map[string]bool{}
	for _, rule := range ingress.Spec.Rules {
		if rule.HTTP == nil {
			continue
		}
		for _, path := range rule.HTTP.Paths {
			if path.Backend.Service != nil && path.Backend.Service.Name == "mcp-platform-api" && path.Backend.Service.Port.Number == 8080 {
				if (path.Path == cb.ResolvePath && path.PathType != nil && string(*path.PathType) == "Exact") || (path.Path == cb.ManagementPath && path.PathType != nil && string(*path.PathType) == "Prefix") {
					found[path.Path] = true
				}
			}
		}
	}
	if !found[cb.ResolvePath] || !found[cb.ManagementPath] {
		return errors.New("credential routes have not migrated; refresh setup with this release and the original setup options before configuring the broker")
	}
	role, err := kube.RbacV1().ClusterRoles().Get(ctx, "mcp-platform-api-gateway-identity", metav1.GetOptions{})
	if err != nil {
		return errors.New("credential broker permissions unavailable; refresh setup with this release and the original setup options")
	}
	grantRead := false
	for _, rule := range role.Rules {
		if reflect.DeepEqual(rule.APIGroups, []string{"mcpruntime.org"}) {
			for _, resource := range rule.Resources {
				for _, verb := range rule.Verbs {
					if resource == "mcpaccessgrants" && verb == "get" && len(rule.ResourceNames) == 0 {
						grantRead = true
					}
				}
			}
		}
	}
	if !grantRead {
		return errors.New("credential broker grant-read permission unavailable; refresh setup with this release and the original setup options")
	}
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		deployment, err := kube.AppsV1().Deployments("mcp-runtime").Get(ctx, "mcp-runtime-operator-controller-manager", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if !setOperatorOrigin(deployment, origin) {
			return errors.New("operator manager container unavailable")
		}
		_, err = kube.AppsV1().Deployments(deployment.Namespace).Update(ctx, deployment, metav1.UpdateOptions{})
		return err
	})
}

func setOperatorOrigin(deployment *appsv1.Deployment, origin string) bool {
	for i := range deployment.Spec.Template.Spec.Containers {
		container := &deployment.Spec.Template.Spec.Containers[i]
		if container.Name != "manager" {
			continue
		}
		for j := range container.Env {
			if container.Env[j].Name == "MCP_GATEWAY_CREDENTIAL_BROKER_URL" {
				container.Env[j] = corev1.EnvVar{Name: "MCP_GATEWAY_CREDENTIAL_BROKER_URL", Value: origin}
				return true
			}
		}
		container.Env = append(container.Env, corev1.EnvVar{Name: "MCP_GATEWAY_CREDENTIAL_BROKER_URL", Value: origin})
		return true
	}
	return false
}
