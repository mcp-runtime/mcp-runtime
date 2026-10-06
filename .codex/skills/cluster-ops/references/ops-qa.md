
# Cluster Operations QA

## Overview

This skill validates **operations**: does the operator reconcile, does the
CLI behave, does `setup --test-mode` produce a working stack, do rollouts
land, do CRD updates converge, does `cluster doctor` stay green under change?
It runs against the **real** Kind cluster brought up by `contributor-cluster`,
not envtest. For code-level unit/golden/envtest checks the contributor should
run `go test` directly per `CLAUDE.md` — this skill exists to catch the
regressions those tests miss.

Regression evidence contract: every successful report must include live
`cluster doctor`, rollout/pod status, and the cached Kind `smoke-auth,governance`
traffic gate unless the diff is explicitly docs-only. If the live cluster or
traffic gate cannot run, mark the operations result **blocked**, not passed.

## Step 1 — Confirm precondition

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
kubectl --kubeconfig "$TEST_KUBECONFIG" config current-context \
  | grep -qx test-mcp-runtime \
  || { echo "Run contributor-cluster first"; exit 1; }
export KUBECONFIG="$TEST_KUBECONFIG"
./bin/mcp-runtime cluster doctor
```

If `cluster doctor` is not green, hand back to `contributor-cluster` rather
than firing checks at a broken cluster.

## Step 2 — Choose mode

- **head-only**. Run the full operational matrix below.
- **git-range** (`BASE=<merge-base>`, default `origin/main`). Reduce the
  matrix to the surfaces the diff touches.

Mode dispatch by changed paths:

| Diff touches | Required sub-suites |
|---|---|
| `internal/operator/**`, `api/v1alpha1/**`, `config/crd/**` | Operator, CRD reconciliation, ingress, generated-file drift |
| `cmd/operator/**`, `cmd/mcp-runtime/**`, `internal/cli/**` | CLI smoke, generated-file drift |
| `internal/cli/setup/**`, `pkg/k8sclient/**`, `pkg/manifest/**`, `pkg/metadata/**` | Setup re-run, registry pulls, ingress, rollout |
| `services/platform-api/**`, `services/runtime-api/**`, `services/analytics-api/**`, `services/ui/**`, `services/ingest/**`, `services/processor/**`, `services/mcp-gateway/**` | Service rollout matrix, gateway sidecar refresh |
| `k8s/**`, `config/**` | Manifest re-apply + rollout |
| `docs/**` only | Skip — not an operations regression surface |

Always include the **Baseline** suite below regardless of diff.

```bash
git diff --name-only "${BASE:-origin/main}"...HEAD
```

## Step 3 — CI parity gate (pre-merge / release readiness)

When the user asks for "all tests", "can this merge", or release readiness,
run the CI-equivalent non-cluster checks before the live cluster matrix. Do not
claim full regression coverage if any required CI parity check is skipped.

```bash
(
TEST_ISOLATION_KUBECONFIG="$(mktemp)"
trap 'rm -f "$TEST_ISOLATION_KUBECONFIG"' EXIT
export KUBECONFIG="$TEST_ISOLATION_KUBECONFIG"
gofmt -s -l .
go vet ./...
staticcheck ./...
go test -race -count=1 $(go list ./... | grep -v '/test/integration$')

for d in services/platform-api services/runtime-api services/analytics-api services/ingest services/processor services/mcp-gateway services/ui; do
  (cd "$d" && go test -race -count=1 ./...)
done

go test ./test/golden/... -count=1
go test -run=^$ -bench=. -benchmem -count=1 ./test/benchmark/...
bash test/e2e/scenarios_test.sh

export KUBEBUILDER_ASSETS="${KUBEBUILDER_ASSETS:-$(go run sigs.k8s.io/controller-runtime/tools/setup-envtest@v0.24.0 use -p path)}"
go test -race -timeout 30m -count=1 ./test/integration/...

make -f Makefile.operator generate manifests
git diff --exit-code
)
```

## Step 4 — Baseline (always run)

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster status
./bin/mcp-runtime registry status
./bin/mcp-runtime ops status
./bin/mcp-runtime cluster doctor
kubectl get pods -A --field-selector=status.phase!=Running,status.phase!=Succeeded
kubectl get events -A --sort-by=.lastTimestamp | tail -40
```

Then the contributor traffic gate (regression canary):

```bash
KUBECONFIG="$TEST_KUBECONFIG" E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1 \
  CLUSTER_NAME=mcp-runtime E2E_SCENARIOS=smoke-auth,governance \
  bash test/e2e/qa-e2e.sh
```

For merge readiness after non-doc code changes, also run or verify CI ran the
full Kind matrix:

```bash
KUBECONFIG="$TEST_KUBECONFIG" E2E_SCENARIOS=all bash test/e2e/qa-e2e.sh
```

Reusing the contributor cluster is intentional — `CLAUDE.md` documents that
`CLUSTER_NAME=mcp-runtime E2E_CACHE_MODE=1 E2E_KEEP_CLUSTER=1` avoids
creating a duplicate `mcp-e2e` cluster.

## Step 5 — Operator + CRD reconciliation

```bash
# Apply an MCPServer change and watch it converge.
kubectl get mcpservers -n mcp-servers -o wide
kubectl describe mcpserver -n mcp-servers oauth-example-go-2025-11-25-gateway | sed -n '/Status:/,$p'

# Force a reconcile and confirm Ready / phase transition.
kubectl annotate mcpserver -n mcp-servers oauth-example-go-2025-11-25-gateway \
  qa.mcpruntime.org/reconcile-ping="$(date +%s)" --overwrite
kubectl wait --for=condition=Ready=true mcpserver/oauth-example-go-2025-11-25-gateway \
  -n mcp-servers --timeout=120s \
  || kubectl describe mcpserver -n mcp-servers oauth-example-go-2025-11-25-gateway

# Operator log scan for reconcile errors (last 10m).
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager \
  --since=10m | grep -Ei 'error|panic|reconcile failed' || echo OK
```

When `internal/operator/**` or `api/v1alpha1/**` changed, also exercise the
governance objects:

```bash
kubectl apply -f /tmp/workspace-assistant-access.yaml
./bin/mcp-runtime server policy inspect oauth-example-go-2025-11-25-gateway --namespace mcp-servers \
  | grep -q local-session || echo "FAIL: policy missing session"
```

## Step 6 — CLI smoke + help drift

Real-cluster CLI behavior (the unit golden suite covers help text — this is
about behavior).

```bash
./bin/mcp-runtime server status --namespace mcp-servers
./bin/mcp-runtime server logs oauth-example-go-2025-11-25-gateway --namespace mcp-servers \
  --since 5m | head -40
./bin/mcp-runtime server policy inspect oauth-example-go-2025-11-25-gateway --namespace mcp-servers \
  | head -40
./bin/mcp-runtime ops events | head -20
./bin/mcp-runtime ops logs api --since 5m | tail -40

# Negative path: error UX should route through internal/cli/core/errors.go.
./bin/mcp-runtime server status --namespace bogus 2>&1 | head -5
./bin/mcp-runtime server logs nonexistent --namespace mcp-servers 2>&1 | head -5
```

After setup, confirm `daemonset/promtail` remains ready in
`mcp-log-collector` and that `ops events` includes
`mcp-observability` and `mcp-log-collector`. The shared ConfigMap's OTel and
Prometheus endpoints must resolve from both `mcp-platform` and
`mcp-observability`.

Any error that prints a bare Cobra usage dump (no `Error:` framing) is a
finding; route the report back to `internal/cli/core/errors.go` and
`pkg/errx/`.

## Step 7 — Setup / registry / ingress regression (only when those areas changed)

For user MCP server validation, follow
`docs/contributor/runtime-mcp-testing.md` instead of hand-writing an
`MCPServer` with placeholder images. Build the sample server with
`server build image`, then push the exact image ref written back into metadata:

```bash
./bin/mcp-runtime server build image oauth-example-go-2025-11-25-gateway \
  --metadata-file /tmp/oauth-example-go-2025-11-25-gateway.yaml \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 \
  --tag dev
IMAGE_REF="$(python3 - <<'PY'
image = tag = ""
with open('/tmp/oauth-example-go-2025-11-25-gateway.yaml') as f:
    for line in f:
        stripped = line.strip()
        if stripped.startswith("image: "):
            image = stripped.split(":", 1)[1].strip()
        elif stripped.startswith("imageTag: "):
            tag = stripped.split(":", 1)[1].strip()
if not image or not tag:
    raise SystemExit("metadata missing image/imageTag; rerun server build image")
print(f"{image}:{tag}")
PY
)"
./bin/mcp-runtime server push --image "$IMAGE_REF"
./bin/mcp-runtime server deploy oauth-example-go-2025-11-25-gateway \
  --scope tenant \
  --metadata-file /tmp/oauth-example-go-2025-11-25-gateway.yaml
```

If `server push` returns `504 Gateway Timeout`, treat that as a platform
registry-push failure and inspect runtime-api / Traefik logs; do not replace
the server with an unrelated image just to get a pod.

```bash
# Re-run setup in place (test-mode is idempotent; this catches drift).
MCP_SETUP_WAIT_TIMEOUT=900 \
  ./bin/mcp-runtime setup --test-mode \
  --ingress-manifest config/ingress/overlays/http

./bin/mcp-runtime cluster doctor
kubectl get ingress -A
curl -fsS -o /dev/null -w "%{http_code}\n" http://localhost:18080/    # 200
ADMIN_KEY=$(kubectl get secret mcp-platform-api-credentials -n mcp-platform -o jsonpath='{.data.ADMIN_API_KEYS}' | base64 -d | cut -d, -f1)
curl -fsS -o /dev/null -w "%{http_code}\n" \
  -H "x-api-key: $ADMIN_KEY" http://localhost:18080/api/v1/dashboard/summary  # 200
curl -fsS -o /dev/null -w "%{http_code}\n" http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp # 405/406 expected (POST-only)
```

If image pulls fail with `http: server gave HTTP response to HTTPS client`,
the Kind containerd mirror is missing — that is itself a regression in the
setup output, not a host issue.

## Step 8 — Service rollout matrix (when services/**/ changed)

