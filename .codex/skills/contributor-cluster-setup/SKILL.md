---
name: contributor-cluster-setup
description: Stand up a real Kind+test-mode MCP Runtime cluster end-to-end (the contributor flow from docs/getting-started.md#3-contributor-test-mode-cluster) so the qa-e2e-* skills can run real validation. Use when Codex is asked to QA on a live cluster, prepare a target environment for cluster-operations-qa / security-regression-qa / dashboard-browser-qa / performance-regression-qa, recover a broken contributor cluster, or verify a fresh setup --test-mode end-to-end. Owns cluster lifecycle; the other QA skills assume this has run.
---

# Contributor Cluster Setup

## Overview

This skill provisions or recovers the **real** contributor cluster described in
`docs/getting-started.md#3-contributor-test-mode-cluster`. It is the entry
point for every other `qa-e2e-*` skill. It is **not** a unit-test skill — it
boots a Kind cluster, builds and pushes runtime images, installs the operator
and Sentinel stack, deploys the bundled Go MCP server, applies a working
grant + session, and exits only after a real MCP `tools/call` succeeds through
Traefik.

Regression evidence contract: this skill is not successful until it records a
live cluster context, image/build inputs, rollout health, and a real MCP
`tools/call` result through the public ingress path. If any live gate cannot
run, report **blocked** with the failing command; do not downgrade to unit tests
or static checks.

Default policy: **reuse if present, create if missing.** Never tear down an
existing `mcp-runtime` cluster without explicit user confirmation —
contributors may have in-flight work on it.

Use only the isolated contributor kubeconfig, never the ambient or production
kubeconfig:

```bash
TEST_KUBECONFIG="${TEST_KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
mkdir -p -m 700 "$HOME/.kube"
if kind get clusters | grep -qx mcp-runtime && \
   ! kubectl --kubeconfig "$TEST_KUBECONFIG" config get-contexts -o name \
     | grep -qx test-mcp-runtime; then
  kind export kubeconfig --name mcp-runtime --kubeconfig "$TEST_KUBECONFIG"
  kubectl --kubeconfig "$TEST_KUBECONFIG" config rename-context \
    kind-mcp-runtime test-mcp-runtime
  chmod 600 "$TEST_KUBECONFIG"
fi
```

## Step 1 — Decide cluster mode

State the mode in the report.

- **reuse** (default if the `mcp-runtime` Kind cluster exists and `kubectl
  --kubeconfig "$TEST_KUBECONFIG" --context test-mcp-runtime get nodes` succeeds). Skip Kind creation;
  re-run `bootstrap` and `cluster doctor` only.
- **create** (no `test-mcp-runtime` context, or user asked for a clean
  cluster). Full path: Kind create → build → setup → deploy demo → grant.
- **rebuild-from-broken** (cluster exists but `cluster doctor` fails). Try
  targeted repair first (rollout restart, re-apply `pipeline deploy`); only
  recreate with the user's explicit ok.

Detect with:

```bash
kind get clusters | grep -qx mcp-runtime && echo "reuse" || echo "create"
kubectl --kubeconfig "$TEST_KUBECONFIG" config get-contexts -o name \
  | grep -qx test-mcp-runtime || echo "no-context"
```

## Step 2 — Host preflight

Run from repo root. Missing tools become recorded blockers, not silent skips.

```bash
command -v docker kind kubectl curl jq python3 go
docker info >/dev/null
STRICT_DEPS_CHECK=1 make deps-check
```

If `make deps-check` reports missing tools, stop here and report — do not
`brew install` or `apt install` without explicit user consent.

## Step 3 — Cluster create (only in **create** mode)

Exactly as documented; the containerd mirror is required for image pulls in
test mode.

```bash
TMP_KIND_CONFIG="$(mktemp)"
trap 'rm -f "$TMP_KIND_CONFIG"' EXIT
cat > "$TMP_KIND_CONFIG" <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
containerdConfigPatches:
  - |-
    [plugins."io.containerd.grpc.v1.cri".registry.mirrors."registry.registry.svc.cluster.local:5000"]
      endpoint = ["http://127.0.0.1:32000"]
EOF

kind create cluster --name mcp-runtime --config "$TMP_KIND_CONFIG" \
  --kubeconfig "$TEST_KUBECONFIG" --wait 120s
chmod 600 "$TEST_KUBECONFIG"
kubectl --kubeconfig "$TEST_KUBECONFIG" config rename-context \
  kind-mcp-runtime test-mcp-runtime
kubectl --kubeconfig "$TEST_KUBECONFIG" config use-context test-mcp-runtime
export KUBECONFIG="$TEST_KUBECONFIG"
```

## Step 4 — Build CLI and install platform

```bash
make deps
make build
./bin/mcp-runtime bootstrap

MCP_SETUP_WAIT_TIMEOUT=900 \
  ./bin/mcp-runtime setup --test-mode \
  --ingress-manifest config/ingress/overlays/http
```

In **reuse** mode, skip `kind create`; still run `make build` (CLI may be
stale) and `bootstrap`. Skip `setup` only if `cluster doctor` already
reports a healthy install; otherwise rerun setup so manifests catch up to
HEAD.

## Step 5 — Health gate

The skill must not exit Step 5 successfully until every check passes.

```bash
./bin/mcp-runtime status
./bin/mcp-runtime cluster status
./bin/mcp-runtime registry status
./bin/mcp-runtime sentinel status
./bin/mcp-runtime cluster doctor
kubectl get pods -A | grep -Ev 'Running|Completed' || echo OK
```

If `cluster doctor` reports admin/UI/ingest key mismatches, roll
`mcp-platform-api`, `mcp-runtime-api`, `mcp-analytics-api`, UI, ingest,
and gateway deployments after patching `mcp-sentinel-secrets`
(see `CLAUDE.md` → API keys). Do not paper over a `Degraded` reading.

If `clickhouse-0` or any `kafka-N` pod is `CrashLoopBackOff` with a high
restart count on an old, reused cluster, that is local PVC corruption, not a
setup regression — see `cluster-troubleshooting/reference.md` for the
diagnosis and recovery steps (pod+PVC recreate, ClickHouse schema replay)
before reporting bring-up as failed.

### Kind image architecture gotcha

Before manually refreshing API/UI/operator/gateway images in Kind, match the
image platform to the Kind node:

```bash
NODE_ARCH="$(docker version --format '{{.Server.Arch}}')"
IMAGE="registry.registry.svc.cluster.local:5000/<repo>:qa-$(date +%s)"
docker build --platform="linux/${NODE_ARCH}" -t "$IMAGE" -f "$DOCKERFILE" .
docker image inspect "$IMAGE" --format '{{.Os}}/{{.Architecture}}'
kind load docker-image "$IMAGE" --name mcp-runtime
kubectl set image -n "$NAMESPACE" deploy/"$DEPLOYMENT" "$CONTAINER=$IMAGE"
kubectl rollout status -n "$NAMESPACE" deploy/"$DEPLOYMENT" --timeout=180s
```

Do not trust a successful `kind load docker-image` by itself. Containerd can
load a `linux/amd64` image into a `linux/arm64` Kind node, but the pod will not
run correctly. If a service image was built for the wrong architecture, rebuild
the touched split API services and UI with the node architecture, load those exact image references, set
the deployment image to the same ref, and wait for rollout.

## Step 6 — Expose the gateway

```bash
pgrep -f 'port-forward.*svc/traefik' >/dev/null \
  || kubectl port-forward -n traefik svc/traefik 18080:8000 \
       >/tmp/mcp-runtime-traefik-pf.log 2>&1 &

curl -fsS -o /dev/null http://localhost:18080/ && echo "dashboard reachable"
```

## Step 7 — Deploy the bundled Go MCP example

Use the metadata from `docs/getting-started.md#3-contributor-test-mode-cluster`
exactly — gateway policy, analytics, and required headers all depend on the
documented shape.

```bash
cat > /tmp/oauth-example-go-2025-11-25-gateway.yaml <<'EOF'
version: v1
servers:
  - name: oauth-example-go-2025-11-25-gateway
    route: /oauth-example-go-2025-11-25-gateway/mcp
    publicPathPrefix: oauth-example-go-2025-11-25-gateway
    port: 8088
    namespace: mcp-servers
    tools:
      - { name: add,   requiredTrust: low, sideEffect: read }
      - { name: upper, requiredTrust: medium, sideEffect: read }
    auth:
      mode: header
      humanIDHeader: X-MCP-Human-ID
      agentIDHeader: X-MCP-Agent-ID
      sessionIDHeader: X-MCP-Agent-Session
    policy:
      mode: allow-list
      defaultDecision: deny
      policyVersion: v1
    session: { required: true }
    gateway: { enabled: true }
    analytics:
      enabled: true
      ingestURL: http://mcp-sentinel-ingest.mcp-sentinel.svc.cluster.local:8081/events
      apiKeySecretRef: { name: oauth-example-go-2025-11-25-gateway-analytics, key: api-key }
EOF

API_KEY="$(kubectl get secret mcp-sentinel-secrets -n mcp-sentinel \
  -o jsonpath='{.data.INGEST_API_KEYS}' | base64 -d | cut -d, -f1)"
kubectl create secret generic oauth-example-go-2025-11-25-gateway-analytics -n mcp-servers \
  --from-literal=api-key="$API_KEY" \
  --dry-run=client -o yaml | kubectl apply -f -

./bin/mcp-runtime server build image oauth-example-go-2025-11-25-gateway \
  --metadata-file /tmp/oauth-example-go-2025-11-25-gateway.yaml \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 \
  --registry registry.registry.svc.cluster.local:5000 \
  --tag dev

./bin/mcp-runtime server push \
  --image registry.registry.svc.cluster.local:5000/oauth-example-go-2025-11-25-gateway:dev

rm -rf /tmp/oauth-example-go-2025-11-25-gateway-manifests
./bin/mcp-runtime pipeline generate \
  --file /tmp/oauth-example-go-2025-11-25-gateway.yaml \
  --output /tmp/oauth-example-go-2025-11-25-gateway-manifests
./bin/mcp-runtime pipeline deploy --dir /tmp/oauth-example-go-2025-11-25-gateway-manifests
kubectl rollout status deploy/oauth-example-go-2025-11-25-gateway -n mcp-servers --timeout=180s
```

## Step 8 — Apply baseline grant + session

```bash
cat > /tmp/workspace-assistant-access.yaml <<'EOF'
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAccessGrant
metadata: { name: workspace-assistant-local, namespace: mcp-servers }
spec:
  serverRef: { name: oauth-example-go-2025-11-25-gateway }
  subject: { humanID: local-user, agentID: local-agent }
  maxTrust: high
  allowedSideEffects: [read]
  policyVersion: v1
  toolRules:
    - { name: add,   decision: allow }
    - { name: upper, decision: allow }
---
apiVersion: mcpruntime.org/v1alpha1
kind: MCPAgentSession
metadata: { name: local-session, namespace: mcp-servers }
spec:
  serverRef: { name: oauth-example-go-2025-11-25-gateway }
  subject: { humanID: local-user, agentID: local-agent }
  consentedTrust: high
  policyVersion: v1
EOF
kubectl apply -f /tmp/workspace-assistant-access.yaml

until ./bin/mcp-runtime server policy inspect oauth-example-go-2025-11-25-gateway --namespace mcp-servers \
  | grep -q local-session; do sleep 2; done
sleep 6   # proxy sidecar polls; do not skip this wait
```

## Step 9 — Real MCP traffic gate

Bring-up is only "done" once a real `tools/call` returns `5`. This catches
the regressions that unit tests miss (Traefik routing, gateway policy
materialization, proxy reload, auth headers, analytics path).

```bash
BASE=http://localhost:18080/oauth-example-go-2025-11-25-gateway/mcp
PROTO=2025-06-18
H=(-H "content-type: application/json"
   -H "accept: application/json, text/event-stream"
   -H "Mcp-Protocol-Version: $PROTO"
   -H "X-MCP-Human-ID: local-user"
   -H "X-MCP-Agent-ID: local-agent"
   -H "X-MCP-Agent-Session: local-session")

SESSION="$(curl -si "${H[@]}" \
  -d '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}' "$BASE" \
  | awk -F': ' 'tolower($1)=="mcp-session-id"{print $2}' | tr -d '\r')"
[ -n "$SESSION" ] || { echo "FAIL: no session id"; exit 1; }

curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","method":"notifications/initialized"}' "$BASE" >/dev/null

RESP="$(curl -sS "${H[@]}" -H "Mcp-Session-Id: $SESSION" \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"add","arguments":{"a":2,"b":3}}}' "$BASE")"
echo "$RESP" | jq -e '.. | .text? // empty' | grep -q '"5"' \
  || { echo "FAIL: tools/call did not return 5: $RESP"; exit 1; }
```

If this fails, do not declare bring-up successful. Capture
`kubectl logs -n mcp-servers <pod> -c mcp-gateway --tail=120` and
`kubectl logs -n traefik deploy/traefik --tail=120`, then return.

## Step 10 — Report

The bring-up report is short and concrete. Other `qa-e2e-*` skills consume it
to know the environment is ready.

- Mode: reuse | create | rebuild-from-broken.
- Cluster context: `kubectl config current-context`.
- Image SHAs pushed (operator, gateway proxy, sentinel api/ui/ingest/processor,
  oauth-example-go-2025-11-25-gateway).
- `cluster doctor` summary line.
- Traefik port-forward pid + log path.
- Demo `tools/call` result: `5` ✓ / details on failure.
- Test-mode credentials reminder (do **not** print passwords; just say
  `PLATFORM_DEV_LOGIN` is enabled).
- Known follow-ups (e.g. `MCPServer` `PartiallyReady` is expected on Kind
  without LB status — set `MCP_INGRESS_READINESS_MODE=permissive` only if a
  later skill needs strict readiness).

## When NOT to use this skill

- Unit / golden / envtest validation — those run without a cluster; do them
  in the package directly per `CLAUDE.md` "Targeted tests."
- Production / TLS / DNS validation — this skill is dev/HTTP only. Production
  needs the full `MCP_PLATFORM_DOMAIN` flow with cert-manager, which is out of
  scope.
- Tearing down a cluster a contributor is using — confirm before destructive
  steps; prefer `kubectl delete` of just the affected namespaces.
