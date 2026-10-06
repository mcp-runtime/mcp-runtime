# Publish an MCP Server

For the supported platform workflow, start with `.mcp` metadata: initialize it
with `mcp-runtime server init`, then build, push, deploy, and verify the server.
Use a hand-written `MCPServer` manifest only for an admin/operator or GitOps
workflow.

Publishing an MCP server takes five steps:

1. generate `.mcp` metadata with `server init` and validate it
2. build the server image
3. push the image to the platform registry
4. deploy the server into the platform
5. verify that the server is reachable and governed

Use an existing platform account, or self-host with [Getting Started](self-hosting.md).
Install the CLI with the [Quickstart](hosted-quickstart.md), and log in before building
or publishing tenant images. Commands below use `mcp-runtime` on `PATH`; source
builds can add their checkout’s `bin` directory to `PATH`.

## Choose an authoring format

You can describe a server in two ways:

- `.mcp` metadata
  Use this for user and team deployments. The CLI creates the metadata and
  handles build, push, and deploy.
- `MCPServer` manifest
  Use this for an admin/operator or GitOps workflow that directly manages the
  Kubernetes resource.

Either way, the operator reconciles a server deployment, service, route, and optional governed request path.

## Recommended workflow: initialize or edit `.mcp` metadata

The metadata-driven server flow uses YAML files under `.mcp`. `server deploy`
publishes directly from that metadata, while `server generate` renders
`MCPServer` manifests when you need YAML for review or GitOps.

Start with `server init` when you do not already have metadata:

```bash
mcp-runtime server init payments --scope org --tool list_invoices --tool refund_invoice
mcp-runtime server init payments \
  --scope org \
  --tool list_invoices \
  --tool-spec refund_invoice:high:destructive
```

This creates `.mcp/servers.yaml` with defaults. Re-run with `--force` to
replace the same server entry, or edit the generated file when tools need
different trust levels or side-effect values. `--tool` is shorthand for a
read-only, low-trust tool. Use `--tool-spec name:low|medium|high:read|write|destructive`
for per-tool metadata.

For grant manifests, `access grant init --tool` is shorthand for
`name:allow:<--trust>`. Use repeated `--tool-rule name:allow|deny:low|medium|high`
when a grant needs mixed per-tool decisions or trust levels. For explicit
session manifests, `access session init` supports `--trust`,
`--expires-in`, `--expires-at`, `--revoked`, and upstream-token secret flags.
For platform-backed access, use an active managed agent ID owned by the subject
team. The cross-team example uses a format sample; replace it with an ID from
`mcp-runtime agent list globex --status active`.

```bash
mcp-runtime auth login --api-url https://platform.example.com

mcp-runtime access grant init payments-globex-cursor \
  --namespace mcp-team-acme \
  --server payments \
  --team-id <globex-team-id> \
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
  --tool list_invoices \
  --tool-rule refund_invoice:allow:high \
  --side-effect read \
  --side-effect destructive \
  --output grant.yaml

mcp-runtime access session init cursor-session \
  --namespace mcp-team-acme \
  --server payments \
  --human-id <user-id> \
  --team-id <globex-team-id> \
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
  --trust medium \
  --expires-in 1h \
  --output session.yaml

mcp-runtime server validate --metadata-dir .mcp \
  --grant-file grant.yaml --session-file session.yaml

mcp-runtime access grant apply --file grant.yaml
# Platform API session apply is admin-only. Use the adapter for agents:
# mcp-runtime access session apply --file session.yaml
```

`server validate` cross-checks the grant and session manifests against the
server metadata before they reach the cluster. Both `--grant-file` and
`--session-file` are repeatable, and `--metadata-file <path>` replaces
`--metadata-dir` when the metadata lives outside `.mcp`.

Adapter-driven agents should skip manual session apply; after the grant exists,
use `mcp-runtime adapter proxy --server payments --namespace mcp-team-acme
--agent agt_01arz3ndektsv4rrffq69g5fav --auto-refresh`, replacing the sample
ID with the active agent's ID. See [Agent Adapters](connect-clients.md).

