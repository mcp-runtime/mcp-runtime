# Service Iteration

After `setup --test-mode` succeeds, do not rerun full setup for every code
change. Run the targeted tests, rebuild the image for the changed component, and
roll only that Deployment.

## Test First

Keep tests isolated from the live cluster. Run test commands in a subshell
with an empty kubeconfig; restore the contributor kubeconfig only for the
explicit local-cluster steps below.

Use focused tests while iterating:

```bash
(
export KUBECONFIG=$(mktemp)
trap 'rm -f "$KUBECONFIG"' EXIT
go test ./internal/operator/... ./internal/cli/... -count=1
go test ./internal/agentadapter -count=1
(cd services/platform-api && go test ./... -count=1)
(cd services/runtime-api && go test ./... -count=1)
(cd services/analytics-api && go test ./... -count=1)
(cd services/ui && go test ./... -count=1)
(cd services/ui/frontend && npm run build && npm run test)
)
```

Run wider checks before handing off a broad change:

```bash
(
export KUBECONFIG=$(mktemp)
trap 'rm -f "$KUBECONFIG"' EXIT
gofmt -s -l .
go vet ./...
go test ./... -count=1
git diff --check
)
```

## API and UI

The following workload-image mutations are for a disposable contributor Kind
cluster only. They test internal platform components, not the user-facing MCP
server deployment path. Use the isolated kubeconfig from
[Local Kind and Test Mode](local-kind.md) and verify `kubectl get nodes` before
continuing. Use `mcp-runtime update` for release rollouts on retained clusters.

API and UI changes often need coordinated rollout when browser flows depend on
new `/api/v1/*` behavior. Traefik routes API traffic directly; the UI serves
static assets, auth sessions, and an allowlisted read-only session BFF.

The UI service remains a Go backend for `/config.js`, `/auth/*`, API proxying,
security headers, and static asset embedding. React, Vite, and TypeScript source
lives under `services/ui/frontend`; Vite writes the browser bundle into
`services/ui/static` for the Go embed step. During the migration, the current
dashboard is shipped from `frontend/public/legacy` and mounted by the React
shell, so migrate tabs into React one at a time.

Build and roll the UI:

```bash
SERVICE=ui
IMAGE_REPO=mcp-ui
DOCKERFILE=services/ui/Dockerfile
BUILD_CONTEXT=.
DEPLOYMENT=mcp-ui
CONTAINER=ui
TAG="${SERVICE}-dev-$(date +%s)"
LOCAL_IMAGE="${IMAGE_REPO}:${TAG}"
REGISTRY=registry.registry.svc.cluster.local:5000

docker build -t "$LOCAL_IMAGE" -f "$DOCKERFILE" "$BUILD_CONTEXT"

./bin/mcp-runtime auth login --api-url http://localhost:18080

./bin/mcp-runtime server push \
  --image "$LOCAL_IMAGE" \
  --name "$IMAGE_REPO"

kubectl -n mcp-platform set image \
  "deployment/$DEPLOYMENT" \
  "$CONTAINER=$REGISTRY/$IMAGE_REPO:$TAG"

kubectl -n mcp-platform rollout status "deployment/$DEPLOYMENT" --timeout=90s
```

Use the same shape for each split API service:

| Service | `IMAGE_REPO` | `DOCKERFILE` | `DEPLOYMENT` | `CONTAINER` | Port |
|---|---|---|---|---|---|
| platform-api | `mcp-platform-api` | `services/platform-api/Dockerfile` | `mcp-platform-api` | `platform-api` | 8080 |
| runtime-api | `mcp-runtime-api` | `services/runtime-api/Dockerfile` | `mcp-runtime-api` | `runtime-api` | 8084 |
| analytics-api | `mcp-analytics-api` | `services/analytics-api/Dockerfile` | `mcp-analytics-api` | `analytics-api` | 8085 |

The UI, platform-api, and runtime-api run in `mcp-platform`; analytics-api
runs in `mcp-observability`. Change the namespace in both `kubectl` commands
when rolling analytics-api.

