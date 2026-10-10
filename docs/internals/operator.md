# Operator Internals

The operator turns `MCPServer` custom resources into Kubernetes workloads and
status. The binary entrypoint lives in `cmd/operator`; reconciliation logic lives
in `internal/operator`.

Useful reference commands:

```bash
go doc -cmd ./cmd/operator
go doc -all ./internal/operator
```

## Operator Binary

`cmd/operator` owns process startup:

- registers Kubernetes core types and MCP Runtime API types into the scheme
- parses manager flags for metrics, health probes, and leader election
- configures controller-runtime logging
- creates the manager
- registers the `MCPServerReconciler`
- registers admission webhooks when `MCP_ENABLE_WEBHOOKS` is enabled
- installs health and readiness probes
- starts the manager with signal handling

Keep this package focused on process wiring. Reconciliation behavior belongs in
`internal/operator`.

## Reconciliation Model

`MCPServerReconciler` follows a predictable loop:

1. Fetch the `MCPServer`.
2. Apply defaults to an in-memory copy for reconcile-time rendering.
3. Validate routing prerequisites.
4. Reconcile the Deployment.
5. Reconcile the Service.
6. Reconcile the Traefik egress NetworkPolicy.
7. Reconcile the Ingress.
8. Compute readiness.
9. Update status.
10. Requeue when resources are not ready.

The operator owns generated Kubernetes resources through owner references so
normal garbage collection cleans them up with the `MCPServer`. The exception is
the Traefik egress NetworkPolicy, which lives in the ingress controller
namespace and cannot carry a cross-namespace owner reference.

## Traefik Egress

The bundled Traefik runs under a default-deny NetworkPolicy, and its static
`traefik-allow-egress` policy reaches platform namespaces only. For each
`MCPServer` the operator manages a NetworkPolicy named
`mcp-egress-<namespace>-<name>-<hash>` in the ingress controller namespace
(`MCP_INGRESS_CONTROLLER_NAMESPACE`, default `traefik`). It selects the Traefik
pods (`MCP_INGRESS_CONTROLLER_POD_LABELS`, default `app=traefik`) and allows TCP
egress only to `app=<name>` pods in the server namespace, on the desired
serving port plus the port the Service currently targets. During a port
transition both ports stay open until the Service is promoted.

The policy carries `mcpruntime.org/component=traefik-egress`,
`mcpruntime.org/server`, and `mcpruntime.org/server-namespace` labels. The
reconciler deletes it when the server is gone or uses another ingress class,
and a leader-only startup sweep removes policies orphaned while the operator
was down. The operator creates the policy only when another NetworkPolicy
already restricts Traefik egress; adding an egress policy to an unrestricted
Traefik (such as k3s Traefik in `kube-system`) would isolate it.

Setup always passes the live Traefik identity (`MCP_INGRESS_CONTROLLER_*`) to
the operator, resolved the same way as `PLATFORM_TRAEFIK_NAMESPACE`: the
explicit env value, otherwise the active `traefik` Deployment (`traefik`
before `kube-system`). Each server reports a `TraefikEgressReady` condition
with reason `PolicyApplied`, `EgressUnrestricted`, or `NotTraefikIngress`.
When no pod matching the Traefik labels exists in the configured namespace,
the condition is `False` with reason `TraefikPodsNotFound`, the operator emits
a Warning event, and `mcp-runtime cluster doctor` fails the
`MCPServer Traefik egress` check.

## Defaults

Default values are intentionally centralized in `api/v1alpha1` so the admission
webhook and reconciler fallback share the same behavior. Current defaults
include:

| Setting | Default |
|---|---|
| replicas | `1` |
| server container port | `8088` |
| gateway sidecar port | `8091` |
| service port | `80` |
| ingress class | `traefik` |
| ingress path type | `Prefix` |
| ingress readiness mode | `strict` |
| CPU request | `50m` |
| memory request | `64Mi` |
| CPU limit | `500m` |
| memory limit | `256Mi` |

If you change a default, update API docs, examples, webhook/reconciler tests,
and any CLI metadata generation that emits the same field.

## Deployment Reconciliation

Deployment reconciliation resolves image references, labels, selectors, pod
template, env vars, resources, probes, pull secrets, and optional gateway
sidecars. Registry behavior is affected by:

- explicit `spec.image`
- `spec.imageTag`
- `spec.registryOverride`
- `spec.useProvisionedRegistry`
- operator environment for provisioned registry settings
- explicit `spec.imagePullSecrets`

Changes here need tests for both create and update paths. When image resolution
changes, also check setup, registry push, metadata generation, and e2e image pull
diagnostics.

## Service and Ingress Reconciliation

The Service exposes the desired service port and targets the container port. The
Ingress supports both host-based routing and hostless path-based routing. Public
path routing is important for local and shared gateway setups, so keep tests for:

- explicit `spec.ingressHost`
- `MCP_DEFAULT_INGRESS_HOST`
- `spec.publicPathPrefix`
- ingress class-specific annotations
- strict and permissive ingress readiness modes

Ingress readiness defaults to strict mode, which requires
`Ingress.status.loadBalancer.ingress[]`. Set operator env
`MCP_INGRESS_READINESS_MODE=permissive` for local port-forward or NodePort-style
setups where traffic works but the ingress controller does not publish load
balancer status.

## Status Contract

Status is the operator's observed state. It should be useful to both humans and
automation:

- `phase` summarizes current state.
- `message` explains what is not ready.
- readiness booleans identify which reconciled surface is ready.
- conditions should use stable types/reasons when possible.

Avoid reporting success until the owned Kubernetes resources are actually
observable and ready.

## Tests

Primary tests live in `internal/operator`. Use fake clients for fast unit
coverage and envtest/integration tests when API server behavior matters.

Run:

```bash
go test ./internal/operator/... -race -count=1
go test ./test/integration/... -count=1
```

The disposable Kind smoke-auth scenario also injects a missing gateway image
while changing its port. It checks the persisted Service route, pending status,
successful MCP calls before and after an operator restart, and promotion after
a fixed candidate becomes ready (`test/e2e/scenarios/port-transition.sh`).