Example metadata:

```yaml
version: v1
servers:
  - name: payments
    description: Payments MCP server for invoice lookup and refund workflows.
    scope: org
    image: registry.example.com/org/payments
    imageTag: v1.0.0
    route: /payments
    port: 8088
    replicas: 1
    gateway:
      enabled: true
    tools:
      - name: list_invoices
        description: List invoices for a customer account.
        requiredTrust: low
        sideEffect: read
        riskLevel: low
      - name: refund_invoice
        description: Issue a refund for an invoice.
        requiredTrust: high
        sideEffect: destructive
        riskLevel: high
```

### Metadata fields

- `name`
  The server name.
- `description`
  A short platform-facing summary shown in the server catalog.
- `image`
  The image repository.
- `imageTag`
  The image tag.
- `scope`
  Optional publish destination: `tenant`, `org`, or `public`. `server init`
  writes `scope: tenant` by default so new metadata starts in the private team
  publish path. `org` resolves to the org catalog namespace, and `public`
  resolves to the public catalog namespace. `tenant` selects one of the
  authenticated user's team namespaces when you publish through the platform
  API; when generating Kubernetes YAML directly, set `namespace` explicitly for
  team deployments.
- `route`
  The public path prefix that will become the server ingress path.
- `port`
  The container port.
- `replicas`
  The desired replica count.
- `namespace`
  The target namespace.
- `tools`
  Tool inventory for the platform catalog and policy authoring. Include each tool's description when the MCP server SDK exposes one through `tools/list`, and set `sideEffect` to `read`, `write`, or `destructive`. Tool side effects are required when a tool is listed. Optional `riskLevel` (`low`, `medium`, `high`) is informational for catalog and audit views; it does not change gateway authorization.
- `auth`, `policy`, `session`, and `gateway`
  Governed request-path settings. `server init` writes `gateway.enabled: true`, allow-list/deny policy, and `session.required: true` so public tool calls go through the adapter/session path by default. Use `--policy-mode`, `--default-decision`, or `--session-required=false` to change those scaffolded values. Init omits platform-managed gateway wiring and auth/session header details unless you override them intentionally.

### Metadata defaults

If fields are omitted, the loader applies defaults:

- image defaults toward the platform registry path
- `server init` writes `scope: tenant` unless you pass `--scope org` or
  `--scope public`
- `scope: org` / `scope: public` prefix default image repositories with
  `org/` or `public/` and default the generated namespace to
  `mcp-servers-org` or `mcp-servers-public`
- tag defaults to `latest`
- route is normalized with a leading `/`
- port defaults to `8088`
- replicas default to `1`
- namespace defaults to `mcp-servers`
- `server init` omits platform-managed gateway wiring and auth/session header
  details; add those fields only when you intentionally need to override the
  platform/operator defaults

For multi-team deployments, set `scope: tenant` and deploy through
`server deploy --scope tenant` with platform credentials. The platform API
resolves the target team namespace and defaults team ownership metadata. The
namespace is the write boundary for the `MCPServer`, grants, sessions, and
secrets. `server deploy` creates by default; if a server with the same name
already exists, pass `--update` to redeploy it intentionally.
For private registry images, the platform deployment path also creates or
refreshes `mcp-runtime-registry-pull` in that namespace and attaches it to the
workload service account. Raw `kubectl apply` does not run this provisioning;
create the pull Secret in the manifest's namespace before applying raw YAML.

Deploy from metadata:

```bash
mcp-runtime server deploy payments --scope org --metadata-dir .mcp

# Redeploy an existing server after changing metadata or image tag.
mcp-runtime server deploy payments --scope org --metadata-dir .mcp --update

# Optional: render YAML for review/GitOps.
mcp-runtime server generate --metadata-dir .mcp --output manifests/
```

## Build and push the server image

Use one image flow per server so tags stay consistent.

`server push` is the user-facing command for publishing MCP server images
through the authenticated platform API. Registry commands are reserved for
registry administration; use `admin registry push` only for direct Kubernetes
operator debugging.

