# Collector admission cutover

The node log collector requires hostPath access and runs in `mcp-log-collector`.
Application workloads in `mcp-platform` and `mcp-observability` use restricted Pod Security. The namespace
policy declarations live in `k8s/00-namespace.yaml`.

Supported setup ensures the Sentinel namespace exists before ingress installs
its RoleBindings, even when analytics is disabled. A new namespace starts
restricted. An existing namespace keeps its admission labels while the legacy
collector may still restart. Applying the namespace bundle during analytics
setup defers the Sentinel policy declaration until cutover finishes.

The destination collector uses its own `promtail-node-log-collector`
ClusterRoleBinding. Setup preserves the legacy `promtail` binding through failed
or interrupted rollout checks. The Loki URL uses `loki.mcp-observability.svc`, which
resolves through Kubernetes search domains without assuming `cluster.local`.

After all required workloads roll out, setup deletes the old DaemonSet with
foreground propagation and waits for its deletion, removes the old collector's
namespace-local resources, validates/removes its legacy binding, then tightens
Sentinel admission. Retries tolerate already-removed objects. An unexpected
legacy binding stops cleanup rather than deleting custom authority.

These checks establish resource ordering. They do not demonstrate Loki delivery,
log cursor continuity, duplicate handling or rollback. Those require disposable
cluster tests and remain merge gates. Apply the namespace bundle directly only
for a fresh installation; its restricted declaration alone is not an upgrade
or migration procedure.

This PR depends on the reserved-namespace provisioning changes in #550 and on
the shared-inventory integration from #553. The latter needs an explicit layout
record for the intermediate collector placement before this cutover is enabled.
Diagnostics, backup/retention and cleanup must cover the new namespace, and all
supported application, optional and helper pods must pass restricted admission.