For each touched platform service, follow the contributor iterate-on-one
loop from `docs/contributor/service-iteration.md` (split API table for
`platform-api`, `runtime-api`, and `analytics-api`):

```bash
# Example: platform-api (see service-iteration.md for runtime-api / analytics-api)
SERVICE=platform-api
IMAGE_REPO=mcp-platform-api
DOCKERFILE=services/platform-api/Dockerfile
BUILD_CONTEXT=.
DEPLOYMENT=mcp-platform-api
CONTAINER=platform-api
TAG="$SERVICE-qa-$(date +%s)"
LOCAL_IMAGE="$IMAGE_REPO:$TAG"
REGISTRY=registry.registry.svc.cluster.local:5000

docker build -t "$LOCAL_IMAGE" -f "$DOCKERFILE" "$BUILD_CONTEXT"
./bin/mcp-runtime admin registry push --image "$LOCAL_IMAGE" --name "$IMAGE_REPO" \
  --mode in-cluster --namespace registry
kubectl -n mcp-platform set image "deployment/$DEPLOYMENT" \
  "$CONTAINER=$REGISTRY/$IMAGE_REPO:$TAG"
kubectl -n mcp-platform rollout status "deployment/$DEPLOYMENT" --timeout=120s
```

For `services/mcp-gateway/**` changes, also update operator env
`MCP_GATEWAY_PROXY_IMAGE` and restart the operator (`CLAUDE.md` step under
**Iterate on one platform service**); then recreate the MCP server pod to
refresh the sidecar image.