Set `BUILD_CONTEXT=.` and pick a unique `TAG` per build. Example for platform-api:

```bash
SERVICE=platform-api
IMAGE_REPO=mcp-platform-api
DOCKERFILE=services/platform-api/Dockerfile
DEPLOYMENT=mcp-platform-api
CONTAINER=platform-api
```

Use a new tag for every build. Reusing `latest` with `IfNotPresent` can leave
old images cached on the node.

## Ingest and Processor

For analytics pipeline changes:

| Service | Image repo | Dockerfile | Build context | Deployment | Container |
|---|---|---|---|---|---|
| Ingest | `mcp-ingest` | `services/ingest/Dockerfile` | `.` | `mcp-ingest` | `ingest` |
| Processor | `mcp-processor` | `services/processor/Dockerfile` | `.` | `mcp-processor` | `processor` |

After rolling either service, generate one MCP request and check both logs
(admin kubectl):

```bash
./bin/mcp-runtime ops logs ingest --since 10m
./bin/mcp-runtime ops logs processor --since 10m
./bin/mcp-runtime ops events
```

## Operator

Operator changes affect how `MCPServer`, `MCPAccessGrant`, and
`MCPAgentSession` objects reconcile. Run the operator tests first:

```bash
(
  export KUBECONFIG=$(mktemp)
  trap 'rm -f "$KUBECONFIG"' EXIT
  go test ./internal/operator/... -count=1
)
```

For a local Kind-only debug build, build an image, load it into the Kind node,
and roll the operator:

```bash
TAG="operator-dev-$(date +%s)"
IMAGE="registry.registry.svc.cluster.local:5000/mcp-runtime-operator:${TAG}"

DOCKER_BUILDKIT=0 docker build --platform=linux/$(go env GOARCH) \
  -t "$IMAGE" \
  -f Dockerfile.operator .

kind load docker-image "$IMAGE" --name mcp-runtime

kubectl patch deployment/mcp-runtime-operator-controller-manager \
  -n mcp-runtime \
  --type=json \
  -p='[{"op":"replace","path":"/spec/template/spec/containers/0/imagePullPolicy","value":"IfNotPresent"}]'

kubectl set image deployment/mcp-runtime-operator-controller-manager \
  -n mcp-runtime \
  manager="$IMAGE"

kubectl rollout status deployment/mcp-runtime-operator-controller-manager \
  -n mcp-runtime \
  --timeout=180s
```

Then watch reconciliation:

```bash
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager --since=10m
kubectl get mcpservers -A
kubectl get deploy,svc,ingress -A | rg '<server-name>|mcp-runtime'
```

If you changed CRD fields, apply the CRD before expecting old controllers or
new controllers to agree on validation:

```bash
kubectl apply -f config/crd/bases/mcpruntime.org_mcpservers.yaml
```

## MCP gateway sidecar

`services/mcp-gateway` runs as the `mcp-gateway` sidecar in each MCP server pod.
To test gateway changes, rebuild and push `mcp-gateway`, update the
operator's `MCP_GATEWAY_PROXY_IMAGE`, roll the operator, then restart affected
MCP server pods so the sidecar is reinjected.

Check the gateway sidecar:

```bash
POD="$(kubectl get pods -n <namespace> -l app=<server-name> -o jsonpath='{.items[0].metadata.name}')"
kubectl logs -n <namespace> "$POD" -c mcp-gateway
kubectl describe pod -n <namespace> "$POD"
```

## Registry Notes

Inside Kubernetes, image references use
`registry.registry.svc.cluster.local:5000`. Your host usually cannot resolve
that DNS name. Prefer `mcp-runtime server push`, which uses an in-cluster
helper after platform login, or `kind load docker-image` for single-node Kind
debugging.

Raw `docker push registry.registry.svc.cluster.local:5000/...` from the host is
expected to fail unless you have added host DNS and insecure registry settings.
