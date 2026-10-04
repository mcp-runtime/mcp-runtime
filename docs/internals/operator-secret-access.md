# Operator Secret access

The cluster-bound `mcp-runtime-operator-role` has no Secret rules. The separately
maintained `mcp-runtime-operator-managed-secrets` ClusterRole grants direct
get/create/update/patch/delete and is bound by RoleBindings in managed MCPServer
namespaces. Secret reads bypass the controller-runtime cache; no Secret list or
watch permission is needed. Keep the scoped role separate from generated RBAC.

Supported setup backfills existing server namespaces and platform-managed empty
namespaces before removing the old cluster-wide Secret rule. Runtime API
provisioning grants access for new managed namespaces. Infrastructure namespaces,
including the platform, observability, and collector namespaces, cannot receive
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

Indirect routes are scoped the same way. Deployment, ServiceAccount and
Certificate create/update/patch/delete (the ways to mount or issue into a Secret)
are granted by the `mcp-runtime-operator-managed-workloads` ClusterRole, bound only
through the same namespace-local RoleBindings. The cluster-bound role keeps
get/list/watch on them for informers. The operator therefore cannot create a
Deployment in `cert-manager` to mount the workload CA key, nor read it directly.
Runtime API holds `bind` on the role for provisioning.

Remaining gaps: other mutating verbs (Services, ConfigMaps, Ingresses,
NetworkPolicies, Traefik resources, `pods` patch) stay cluster-wide, and
real API-server authorization (`kubectl auth can-i` for
`cert-manager/mcp-runtime-ca`) plus Staging E2E have not been exercised.
Do not treat this change as closing the CA-key isolation issue until that is done.