## Step 9 — Generated-file drift

Drift is a behavioral regression even if unit tests pass.

```bash
make -f Makefile.operator generate manifests
git diff --exit-code api/ config/crd/bases/ || echo "FAIL: regen drift"
```

## Step 10 — Chaos canary (ship / pre-tag only)

Only when explicitly QAing ship or pre-tag operational resilience:

```bash
# Kill operator mid-reconcile.
kubectl -n mcp-runtime delete pod -l control-plane=controller-manager
kubectl -n mcp-runtime rollout status \
  deploy/mcp-runtime-operator-controller-manager --timeout=120s

# Bounce runtime-api mid grant-apply.
kubectl -n mcp-platform scale deploy/mcp-runtime-api --replicas=0
kubectl apply -f /tmp/workspace-assistant-access.yaml
kubectl -n mcp-platform scale deploy/mcp-runtime-api --replicas=1
kubectl -n mcp-platform rollout status deploy/mcp-runtime-api --timeout=90s
./bin/mcp-runtime server policy inspect oauth-example-go-2025-11-25-gateway --namespace mcp-servers \
  | grep -q local-session
```

## Step 11 — Report

Use the structure in `../_shared/FINDINGS-TEMPLATE.md`. One row per command:
pass/fail, duration when interesting, the failure line if it failed.

- Lead with mode (head-only | git-range BASE=<sha>), cluster context, commit
  SHA, and a one-line verdict.
- Separate scanner-style failures (rollout timeout, regen drift) from
  behavioral failures (CLI exit code, `tools/call` deny).
- Call out checks **skipped because the diff did not warrant them** — do
  not silently expand or contract the matrix.
- Call out checks **skipped because the environment is not ready** (cluster
  not up, port-forward dead) — those route back to `contributor-cluster`.
- Cross-link to `cluster-ops` / `dashboard-browser-qa` / `cluster-ops` when an
  operational finding suggests their surfaces are also implicated.
