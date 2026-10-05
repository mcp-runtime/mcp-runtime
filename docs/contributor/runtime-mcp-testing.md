# Runtime MCP Testing

Use this page to deploy MCP servers, verify catalog visibility, inspect
gateway policy, and clean up stale runtime objects in a contributor cluster.

## Catalog Model

`mcp-servers` is the legacy single-team/example namespace used by the local
testing manifests below. In default `tenant` platform mode, signed-in users see
only team namespaces they belong to. `--platform-mode org` uses
`mcp-servers-org` as the shared authenticated catalog, and
`--platform-mode public` uses `mcp-servers-public` as the anonymous preview
catalog. Team-specific MCPs belong in namespaces such as `mcp-team-tenant-a`.

Expected UI/API behavior:

| Principal | Expected catalog |
|---|---|
| Anonymous | `401` for `/api/v1/runtime/servers`, except public mode can read `mcp-servers-public` |
| Normal user in tenant mode | MCPs from team namespaces they belong to |
| Tenant user in tenant mode | MCPs from their own team namespace |
| User in org mode | MCPs from `mcp-servers-org` |
| User in public mode | MCPs from `mcp-servers-public` |
| Admin | Cluster-wide management visibility, with namespace/team checks on writes |

## Deploy the bundled workspace assistant

The bundled workspace assistant sample is useful for disposable local testing.
Keep it separate from long-lived shared-cluster MCPs.

Create metadata:

```bash
cat > /tmp/oauth-example-go-2025-11-25-gateway.yaml <<'EOF'
version: v1
servers:
  - name: oauth-example-go-2025-11-25-gateway
    description: Workspace assistant MCP server for task cards, release notes, and text cleanup.
    namespace: mcp-servers
    route: /oauth-example-go-2025-11-25-gateway/mcp
    publicPathPrefix: oauth-example-go-2025-11-25-gateway
    port: 8088
    tools:
      - name: add
        description: Add two numeric values.
        requiredTrust: low
        sideEffect: read
      - name: upper
        description: Convert text to uppercase for normalization checks.
        requiredTrust: medium
        sideEffect: read
      - name: create_task
        description: Create a deterministic task card summary.
        requiredTrust: low
        sideEffect: write
      - name: draft_release_note
        description: Draft a compact release note from a change summary and impact.
        requiredTrust: low
        sideEffect: read
    auth:
      issuerURL: http://localhost:18080/oauth
      audience: http://localhost:18080/demo/mcp
    policy:
      mode: allow-list
      defaultDecision: deny
      policyVersion: v1
    session:
      required: true
    gateway:
      enabled: true
EOF
```

Build, push, and deploy:

```bash
./bin/mcp-runtime server build image oauth-example-go-2025-11-25-gateway \
  --metadata-file /tmp/oauth-example-go-2025-11-25-gateway.yaml \
  --dockerfile examples/oauth-example-go-2025-11-25/Dockerfile \
  --context examples/oauth-example-go-2025-11-25 \
  --tag dev

./bin/mcp-runtime auth login --api-url http://localhost:18080

# `server build image` updates the metadata with the resolved registry image
# and tags that exact image locally. Push that image ref, not a guessed short
# name, so the push command and deploy metadata stay in sync.
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
./bin/mcp-runtime server push \
  --image "$IMAGE_REF"

./bin/mcp-runtime server deploy oauth-example-go-2025-11-25-gateway \
  --scope tenant \
  --metadata-file /tmp/oauth-example-go-2025-11-25-gateway.yaml
SERVER_NAMESPACE="$(
  kubectl get deploy -A -l app=oauth-example-go-2025-11-25-gateway \
    -o jsonpath='{.items[0].metadata.namespace}'
)"
kubectl rollout status deploy/oauth-example-go-2025-11-25-gateway -n "$SERVER_NAMESPACE" --timeout=180s
```

## Inspect runtime outputs

```bash
SERVER=oauth-example-go-2025-11-25-gateway
NAMESPACE="$(
  kubectl get deploy -A -l app="$SERVER" \
    -o jsonpath='{.items[0].metadata.namespace}'
)"

kubectl get mcpserver "$SERVER" -n "$NAMESPACE" -o yaml
kubectl get deploy/"$SERVER" svc/"$SERVER" ingress/"$SERVER" -n "$NAMESPACE" -o wide
kubectl get cm -n "$NAMESPACE" "${SERVER}-gateway-policy" -o yaml

./bin/mcp-runtime auth login --api-url http://localhost:18080
./bin/mcp-runtime server policy inspect "$SERVER" --namespace "$NAMESPACE"
```

Admin/operator fallback when you need the raw ConfigMap JSON without platform auth:

```bash
./bin/mcp-runtime server policy inspect "$SERVER" --namespace "$NAMESPACE" --use-kube
```

