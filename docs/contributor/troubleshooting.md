# Contributor Troubleshooting

Start from the symptom and inspect the narrowest surface first.

## Anonymous users can see MCPs

Expected behavior:

```bash
curl -i http://localhost:18080/api/v1/runtime/servers
```

The response should be `401 Unauthorized`.

If it returns servers:

1. Confirm the UI and API Deployments are on the current images.

   ```bash
   kubectl get deploy mcp-platform-api mcp-runtime-api mcp-ui -n mcp-platform \
   kubectl get deploy mcp-analytics-api -n mcp-observability \
     -o jsonpath='{range .items[*]}{.metadata.name}{" "}{.spec.template.spec.containers[0].image}{"\n"}{end}'
   ```

2. Roll both Deployments after patching auth or catalog code.

   ```bash
   kubectl rollout status deployment/mcp-platform-api -n mcp-platform --timeout=90s
   kubectl rollout status deployment/mcp-runtime-api -n mcp-platform --timeout=90s
   kubectl rollout status deployment/mcp-ui -n mcp-platform --timeout=90s
   ```

3. Sign out in the browser or use a fresh private window. A valid UI session
   cookie makes the same URL authenticated.

## Tenant user sees the wrong MCPs

Check the API path first:

```bash
curl -sS -b /tmp/mcp-tenant-a-cookie.txt \
  http://localhost:18080/api/v1/runtime/servers |
  jq '[.servers[] | {namespace,name,team_id}]'
```

Then compare Kubernetes state:

```bash
kubectl get mcpservers -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,TEAM:.spec.teamID'
```

Rules to verify:

- Single-team/example MCPs in this guide live in `mcp-servers`.
- Org-mode and public-mode catalogs live in `mcp-servers-org` and
  `mcp-servers-public`; tenant MCPs live in `mcp-team-<slug>` namespaces with
  matching ownership.
- Non-admin users should receive `403` when explicitly requesting another
  tenant namespace.
- Direct `kubectl get mcpservers -A` is not an authz check; it shows what your
  kubeconfig can read.

## `tools[].sideEffect` Is Required

Every listed tool must declare a side effect:

```yaml
tools:
  - name: add
    requiredTrust: low
    sideEffect: read
```

If the operator logs validation errors such as
`spec.tools[0].sideEffect: Required value`, check for CRD/operator skew and old
test objects:

```bash
kubectl apply -f config/crd/bases/mcpruntime.org_mcpservers.yaml
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager --since=10m
kubectl get mcpservers -A -o yaml | rg -n "name:|sideEffect"
```

Patch old local test objects or redeploy them from current metadata.

## Image pulls fail

For Kind test mode, pod images should use:

```text
registry.registry.svc.cluster.local:5000/<image>:<tag>
```

The Kind cluster must have a containerd mirror for that exact host:

```toml
[plugins."io.containerd.grpc.v1.cri".registry.mirrors."registry.registry.svc.cluster.local:5000"]
  endpoint = ["http://127.0.0.1:32000"]
```

Useful checks:

```bash
kubectl describe pod -n <namespace> <pod>
kubectl get events -n <namespace> --sort-by=.lastTimestamp
./bin/mcp-runtime cluster doctor
```

If events say `http: server gave HTTP response to HTTPS client`, the node tried
HTTPS against the plain HTTP dev registry. Recreate the Kind cluster with the
mirror or configure the node runtime for the exact image host.

## Host cannot push to cluster DNS registry

This is expected:

```bash
docker push registry.registry.svc.cluster.local:5000/example:dev
```

The host usually cannot resolve Kubernetes service DNS. Use the CLI helper:

```bash
./bin/mcp-runtime auth login --api-url http://localhost:18080

./bin/mcp-runtime server push \
  --image example:dev \
  --name example
```

For one-node Kind debug loops, `kind load docker-image` is also valid when the
Deployment image name matches the loaded tag.

## Dashboard or API Returns 404

Check the Traefik port-forward and ingress resources:

```bash
lsof -nP -iTCP:18080 -sTCP:LISTEN
kubectl get ingress -A
kubectl logs -n traefik deploy/traefik --tail=120
```

The local dashboard should be reachable at:

```text
http://localhost:18080/
```

## Browser login fails but direct API works

The HTTP ingress overlay can include the `pii-redactor@file` Traefik middleware.
That middleware is useful for request-path testing on ingest traffic, but it
must not be attached to control-plane `/api/v1` routes because API keys, team IDs,
server names, namespaces, and grant/session subjects must stay exact. If local
API responses show `[redacted]`, verify that `mcp-platform-gateway-api` does not
reference `pii-redactor@file`. For identity-store debugging, port-forward the
platform API directly:

```bash
kubectl port-forward -n mcp-platform svc/mcp-platform-api 18081:8080
```

Then use `http://localhost:18081` for direct API debugging and stop the
port-forward afterward.

## Operator is reconciling but routes do not work

Check each layer in order:

```bash
kubectl get mcpserver <server-name> -n <namespace> -o yaml
kubectl get deploy,svc,ingress -n <namespace> | rg '<server-name>|NAME'
kubectl describe pod -n <namespace> -l app=<server-name>
kubectl logs -n <namespace> deploy/<server-name> -c mcp-gateway --tail=120
kubectl logs -n traefik deploy/traefik --tail=120
```

In local Kind, `MCPServer.status.phase` can stay `PartiallyReady` even when the
Deployment is ready and traffic works, because strict ingress readiness waits
for load balancer status. Use the Deployment, Service, Ingress, and actual
traffic checks to decide whether local routing works.

## Grant or session does not affect traffic

Use the platform API path first:

```bash
./bin/mcp-runtime auth login --api-url http://localhost:18080
./bin/mcp-runtime access grant list --namespace mcp-servers
./bin/mcp-runtime access session list --namespace mcp-servers
./bin/mcp-runtime server policy inspect <server-name> --namespace mcp-servers
```

Allow a few seconds after apply; the gateway sidecar reloads rendered policy on
a short polling loop. If policy looks correct but calls still fail, check
gateway logs, the `Mcp-Session-Id` transport header, OAuth claims, and the
verified adapter SPIFFE identity.

Admin/operator fallback:

```bash
kubectl get mcpaccessgrant,mcpagentsession -n <namespace> -o wide
./bin/mcp-runtime server policy inspect <server-name> --namespace <namespace> --use-kube
kubectl get cm -n <namespace> <server-name>-gateway-policy -o yaml
```
