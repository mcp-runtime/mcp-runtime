# MCPServer port transitions

The Service retains its existing target port and selector until a Ready stable
candidate pod declares the requested listener. The Deployment uses a rolling
update with zero unavailable replicas and one surge replica while its serving
port differs from the existing Service. This also overrides an explicit Recreate
strategy for that transition. If the cluster lacks surge capacity, the transition
waits and retains the old route.

On promotion, the Service adds `mcpruntime.org/serving-port` to its selector,
excluding old revisions with a different listener. The operator stamps the label
on stable and canary templates; the immutable Deployment selector stays the same.
An existing Service with an unchanged numeric port keeps its legacy selector.
The Service itself records the old route, so an operator restart does not lose it.

With adapter certificates enabled, the gateway NetworkPolicy permits Traefik
to reach the named `gateway` port on each pod. Kubernetes resolves that name
to each pod's listener, allowing both the retained and candidate ports during
the rollout while keeping ingress restricted to the controller.

Deployment readiness requires the observed generation and all desired replicas
updated, Ready, and available with no overlapping old replicas. Service readiness
requires its desired target port and a Ready, non-terminating EndpointSlice
endpoint for that port. A retained route during a failed transition is therefore
reported PartiallyReady, rather than Ready for the failed candidate.

The operator needs read-only EndpointSlice permissions. Apply the updated
operator RBAC with the operator upgrade. This change does not undo a Service
already switched to an unusable port before the upgrade, and cannot retain a
route for zero replicas or when old pods independently fail. Live failed-rollout
traffic checks in disposable Staging E2E are still required before closing #533.