The MCP app container is usually distroless. Use logs and `kubectl describe`
before trying to exec a shell:

```bash
POD="$(kubectl get pods -n "$NAMESPACE" -l app="$SERVER" -o jsonpath='{.items[0].metadata.name}')"
kubectl describe pod -n "$NAMESPACE" "$POD"
kubectl logs -n "$NAMESPACE" "$POD" -c "$SERVER"
kubectl logs -n "$NAMESPACE" "$POD" -c mcp-gateway
```

## Grants and Sessions

The example below is a contributor-only runtime policy test. It uses synthetic
identity values and the explicit `--use-kube` path to test the gateway's CRD
policy behavior in a disposable cluster. It does not test managed-agent
directory checks or platform-issued sessions. For a platform-backed access
flow, use the [Quickstart](../hosted-quickstart.md) or the [staging E2E guide](staging-e2e.md).

Gateway policy requires both an access grant and an agent session when the
server has `spec.session.required=true`.

Use `init` to scaffold manifests. In this test, apply both resources with
`--use-kube`; this intentionally bypasses platform API identity validation.
Platform API session apply requires an admin role. For normal agent use, prefer
the adapter path described in the user docs.

```bash
./bin/mcp-runtime auth login --api-url http://localhost:18080 \
  --email admin@mcpruntime.org --password 'admin@123'

./bin/mcp-runtime access grant init workspace-assistant-grant \
  --namespace mcp-servers \
  --server oauth-example-go-2025-11-25-gateway \
  --human-id local-user \
  --agent-id local-agent \
  --tool add --tool upper \
  --trust high \
  --output /tmp/grant.yaml

./bin/mcp-runtime access session init local-session \
  --namespace mcp-servers \
  --server oauth-example-go-2025-11-25-gateway \
  --human-id local-user \
  --agent-id local-agent \
  --trust high \
  --output /tmp/session.yaml

./bin/mcp-runtime access grant apply --file /tmp/grant.yaml --use-kube
./bin/mcp-runtime access session apply --file /tmp/session.yaml --use-kube
```

Then verify materialization:

```bash
kubectl get mcpaccessgrant,mcpagentsession -n "$NAMESPACE" -o wide
./bin/mcp-runtime server policy inspect "$SERVER" --namespace "$NAMESPACE"
```

Raw ConfigMap inspection without platform auth (`--use-kube`):

```bash
./bin/mcp-runtime server policy inspect "$SERVER" --namespace "$NAMESPACE" --use-kube
```

If the CRDs exist but the policy file does not include them, check:

```bash
kubectl logs -n mcp-runtime deploy/mcp-runtime-operator-controller-manager --since=10m
kubectl get cm -n "$NAMESPACE" "${SERVER}-gateway-policy" \
  -o 'go-template={{index .data "policy.json"}}'
```

## Tenant MCPs

For team-specific servers, use one namespace per tenant or team and set
`spec.teamID` to the immutable platform team ID. The platform API defaults and
validates this for team namespace writes; direct `kubectl apply` depends on the
manifest and Kubernetes RBAC.

Cross-team delegation is modeled as an access resource in the server owner's
namespace with an explicit foreign `subject.teamID`. For example, Tenant A can
create a grant and session in `mcp-team-tenant-a` that point at
`tenant-a-mcp`, while setting `subject.teamID` to Tenant B's team ID. The
request must then carry an OAuth token containing Tenant B's team, human,
agent, and session claims. Reusing the same session with Tenant A's team claim
should fail with `session_not_found` or `no_matching_grant`.

Inventory command:

```bash
kubectl get mcpservers -A \
  -o custom-columns='NAMESPACE:.metadata.namespace,NAME:.metadata.name,TEAM:.spec.teamID,PATH:.spec.ingressPath,READY:.status.deploymentReady,GW:.status.gatewayReady'
```

Tenant visibility should be checked through the UI/API, not only `kubectl`,
because the UI/API path enforces principal namespaces.

## Remove a stale test MCP

Remove access resources first, then the server and single-purpose Secret:

```bash
kubectl delete mcpagentsession <session-name> -n <namespace> --ignore-not-found
kubectl delete mcpaccessgrant <grant-name> -n <namespace> --ignore-not-found
kubectl delete mcpserver <server-name> -n <namespace> --ignore-not-found
kubectl delete secret <server-name>-analytics-creds -n <namespace> --ignore-not-found
```

Confirm the catalog through the UI/API after cleanup:

```bash
curl -sS -b /tmp/mcp-test-user-cookie.txt \
  http://localhost:18080/api/v1/runtime/servers |
  jq '{count: (.servers|length), names: [.servers[] | (.namespace + "/" + .name)]}'
```
