package cluster

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"

	"github.com/spf13/cobra"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"

	"mcp-runtime/internal/cli/setup/assetpath"
)

const operatorRoleName = "mcp-runtime-operator-role"

// newOperatorRBACCmd repairs product-owned operator permissions without
// re-running setup or changing workload images and configuration.
func newOperatorRBACCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "operator-rbac", Short: "Reconcile operator permissions from this release"}
	var kubeconfig, kubeContext string
	var dryRun, yes bool
	apply := &cobra.Command{
		Use:   "apply",
		Short: "Apply the shipped operator ClusterRole and optional MCP Auth binding",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if kubeconfig == "" || kubeContext == "" {
				return errors.New("--kubeconfig and --context are required")
			}
			if !dryRun && !yes {
				return errors.New("pass --yes to update operator RBAC")
			}
			rules := clientcmd.NewDefaultClientConfigLoadingRules()
			rules.ExplicitPath = kubeconfig
			cfg := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{CurrentContext: kubeContext})
			rest, err := cfg.ClientConfig()
			if err != nil {
				return fmt.Errorf("load selected Kubernetes context: %w", err)
			}
			client, err := kubernetes.NewForConfig(rest)
			if err != nil {
				return fmt.Errorf("connect to Kubernetes: %w", err)
			}
			changes, err := reconcileOperatorRBAC(cmd.Context(), client, dryRun)
			if err != nil {
				return err
			}
			for _, change := range changes {
				fmt.Fprintln(cmd.OutOrStdout(), change)
			}
			return nil
		},
	}
	apply.Flags().StringVar(&kubeconfig, "kubeconfig", "", "Explicit kubeconfig path")
	apply.Flags().StringVar(&kubeContext, "context", "", "Explicit Kubernetes context")
	apply.Flags().BoolVar(&dryRun, "dry-run", false, "Show changes without writing them")
	apply.Flags().BoolVar(&yes, "yes", false, "Apply the changes without an interactive prompt")
	cmd.AddCommand(apply)
	return cmd
}

func loadOperatorRBAC() (*rbacv1.ClusterRole, *rbacv1.Role, *rbacv1.RoleBinding, error) {
	rolePath, err := assetpath.ResolveRepoAssetPath("config/rbac/role.yaml")
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err := os.ReadFile(rolePath) // #nosec G304 -- resolved shipped repository asset.
	if err != nil {
		return nil, nil, nil, err
	}
	var clusterRole rbacv1.ClusterRole
	if err := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096).Decode(&clusterRole); err != nil {
		return nil, nil, nil, fmt.Errorf("decode operator ClusterRole: %w", err)
	}
	if clusterRole.Kind != "ClusterRole" || clusterRole.Name != operatorRoleName {
		return nil, nil, nil, errors.New("shipped operator ClusterRole has an unexpected identity")
	}
	authPath, err := assetpath.ResolveRepoAssetPath("k8s/23-mcp-auth-rbac.yaml")
	if err != nil {
		return nil, nil, nil, err
	}
	raw, err = os.ReadFile(authPath) // #nosec G304 -- resolved shipped repository asset.
	if err != nil {
		return nil, nil, nil, err
	}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(raw), 4096)
	var authRole rbacv1.Role
	if err := decoder.Decode(&authRole); err != nil {
		return nil, nil, nil, fmt.Errorf("decode MCP Auth Role: %w", err)
	}
	var authBinding rbacv1.RoleBinding
	if err := decoder.Decode(&authBinding); err != nil {
		return nil, nil, nil, fmt.Errorf("decode MCP Auth RoleBinding: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, nil, nil, errors.New("MCP Auth RBAC contains extra documents")
	}
	if authRole.Kind != "Role" || authRole.Namespace != mcpAuthNamespace ||
		authRole.Name != "mcp-runtime-operator-bundled-auth" ||
		authBinding.Kind != "RoleBinding" || authBinding.Namespace != authRole.Namespace ||
		authBinding.Name != authRole.Name || authBinding.RoleRef.Kind != "Role" ||
		authBinding.RoleRef.Name != authRole.Name {
		return nil, nil, nil, errors.New("shipped MCP Auth RBAC has an unexpected identity")
	}
	return &clusterRole, &authRole, &authBinding, nil
}