### Flow A: metadata-driven build with the CLI

```bash
mcp-runtime server build image payments --tag v1.0.0 --platform linux/amd64
```

`server build image` builds the image, resolves the target registry host, tags the local image with that resolved reference, and rewrites matching `.mcp` metadata (`image` and `imageTag`). The command defaults Docker builds to `linux/amd64`, matching common amd64 Kubernetes nodes; set `--platform` or `MCP_DOCKER_PLATFORM` when your target nodes use another architecture. Without `--registry`, the registry host comes from the active `auth login` profile (the registry host saved at login, or `registry.<domain>` derived from a `platform.<domain>` API URL), then `MCP_REGISTRY_INGRESS_HOST`, `MCP_REGISTRY_HOST`, then `MCP_PLATFORM_DOMAIN`, the same order `server push` uses. The command prints the built image ref and the `server push` command to run next. `MCP_REGISTRY_ENDPOINT` is reserved for internal pulls and transfers; it does not configure an Ingress hostname, image name, or credential host. During manifest generation, unqualified and platform-registry image refs are rewritten to the kubelet's internal pull host; external registry refs such as `ghcr.io/owner/image` remain unchanged. When metadata sets `scope: tenant`, the build command uses platform credentials to resolve the same team repository prefix that `server push --scope tenant` uses, so log in first or set `MCP_PLATFORM_API_TOKEN` with a saved or explicit `MCP_PLATFORM_API_URL`.

After this command, push the exact image reference produced by the build output (or read it from the rewritten metadata):

```bash
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server push --scope org --image <exact-image-ref-from-build>
```

`server push` requires platform credentials from `mcp-runtime auth login` or
`MCP_PLATFORM_API_TOKEN` with a saved or explicit `MCP_PLATFORM_API_URL`;
unauthenticated pushes are
rejected before Docker or the in-cluster helper starts. `<exact-image-ref-from-build>`
may be a resolved public registry host such as `registry.example.com/org/payments:v1.0.0`,
or a registry Service address when no public registry Ingress is configured.
Use `--scope public` for public catalog images. Use `--scope tenant` for team
images; if the image name has no repository prefix, the CLI prefixes it with
the authenticated user's sole team slug. For users in multiple teams, initialize
the metadata with a team-scoped repository, for example
`mcp-runtime server init payments --scope tenant --image registry.example.com/acme/payments`,
then run `server build image`, push
the printed image ref and deploy to that team. Explicit tenant repository
prefixes must match one of the user's teams; ambiguous unscoped tenant
repositories are rejected before Docker runs.
`--scope org` and `--scope public` are accepted only when the platform runs in
the matching mode (`PLATFORM_MODE=org` or `public`); `server push` and
`server deploy` reject a disabled scope the same way and list the enabled
scopes, so use `--scope tenant` on a tenant-mode platform. Uploads may take up
to `MCP_REGISTRY_PUSH_UPLOAD_TIMEOUT` (default `20m`) on `mcp-runtime-api`;
archives over 512 MiB are rejected with `413`.

Then deploy from metadata:

```bash
mcp-runtime server deploy payments --scope org --metadata-dir .mcp
```

### Flow B: manual Docker build, push, and direct platform deploy

Use this when you manage image tags directly and want the platform API to write
the `MCPServer` for you:

```bash
docker build -t payments:v1.0.0 .
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server push --scope public --image payments:v1.0.0
mcp-runtime server deploy payments --scope public --image payments --tag v1.0.0
```

Short names like `payments:v1.0.0` are valid for `server push` when that
exact local image tag exists. For `server deploy`, the platform API accepts
short names such as `payments` and resolves them to the configured registry and
scope prefix, for example `<registry>/public/payments` in public mode or
`<registry>/<team-slug>/payments` in tenant mode.
`server deploy --scope public` resolves the platform public catalog namespace;
`--scope org` resolves the org catalog namespace; `--scope tenant` uses the
authenticated user's team namespace unless `--team` or `--namespace` selects one
explicitly. `server deploy` uses the default public route `/<name>/mcp`, and
the operator sets `MCP_PATH` to the path the server receives so the bundled Go,
Python, and Rust examples listen on the route the ingress exposes. The platform API and CLI
deploy flow also default `spec.gateway.enabled: true`, so published servers use
the governed gateway path unless you explicitly provide `spec.gateway`. When
you run `server deploy` from a directory with `.mcp/*.yaml`, the CLI copies the
matching server metadata into the request. If the metadata directory contains
exactly one server, it uses that server's inventory even when the deployed
runtime name is different; this keeps `spec.tools` side-effect metadata in sync
with governance policy.

