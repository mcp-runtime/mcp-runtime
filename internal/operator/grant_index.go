package operator

import (
	"context"
	"fmt"
	"strings"

	"k8s.io/apimachinery/pkg/types"
	mcpv1alpha1 "mcp-runtime/api/v1alpha1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const grantTargetServerIndex = "mcpruntime.org/effective-target-server"

func effectiveServerReference(objectNamespace string, ref mcpv1alpha1.ServerReference) types.NamespacedName {
	namespace := strings.TrimSpace(ref.Namespace)
	if namespace == "" {
		namespace = objectNamespace
	}
	return types.NamespacedName{Namespace: namespace, Name: ref.Name}
}

func targetServerKey(namespace, name string) string {
	return (types.NamespacedName{Namespace: namespace, Name: name}).String()
}

func grantTargetServerKeys(obj client.Object) []string {
	grant := obj.(*mcpv1alpha1.MCPAccessGrant)
	if grant.Spec.ServerRef.Name == "" {
		return nil
	}
	ref := effectiveServerReference(grant.Namespace, grant.Spec.ServerRef)
	return []string{targetServerKey(ref.Namespace, ref.Name)}
}

func registerGrantTargetServerIndex(ctx context.Context, indexer client.FieldIndexer) error {
	if err := indexer.IndexField(ctx, &mcpv1alpha1.MCPAccessGrant{}, grantTargetServerIndex, grantTargetServerKeys); err != nil {
		return fmt.Errorf("register access grant target-server index: %w", err)
	}
	return nil
}
