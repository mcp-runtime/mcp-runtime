# Operator Secret access

The cluster-bound `mcp-runtime-operator-role` has no Secret rules. The separately
maintained `mcp-runtime-operator-managed-secrets` ClusterRole grants direct
get/create/update/patch/delete and is bound by RoleBindings in managed MCPServer
namespaces. Secret reads bypass the controller-runtime cache; no Secret list or
watch permission is needed. Keep the scoped role separate from generated RBAC.

Supported setup backfills existing server namespaces and platform-managed empty
namespaces before removing the old cluster-wide Secret rule. Runtime API
provisioning grants access for new managed namespaces. Infrastructure namespaces,
including the proposed platform/observability/collector domains, cannot receive
this tenant binding. The direct installation bundle creates the default
`mcp-servers` namespace and its binding; administrators installing tenant
manifests directly must arrange the corresponding namespace-local binding.

If `MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE` is configured, setup preserves or
pre-creates `mcp-adapter-client-ca` there. This contains public trust material.
The operator gets named get/update/patch through a separate Role and RoleBinding.
The installer creates the object because Kubernetes cannot restrict Secret
create by `resourceNames`. Cert-manager, operator and Kubernetes system
namespaces cannot be configured for this trust bundle. Direct customized
installations must pre-provision the bundle and equivalent named RoleBinding;
removing the Secret requires rerunning setup before reconciliation can restore it.

This limits direct Secret API access. It does not establish full workload-CA
isolation while the operator retains cluster-wide Deployment, ServiceAccount
and Certificate mutation rights. Narrowing that indirect authority and testing
real API-server authorization remain prerequisites in the namespace refactor
plan. Do not treat this change as closing the CA-key isolation issue.