### Flow C: manual Docker build, push, and manifest apply (admin/GitOps)

Use this when you need full control of `MCPServer` fields and have
admin/operator Kubernetes access. For the normal tenant platform path, use
**Flow A/B** with `server deploy`.

```bash
docker build -t payments:v1.0.0 .
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server push --scope tenant --image payments:v1.0.0
mcp-runtime server apply --file payments.yaml --use-kube
```

## What happens after deploy

After the server description reaches the platform, the operator:

1. stores the `MCPServer` resource in Kubernetes
2. resolves the final image reference
3. creates or updates a `Deployment`
4. creates or updates a `Service`
5. creates or updates an `Ingress`
6. renders gateway policy when governed access is enabled
7. updates `MCPServer.status` with readiness and progress

With the default path-based shape, the server becomes available at:

```text
/{publicPathPrefix}/mcp
```

For the example above, that is:

```text
/payments/mcp
```

## Verify from the CLI

Check server state:

```bash
mcp-runtime auth login --api-url https://platform.example.com
mcp-runtime server status
mcp-runtime server get payments
mcp-runtime status
```

Tenant deploys land in the team namespace, `mcp-team-<team-slug>`, not
`mcp-servers`. Pass `--namespace mcp-team-acme` to `server get`,
`server status`, `server policy inspect`, and the `access` commands when the
server is not in your default namespace.

If the server uses governed access:

```bash
mcp-runtime server policy inspect payments
mcp-runtime ops status
```

If traffic is failing:

```bash
mcp-runtime server policy inspect payments
mcp-runtime status

# Admin/operator only
mcp-runtime ops logs gateway --follow
mcp-runtime server logs payments --follow --use-kube
```

## Common failure points

### Image built, but deploy still points at the wrong image

Check:

- the `spec.image` and `spec.imageTag` in your manifest
- the metadata entry updated by `server build image`
- whether you pushed the exact same image reference (registry/repo/tag) that your metadata or manifest points to

### Image pushed, but server never becomes ready

Check:

- `mcp-runtime server get <name>`
- `mcp-runtime server status`
- `mcp-runtime status`

Most often this is an image reference, image-pull, or routing mismatch.

### Route exists, but governed calls fail

Check:

- `mcp-runtime server policy inspect <name>`
- your grant and session objects
- `mcp-runtime ops logs gateway --follow`

### Event count is 0

Check:

- `kubectl get mcpserver <name> -n <namespace> -o yaml`
- `GatewayReady=True` and `PolicyReady=True` on the MCPServer status
- the server pod is `2/2` and includes the `mcp-gateway` sidecar
- `kubectl logs -n <namespace> <pod> -c mcp-gateway`
- `mcp-runtime ops status`
- `mcp-runtime ops logs ingest --follow`
- `mcp-runtime ops logs processor --follow`

Request analytics only exist for traffic that flows through `mcp-gateway`.
The gateway is on by default; the adapter is optional for analytics and is
required only when you enforce grants/sessions via `spec.policy`. If you apply
raw YAML with `kubectl apply` or `server apply --use-kube`, create a
namespace-local ingest-key Secret and set `spec.analytics.apiKeySecretRef`
(platform `server deploy` does this for you); otherwise ingest may reject
events with 401. Opt out of emission with:

```yaml
analytics:
  disabled: true
```

## Admin and GitOps workflow: write an `MCPServer` manifest

Start with a minimal manifest:

```yaml
apiVersion: mcpruntime.org/v1alpha1
kind: MCPServer
metadata:
  name: payments
  namespace: mcp-servers
spec:
  description: Payments MCP server for invoice lookup and refund workflows.
  image: registry.example.com/payments
  imageTag: v1.0.0
  port: 8088
  publicPathPrefix: payments
  gateway:
    enabled: true
```

`namespace: mcp-servers` suits a single-team install. For a tenant deployment,
use the team namespace (`mcp-team-<team-slug>`) and set `spec.teamID`: the
platform API fills both when you publish with `server deploy --scope tenant`,
but hand-written YAML must state them explicitly.

### What each field does

- `metadata.name`
  The server name inside the platform. This is also the default public route prefix when you do not override it.
- `metadata.namespace`
  Usually `mcp-servers` for a single-team setup. In a multi-team deployment,
  use the team's namespace, for example `mcp-team-acme`; see
  [Multi-team isolation](teams-and-access.md).
- `spec.teamID`
  Stable platform team ID for the server owner. The platform API defaults this
  for team namespaces; hand-written YAML should set it explicitly.
- `spec.description`
  A short platform-facing summary shown in the server catalog.
- `spec.image`
  The image repository to run.
- `spec.imageTag`
  The image tag when the tag is not embedded directly in `spec.image`.
- `spec.port`
  The port your MCP process listens on inside the container.
- `spec.publicPathPrefix`
  The public route prefix. `payments` becomes `/payments/mcp`.
- `spec.gateway.enabled`
  Sends requests through `mcp-gateway` for metrics, traces, and request
  analytics. The sidecar is **on by default** (including `gateway: {}`). Set
  `enabled: false` to opt out. Policy enforcement is separate: omit
  `spec.policy` for observe-mode (tool calls allowed, decisions still recorded);
  set `spec.policy` (for example allow-list + deny) when you want grant/session
  enforcement with the adapter.
- `spec.analytics`
  Analytics emission is on by default whenever the gateway is on and an
  ingest URL is configured. The platform API fills `spec.analytics.ingestURL`
  from `MCP_ANALYTICS_INGEST_URL` when publishing a server; an explicit URL in
  the server metadata takes precedence. The platform API reads the old
  `MCP_SENTINEL_INGEST_URL` key as a fallback for installs awaiting setup
  migration. The operator also accepts its own ingest URL setting.
  Set `spec.analytics.disabled: true`
  to opt out. Platform API deploys create a namespace-local ingest-key Secret
  and set `spec.analytics.apiKeySecretRef` automatically when analytics is not
  disabled.

`mcp-runtime server deploy` and the platform API (`POST /api/v1/runtime/servers`)
default the gateway on. Hand-written YAML can omit `gateway` or use
`gateway: {}`; only `gateway.enabled: false` disables the sidecar.

### Common edits

- Add `spec.ingressHost` for host-based routing instead of path-based routing.
- Add `spec.servicePort` when you want a Service port other than `80`.
- Add `spec.envVars` or `spec.secretEnvVars` for runtime configuration.
- Add `spec.imagePullSecrets` if your registry requires explicit pull credentials.
- Add `spec.tools` with tool descriptions, trust levels, and side-effect classes so the platform catalog and policy engine mirror the tool summaries clients see from `tools/list`.
- Add `spec.auth`, `spec.policy`, `spec.session`, or `spec.rollout` when you want stricter governance or more delivery control.

Apply the manifest only from an admin/operator workstation. `server apply`
requires `--use-kube`, kubectl, and kubeconfig/RBAC access to the target
namespace. For normal platform workflows, use the platform-backed
`server deploy` flow in Option B.

```bash
mcp-runtime server apply --file payments.yaml --use-kube
mcp-runtime server status --use-kube   # pod detail; omit --use-kube for platform API summary
```

## Related docs

- [Getting Started](self-hosting.md)
- [CLI](cli-reference.md)
- [Runtime](runtime-operations.md)
- [API](api-reference.md)
- [Platform services](platform-services.md)

**Next:** [Agent Adapters](connect-clients.md): connect your MCP client through the adapter proxy.