func reconcileOperatorRBAC(ctx context.Context, client kubernetes.Interface, dryRun bool) ([]string, error) {
	desiredClusterRole, desiredAuthRole, desiredAuthBinding, err := loadOperatorRBAC()
	if err != nil {
		return nil, err
	}
	changes := []string{}
	clusterRoleAction := "unchanged"
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.RbacV1().ClusterRoles().Get(ctx, desiredClusterRole.Name, metav1.GetOptions{})
		if err != nil {
			return fmt.Errorf("read existing operator ClusterRole: %w", err)
		}
		if reflect.DeepEqual(current.Rules, desiredClusterRole.Rules) {
			return nil
		}
		if dryRun {
			clusterRoleAction = "would update"
			return nil
		}
		updated := current.DeepCopy()
		updated.Rules = desiredClusterRole.Rules
		_, err = client.RbacV1().ClusterRoles().Update(ctx, updated, metav1.UpdateOptions{})
		if err == nil {
			clusterRoleAction = "updated"
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile operator ClusterRole: %w", err)
	}
	changes = append(changes, fmt.Sprintf("%s ClusterRole/%s", clusterRoleAction, desiredClusterRole.Name))
	if _, err := client.AppsV1().Deployments(mcpAuthNamespace).Get(ctx, "mcp-auth-server", metav1.GetOptions{}); apierrors.IsNotFound(err) {
		return append(changes, "bundled MCP Auth is absent; its optional Role was skipped"), nil
	} else if err != nil {
		return nil, fmt.Errorf("read bundled MCP Auth Deployment: %w", err)
	}
	authRoleAction := "unchanged"
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.RbacV1().Roles(mcpAuthNamespace).Get(ctx, desiredAuthRole.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if dryRun {
				authRoleAction = "would create"
				return nil
			}
			_, err = client.RbacV1().Roles(mcpAuthNamespace).Create(ctx, desiredAuthRole, metav1.CreateOptions{})
			if err == nil {
				authRoleAction = "created"
			}
			return err
		}
		if err != nil {
			return err
		}
		if reflect.DeepEqual(current.Rules, desiredAuthRole.Rules) {
			return nil
		}
		if dryRun {
			authRoleAction = "would update"
			return nil
		}
		updated := current.DeepCopy()
		updated.Rules = desiredAuthRole.Rules
		_, err = client.RbacV1().Roles(mcpAuthNamespace).Update(ctx, updated, metav1.UpdateOptions{})
		if err == nil {
			authRoleAction = "updated"
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile bundled MCP Auth Role: %w", err)
	}
	changes = append(changes, fmt.Sprintf("%s Role/%s/%s", authRoleAction, mcpAuthNamespace, desiredAuthRole.Name))
	authBindingAction := "unchanged"
	err = retry.RetryOnConflict(retry.DefaultRetry, func() error {
		current, err := client.RbacV1().RoleBindings(mcpAuthNamespace).Get(ctx, desiredAuthBinding.Name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if dryRun {
				authBindingAction = "would create"
				return nil
			}
			_, err = client.RbacV1().RoleBindings(mcpAuthNamespace).Create(ctx, desiredAuthBinding, metav1.CreateOptions{})
			if err == nil {
				authBindingAction = "created"
			}
			return err
		}
		if err != nil {
			return err
		}
		if current.RoleRef != desiredAuthBinding.RoleRef {
			return errors.New("existing MCP Auth RoleBinding has a conflicting roleRef")
		}
		if reflect.DeepEqual(current.Subjects, desiredAuthBinding.Subjects) {
			return nil
		}
		if dryRun {
			authBindingAction = "would update"
			return nil
		}
		updated := current.DeepCopy()
		updated.Subjects = desiredAuthBinding.Subjects
		_, err = client.RbacV1().RoleBindings(mcpAuthNamespace).Update(ctx, updated, metav1.UpdateOptions{})
		if err == nil {
			authBindingAction = "updated"
		}
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("reconcile bundled MCP Auth RoleBinding: %w", err)
	}
	return append(changes, fmt.Sprintf("%s RoleBinding/%s/%s", authBindingAction, mcpAuthNamespace, desiredAuthBinding.Name)), nil
}
