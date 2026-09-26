# CLI reference

Examples on this page use the example servers in the repository.

## Quick reference

| Goal | Commands |
|---|---|
| Log in | `auth login` → `auth use <profile>` |
| Deploy a server | `server init` → `server validate` → `server build image` → `server push` → `server deploy` |
| Grant an agent access | `access grant init` → `server validate --grant-file` → `access grant apply` |
| Create a session manually | `access session init` → `access session apply` |
| Connect an MCP client | `adapter proxy --server ... --agent ... --auto-refresh` |
| Find a tool | `catalog tools` · `catalog tool <name>` |
| Create a team and its users | `team create` → `team user create` |
| Check platform health | `status` |
| Inspect a running server | `server list` · `server get` · `server policy inspect` |
| View analytics logs | `sentinel status` · `sentinel logs api` |
| Check setup readiness | `cluster doctor` |
| Diagnose an installed cluster | `cluster diagnostics` |
| Check an OIDC provider before mcp-auth | `auth provider-check` |

**Example servers in this repo:**

| Server | Language | Run command | Tools |
|---|---|---|---|
| `workspace-assistant-mcp` | Go | `go run .` | `aaa-ping`, `echo`, `add`, `upper`, `lower`, `slugify`, `create_task`, `draft_release_note` |
| `data-utility-mcp` | Python | `python app.py` | `echo`, `add`, `multiply`, `upper`, `lower`, `ping`, `reverse` |
| `text-analysis-mcp` | Rust | `cargo run` | `repeat`, `word_count`, `extract_keywords` |

All three listen on `http://localhost:8088/mcp` by default.
`--from-server http://localhost:8088` appends `/mcp` automatically.

## Access model

Three roles control what each command can do:

| Role | Who | How to authenticate |
|---|---|---|
| User | Team member deploying servers | `mcp-runtime auth login` |
| Admin | Platform admin or kube operator | Platform API admin role, or `--use-kube` with cluster-admin RBAC |
| Operator | Cluster operator | `KUBECONFIG` with cluster-admin RBAC; no platform login needed |

Commands on this page are labelled **[User]**, **[Admin]**, or **[Operator]**.

## Profiles

Credentials are saved in `~/.mcpruntime/config.json`. Each `auth login` creates a
named profile. Switch with `auth use` or override for a single command with
`MCP_PLATFORM_API_PROFILE`.

```bash
# Save as the default profile
mcp-runtime auth login --api-url https://platform.example.com

# Save under a named profile
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email alice@acme.com --password '...' \
  --profile alice

# Switch the active profile
mcp-runtime auth use alice

# Use a different profile for one command without switching
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team list

# Check which profile is active
mcp-runtime auth status

# Remove saved credentials
mcp-runtime auth logout
```

## Command map

| Command | Role | What it does | Guide |
|---|---|---|---|
| `auth` | User | Save and switch platform credentials | [auth](#auth) |
| `auth provider-check` | Operator | Inspect an OIDC provider before `setup --with-mcp-auth-server` | [MCP authorization](mcp-authorization.md) |
| `status` | User | Platform health at a glance | [status](#status) |
| `catalog` | User | Search tools across visible servers | [catalog](#catalog) |
| `server` | User / Admin | Scaffold, validate, build, push, deploy, manage | [Publish a server](publish-mcp-server.md) |
| `registry` | Operator | Inspect or configure a registry | [registry](#registry) |
| `access` | User / Admin | Grants and sessions for gateway policy | [API reference](api.md) |
| `adapter` | User | HTTP proxy, stdio shim, and mTLS enrollment for agents | [Agent adapters](agent-adapters.md) |
| `team` | Admin | Create teams and add password users | [Multi-team](multi-team.md) |
| `sentinel` | Operator | Inspect and operate the analytics stack | [Sentinel](sentinel.md) |
| `bootstrap` | Operator | Pre-install cluster checks | [Cluster readiness](cluster-readiness.md) |
| `setup` | Operator | Install the full platform stack | [setup](#setup) |
| `update` | Operator | Update installed platform services to a release | [update](#update) |
| `cluster` | Operator | Initialize clusters, run readiness and post-install checks, manage cert-manager | [Deployment targets](deployment-targets.md) |

`server push` publishes an image through the authenticated platform API.
Registry administration is under `registry`. Use `admin registry push` only for
direct Kubernetes operator debugging.

MCP Runtime is alpha software. Commands, flags, and output shapes on this page
may change between releases.

## auth

**[User]**

```bash
# Interactive
mcp-runtime auth login --api-url https://platform.example.com

# Non-interactive (CI)
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --token-stdin < token.txt

# Email + password
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email alice@acme.com --password '...' \
  --profile alice

mcp-runtime auth use alice
mcp-runtime auth status
mcp-runtime auth logout
```

`--api-url` takes scheme and host only, with no `/api` path. `--username` is an
alias for `--email`; prefer `--email`.

**[Operator]** Check an OIDC provider before enabling the bundled mcp-auth
authorization server. The command fetches
`/.well-known/openid-configuration` and reports what setup needs:

```bash
mcp-runtime auth provider-check --issuer-url https://keycloak.example.com/realms/mcp
```

See [MCP authorization](mcp-authorization.md) for the full provider flow.

## status

**[User]** (kubeconfig optional for sentinel detail)

```bash
mcp-runtime status                                         # registry, operator, platform API
mcp-runtime registry status                               # registry pod + endpoint
KUBECONFIG=~/.kube/config mcp-runtime sentinel status     # sentinel stack
```

## server

**[User]** by default, **[Admin]** with `--use-kube`

> Full guide: [Publish an MCP Server](publish-mcp-server.md)

The developer flow: **init → validate → build → push → deploy**.

### server init

`server init` creates `.mcp/servers.yaml` with tool names, trust levels, side effects,
and policy. Tool names must exactly match what your server implements.

Use `--from-server` to discover them from a running local instance:

```bash
# workspace-assistant-mcp (Go)
cd examples/workspace-assistant-mcp
go run . &
SERVER_PID=$!
mcp-runtime server init workspace-demo --from-server http://localhost:8088
kill $SERVER_PID
# Discovered: aaa-ping, add, create_task, draft_release_note, echo, lower, slugify, upper
```

```bash
# data-utility-mcp (Python)
cd examples/data-utility-mcp
pip install "mcp[cli]"
python app.py &
SERVER_PID=$!
mcp-runtime server init data-util --from-server http://localhost:8088
kill $SERVER_PID
# Discovered: add, echo, lower, multiply, ping, reverse, upper
```

```bash
# text-analysis-mcp (Rust)
cd examples/text-analysis-mcp
cargo run &
SERVER_PID=$!
mcp-runtime server init text-analysis --from-server http://localhost:8088
kill $SERVER_PID
# Discovered: extract_keywords, repeat, word_count
```

Manual alternative when you already know the tool names:

```bash
# --tool name                         adds an allow rule with read side-effect and low trust
# --tool-spec name:trust:side-effect  full control  (trust: low|medium|high,
#                                                    side-effect: read|write|destructive)
mcp-runtime server init workspace-demo \
  --tool echo \
  --tool add \
  --tool upper \
  --tool-spec create_task:medium:write \
  --tool-spec draft_release_note:medium:write
```

### server validate

Checks tool names before a build. A mismatch causes `tool_side_effect_unknown`
errors at the gateway at runtime.

```bash
mcp-runtime server validate --metadata-dir .mcp

# Point at a servers.yaml outside the default .mcp directory
mcp-runtime server validate --metadata-file config/servers.yaml

# Validate a grant alongside the metadata
mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml

# Validate a session manifest, or several grants and sessions at once
mcp-runtime server validate --metadata-dir .mcp --session-file session.yaml
mcp-runtime server validate --metadata-dir .mcp \
  --grant-file grant.yaml --grant-file grant-cross.yaml \
  --session-file session.yaml --session-file session-cross.yaml

# Cross-check against the locally running server
mcp-runtime server validate --metadata-dir .mcp --from-server http://localhost:8088
```

`--grant-file` and `--session-file` are repeatable. `--metadata-file`
overrides `--metadata-dir`.

### server build image

Run from the directory where the Dockerfile lives:

```bash
cd examples/workspace-assistant-mcp
mcp-runtime server build image workspace-demo --tag v1
```

The command prints the exact image ref and the matching push command:

```
Built image registry.example.com/acme/workspace-demo:v1
Push it with: mcp-runtime server push --image registry.example.com/acme/workspace-demo:v1 --scope tenant
```

Without `--registry`, the registry host comes from the active `auth login`
profile (the registry host saved at login), then `MCP_REGISTRY_INGRESS_HOST`,
`MCP_REGISTRY_HOST`, or `MCP_PLATFORM_DOMAIN`, then cluster discovery.

Use `--platform linux/amd64` when building on Apple Silicon for k3s or EKS nodes.

### server push

Use the exact ref printed by `server build image`:

```bash
mcp-runtime server push \
  --image registry.example.com/acme/workspace-demo:v1 \
  --scope tenant

# Other scopes
mcp-runtime server push --image ... --scope org      # org-wide catalog
mcp-runtime server push --image ... --scope public   # anonymous catalog
```

`server push` requires platform credentials.

### server deploy

```bash
mcp-runtime server deploy workspace-demo \
  --scope tenant \
  --metadata-dir .mcp

# Re-deploy after a code or image change
mcp-runtime server deploy workspace-demo \
  --scope tenant \
  --metadata-dir .mcp \
  --update
```

### Full example: workspace-assistant-mcp

```bash
cd examples/workspace-assistant-mcp

go run . &
SERVER_PID=$!
mcp-runtime server init workspace-demo --from-server http://localhost:8088
kill $SERVER_PID

mcp-runtime server validate --metadata-dir .mcp

mcp-runtime auth use alice
mcp-runtime server build image workspace-demo --tag v1
# prints: registry.example.com/acme/workspace-demo:v1

mcp-runtime server push \
  --image registry.example.com/acme/workspace-demo:v1 \
  --scope tenant

mcp-runtime server deploy workspace-demo --scope tenant --metadata-dir .mcp

mcp-runtime server list
mcp-runtime server get workspace-demo --namespace mcp-team-acme
mcp-runtime server policy inspect workspace-demo --namespace mcp-team-acme
```

### Inspect and manage

```bash
mcp-runtime server list
mcp-runtime server get workspace-demo --namespace mcp-team-acme
mcp-runtime server status --namespace mcp-team-acme
mcp-runtime server connect-config workspace-demo --namespace mcp-team-acme --client claude
mcp-runtime server policy inspect workspace-demo --namespace mcp-team-acme
mcp-runtime server delete workspace-demo
mcp-runtime server generate --metadata-dir .mcp --output manifests/
```

## catalog

**[User]** platform API only

```bash
mcp-runtime catalog tools
mcp-runtime catalog tools --query invoice --risk high
mcp-runtime catalog tools --namespace mcp-team-acme --side-effect write
mcp-runtime catalog tool refund_invoice --server payments --output json
```

The catalog is read-only. It shows tools from visible servers with trust,
side effect, computed or declared risk, drift (`declared`, `ungoverned`,
`missing`), and copyable connect config.

### Direct Kubernetes operations (--use-kube) [Admin]

```bash
mcp-runtime server create workspace-demo --image repo/workspace-demo --tag v1 \
  --namespace mcp-team-acme --use-kube
mcp-runtime server apply  --file server.yaml --use-kube
mcp-runtime server export workspace-demo --namespace mcp-team-acme --use-kube
mcp-runtime server patch  workspace-demo --namespace mcp-team-acme \
  --patch '{"spec":{"imageTag":"v2"}}' --use-kube
mcp-runtime server logs   workspace-demo --namespace mcp-team-acme --follow --use-kube
```

## registry

**[Operator]**

```bash
# Inspect
mcp-runtime registry status
mcp-runtime registry info

# Configure an external registry
mcp-runtime registry provision --url registry.example.com
```

Publish MCP server images with [`server push`](#server-push). The registry
group covers operator tasks: status, info, and external registry provisioning.
`admin registry push` is a separate hidden command for direct Kubernetes
debugging and requires cluster-admin access.

## access

**[User]** for grants, **[Admin]** for session `apply`

> Full reference: [API reference](api.md)

Tool names in grants must exactly match `.mcp/servers.yaml`. Run
`server validate --grant-file grant.yaml` before applying to catch mismatches
that cause `tool_side_effect_unknown` at the gateway.

### Grants

```bash
# Allow specific tools (read side-effect, low trust)
mcp-runtime access grant init workspace-ops \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent-id cursor \
  --tool echo \
  --tool add \
  --tool upper \
  --output grant.yaml

# Mixed allow/deny rules: --tool-rule name:allow|deny:low|medium|high
mcp-runtime access grant init workspace-ops \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent-id cursor \
  --tool-rule echo:allow:low \
  --tool-rule add:allow:low \
  --tool-rule create_task:deny:medium \
  --output grant.yaml

# Validate then apply
mcp-runtime server validate --metadata-dir .mcp --grant-file grant.yaml
mcp-runtime access grant apply --file grant.yaml

# Manage
mcp-runtime access grant list
mcp-runtime access grant list    --namespace mcp-team-acme
mcp-runtime access grant get     workspace-ops --namespace mcp-team-acme
mcp-runtime access grant disable workspace-ops --namespace mcp-team-acme
mcp-runtime access grant enable  workspace-ops --namespace mcp-team-acme
mcp-runtime access grant revoke-sessions workspace-ops --namespace mcp-team-acme
mcp-runtime access grant delete  workspace-ops --namespace mcp-team-acme
```

`grant revoke-sessions` revokes all active adapter sessions explicitly linked
to the grant, keeps the grant enabled, and records the revocations in the
platform audit trail. It requires platform API authentication; it is not a
direct `--use-kube` operation.

### Sessions

Agents normally get sessions from `adapter --auto-refresh`. Use
`session init` + `session apply` only for manual sessions.

```bash
mcp-runtime access session init cursor-session \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent-id cursor \
  --trust low \
  --expires-in 4h \
  --output session.yaml

MCP_PLATFORM_API_PROFILE=admin \
  mcp-runtime access session apply --file session.yaml

mcp-runtime access session list
mcp-runtime access session get      cursor-session --namespace mcp-team-acme
mcp-runtime access session revoke   cursor-session --namespace mcp-team-acme
mcp-runtime access session unrevoke cursor-session --namespace mcp-team-acme
```

### Explain a hypothetical decision

Evaluate a request against the live rendered gateway policy without sending
traffic. The command exits 0 for an allow decision and 1 for a deny decision.
Use `--policy-file` to test a local rendered policy document; the file must
include a valid policy revision.

```bash
mcp-runtime access explain \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --human alice@example.com \
  --tool write-file

mcp-runtime access explain \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --human alice@example.com \
  --tool write-file \
  --policy-file candidate-policy.json \
  --json
```

### Cross-team access

Team A can grant Team B's agents access to Team A's servers:

```bash
mcp-runtime access grant init workspace-to-globex \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --team-id <globex-team-uuid> \
  --agent-id cursor \
  --expires-in 4h \
  --tool echo \
  --tool add \
  --output grant-cross.yaml
mcp-runtime access grant apply --file grant-cross.yaml

MCP_PLATFORM_API_PROFILE=admin \
  mcp-runtime access session init globex-session \
    --server workspace-demo \
    --namespace mcp-team-acme \
    --team-id <globex-team-uuid> \
    --agent-id cursor \
    --trust low \
    --expires-in 4h \
    --output session-cross.yaml
MCP_PLATFORM_API_PROFILE=admin \
  mcp-runtime access session apply --file session-cross.yaml
```

See [Multi-team isolation](multi-team.md).

The grant expiry is the maximum lifetime of the delegation. Any session issued
from it expires no later than that time, and `--auto-refresh` cannot renew a
session after the grant expires. Use `--expires-at <RFC3339 timestamp>` when a
fixed deadline is more appropriate than a duration.

## adapter

**[User]**

> Full guide: [Agent adapters](agent-adapters.md)

The adapter adds platform session and governance headers to every request
before it reaches the MCP server. When `--server` is set, the adapter creates
the session. `--agent` (session name) is required in that case. `--agent-id`
sets the identity header forwarded to the server.

The adapter never creates grants. First apply an enabled `MCPAccessGrant` that
matches the server, the signed-in user, and the agent (`access grant apply`).
Without it, the platform refuses to issue or refresh the session and the adapter
exits with a 403.

`--platform-url` takes scheme and host only, with no `/api` path; it defaults to
the URL saved by `auth login` or `$MCP_PLATFORM_API_URL`.

```bash
# Enterprise mTLS enrollment. Generates client.key locally and writes the
# issued client.crt and ca.crt into the output directory.
mcp-runtime adapter enroll \
  --platform-url https://platform.example.com \
  --server workspace-demo \
  --namespace mcp-servers \
  --agent cursor \
  --trust-domain mcpruntime.org \
  --output-dir ~/.config/mcp-runtime/workspace-demo

# HTTP proxy. MCP clients connect to http://127.0.0.1:8099
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --server workspace-demo \
  --agent cursor \
  --agent-id cursor \
  --auto-refresh \
  --listen 127.0.0.1:8099

# stdio shim for Claude Desktop or local agent processes
mcp-runtime adapter stdio \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --server workspace-demo \
  --agent cursor \
  --agent-id cursor \
  --auto-refresh
```

With the adapter running, point any MCP client at `http://127.0.0.1:8099`.
The adapter handles session creation and governance headers.

For OAuth-protected servers, an adapter may additionally present a
session-bound certificate. OAuth authentication is still required. Enroll once
and pass the files, or let the adapter enroll a certificate in memory with
`--auth mtls`:

```bash
# Reuse enroll output
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --tls-client-cert ~/.config/mcp-runtime/workspace-demo/client.crt \
  --tls-client-key  ~/.config/mcp-runtime/workspace-demo/client.key \
  --tls-ca-bundle   ~/.config/mcp-runtime/workspace-demo/ca.crt

# One-command in-memory enrollment
mcp-runtime adapter proxy \
  --auth mtls \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --platform-url https://platform.example.com \
  --server workspace-demo \
  --namespace mcp-servers \
  --agent cursor \
  --auto-refresh
```

`--auth mtls` requires an `https` runtime URL. The platform returns its trust
domain with the adapter session; `--trust-domain` or `MCP_TRUST_DOMAIN` is only
an optional matching override. See
[Agent adapters](agent-adapters.md#enterprise-mtls-and-spiffe).

## agent

Create and manage immutable, team-scoped agent identities used by access grants
and sessions. Deactivation retains history and revokes active sessions.

```bash
mcp-runtime agent create acme --name "Build assistant"
mcp-runtime agent list acme --status active --limit 50
mcp-runtime agent get agt_01arz3ndektsv4rrffq69g5fav
mcp-runtime agent rename agt_01arz3ndektsv4rrffq69g5fav --name "Release assistant"
mcp-runtime agent deactivate agt_01arz3ndektsv4rrffq69g5fav
mcp-runtime agent reactivate agt_01arz3ndektsv4rrffq69g5fav
```

Agent IDs are platform-generated and immutable. Names are unique per team;
inactive agents remain in the directory for history but cannot be selected for
new grants or sessions. Deactivation revokes active sessions. Admins can
manage all teams; team owners manage their own agents; team members can list
and view their team's agents. The administration workspace includes an
**Agents** directory page.
Agent subjects must be selected from the active directory for the subject's
team. The API rejects unknown, malformed, inactive, and wrong-team agent IDs.

## team

**[Admin]** All `team` commands require the platform API admin role.

> Full guide: [Multi-team isolation](multi-team.md)

```bash
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team list

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team create acme --name "Acme Corp"

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user create acme \
  --email alice@acme.com --password '...' --role owner

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user list acme
```

Team users log in with:

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email alice@acme.com --password '...' \
  --profile alice
```

Note: `team init` is deprecated. Use `team create`.

## sentinel

**[Operator]** Requires `KUBECONFIG` with cluster-admin RBAC.

> Full guide: [Sentinel](sentinel.md)

```bash
KUBECONFIG=~/.kube/config mcp-runtime sentinel status
KUBECONFIG=~/.kube/config mcp-runtime sentinel events

# Logs support --follow, --tail, --since, and --previous
KUBECONFIG=~/.kube/config mcp-runtime sentinel logs api --since 15m --follow
KUBECONFIG=~/.kube/config mcp-runtime sentinel logs ingest --tail 200

# Restart
KUBECONFIG=~/.kube/config mcp-runtime sentinel restart gateway
KUBECONFIG=~/.kube/config mcp-runtime sentinel restart --all

# Port-forward a component locally
KUBECONFIG=~/.kube/config mcp-runtime sentinel port-forward ui
KUBECONFIG=~/.kube/config mcp-runtime sentinel port-forward grafana
```

Component names for `logs` and `restart`:
`clickhouse`, `kafka`, `ingest`, `processor`, `api`, `ui`,
`gateway`, `prometheus`, `grafana`, `otel-collector`, `tempo`, `loki`, `promtail`

## bootstrap

**[Operator]** Run before `setup` on a fresh cluster.

> Full guide: [Cluster readiness](cluster-readiness.md)

```bash
mcp-runtime bootstrap
mcp-runtime bootstrap --provider k3s
mcp-runtime bootstrap --apply --provider k3s    # automated fix on k3s
```

## setup

**[Operator]** Runs pre-flight checks before installing anything.

```bash
# Recommended: drive all flags from an env file
mcp-runtime setup --env-file config/deployments/mcpruntime-org.env

# Common explicit flags
mcp-runtime setup \
  --with-tls \
  --tls-cluster-issuer letsencrypt-prod \
  --registry-mode bundled-https \
  --platform-mode tenant \
  --ingress none

mcp-runtime setup --with-tls --acme-email ops@example.com   # Let's Encrypt
mcp-runtime setup --without-sentinel                         # skip analytics
mcp-runtime setup --test-mode                                # local Kind dev
```

### Defaults worth knowing

| Flag | Default | Notes |
|---|---|---|
| `--ingress` | `traefik` | `none` leaves an existing ingress controller alone |
| `--ingress-manifest` | `config/ingress/overlays/http` | Use the HTTP overlay for local Kind installs |
| `--registry-mode` | `auto` | Uses a provisioned registry config when present, otherwise the bundled registry |
| `--registry-type` | `docker` | Harbor is not available yet |
| `--registry-storage` | `20Gi` | Bundled registry PVC size |
| `--platform-mode` | `tenant` | `org` and `public` change the default publish namespace |
| `--storage-mode` | `dynamic` | Use `hostpath` for single-node k3s/minikube/kind with no provisioner |

### Production guardrails

```bash
# Require production-style registry and TLS validation
mcp-runtime setup --registry-mode bundled-https --with-tls --strict-prod

# Enterprise mTLS for gateway and adapter client certificates (requires --with-tls)
mcp-runtime setup \
  --with-tls \
  --tls-cluster-issuer letsencrypt-prod \
  --mtls-cluster-issuer company-workload-ca
```

`--strict-prod` requires TLS, rejects dev-only registry assumptions such as
`registry.local`, and forces a stable production-style registry endpoint.
`--mtls-cluster-issuer` names the cert-manager `ClusterIssuer` for workload
certificates; name your enterprise issuer, or the bundled `mcp-runtime-ca` to
have setup provision one. `--test-mode` defaults it to `mcp-runtime-ca`. Full
flow: [Agent adapters](agent-adapters.md#enterprise-mtls-and-spiffe).

### Local and single-node clusters

```bash
# Single-node cluster without a dynamic provisioner
mcp-runtime setup --test-mode --storage-mode hostpath \
  --ingress-manifest config/ingress/overlays/http

# Build and publish the setup images in parallel
mcp-runtime setup --test-mode --parallel-builds
```

`--parallel-builds` only parallelizes image build and publish; cluster,
registry, TLS, and rollout sequencing are unchanged.

### Optional mcp-auth authorization server

The bundled OAuth authorization server is opt-in. Check the provider first with
`auth provider-check`, then enable it:

```bash
mcp-runtime setup \
  --with-tls \
  --with-mcp-auth-server \
  --mcp-auth-connectors-file connectors.json \
  --mcp-auth-connector keycloak \
  --mcp-auth-signing-key-secret mcp-auth-signing-key
```

Outside `--test-mode`, setup derives the issuer from `MCP_PLATFORM_DOMAIN`; the
issuer and each server's `auth.issuerURL` can be overridden explicitly. The
operator reconciles accepted resources from OAuth MCPServer audiences, so
`--mcp-auth-resource-url` is only an optional bootstrap value. The signing-key
Secret remains required. With managed TLS, setup provisions the issuer
certificate. Full walkthrough:
[MCP authorization](mcp-authorization.md).

Key env vars for `--env-file` (see `config/deployments/mcpruntime-org.env.example`):

| Env var | Flag |
|---|---|
| `MCP_PLATFORM_DOMAIN=example.com` | derives all three ingress hostnames |
| `MCP_SETUP_WITH_TLS=1` | `--with-tls` |
| `MCP_SETUP_TLS_CLUSTER_ISSUER=letsencrypt-prod` | `--tls-cluster-issuer` |
| `MCP_SETUP_MTLS_CLUSTER_ISSUER=company-workload-ca` | `--mtls-cluster-issuer` |
| `MCP_ACME_EMAIL=ops@example.com` | `--acme-email` |
| `MCP_SETUP_REGISTRY_MODE=bundled-https` | `--registry-mode` |
| `MCP_SETUP_PLATFORM_MODE=tenant` | `--platform-mode` |
| `MCP_SETUP_INGRESS=none` | `--ingress` |
| `MCP_SETUP_SKIP_CERT_MANAGER_INSTALL=1` | `--skip-cert-manager-install` |

Deeper guides: [Cluster readiness](cluster-readiness.md),
[Deployment targets](deployment-targets.md), and
[Getting started](getting-started.md#4-production-style-install).

## update

**[Operator]** Update an installed MCP Runtime platform to a release.

```bash
mcp-runtime update --to v0.5.0 --dry-run
mcp-runtime update --release-manifest ./platform-manifest.json
mcp-runtime update --to v0.5.0 --only ui,platform-api --yes
mcp-runtime update --release-manifest ./platform-manifest.json --include-auth --output json
```

The target comes from a release component manifest (service -> image
repository, tag, optional digest), selected with `--to` (fetches the manifest
attached to that GitHub release) or `--release-manifest` (local path or https
URL). update compares it with the images running in the cluster and patches
only the Deployments whose images changed, one at a time, waiting for each
rollout.

update only patches container images (and the operator's
`MCP_GATEWAY_PROXY_IMAGE` env var) plus version labels/annotations. It never
modifies Secrets, PVCs, ConfigMaps, cert-manager Issuers/Certificates, CRDs,
Services, or Ingresses, and never deletes or recreates workloads. mcp-auth and
cert-manager are skipped unless selected with `--include-auth`,
`--include-cert-manager`, or `--only`. Releases that change CRDs are refused; run
setup from that release instead.

The plan always shows the kube context and cluster ID. Without `--dry-run`,
update asks for confirmation (or requires `--yes` when not interactive).
Images must already be published to the registry the manifest resolves to;
relative repositories resolve against the registry of the running image.

| Flag | Default | Notes |
|---|---|---|
| `--to` | none | Target release version (for example v0.5.0); fetches that release's platform-manifest.json unless `--release-manifest` is set |
| `--release-manifest` | none | Release component manifest path or https URL; its version must match `--to` when both are set |
| `--dry-run` | `false` | Print the update plan and exit without changing the cluster |
| `--yes` | `false` | Apply without the interactive confirmation prompt |
| `--only` | all non-opt-in components | Comma-separated components to consider |
| `--include-auth` | `false` | Include the mcp-auth authorization server |
| `--include-cert-manager` | `false` | Include cert-manager images (patch releases only; CRDs are not upgraded) |
| `--allow-downgrade` | `false` | Allow a target version lower than the installed version |
| `--rollback-on-failure` | `true` | Restore previous images of workloads changed in this run if a rollout fails |
| `--timeout` | `5m0s` | Rollout wait timeout per workload |
| `--output` | `text` | Output format: text or json |
| `--kubeconfig`, `--context` | current kubeconfig context | Target cluster |

Components: `operator`, `gateway-proxy`, `platform-api`, `runtime-api`,
`analytics-api`, `ingest`, `processor`, `ui`, `doctor-smoke` (image only, no
workload), plus the opt-in `mcp-auth`, `cert-manager-controller`,
`cert-manager-webhook`, and `cert-manager-cainjector`. Changing
`gateway-proxy` makes the operator re-render MCPServer gateway sidecars, so
tenant MCP server pods restart.

Manifest shape (`platform-manifest.json`, attached to each GitHub release):

```json
{
  "apiVersion": "mcpruntime.org/v1alpha1",
  "kind": "PlatformRelease",
  "version": "v0.5.0",
  "registry": "",
  "crdChange": false,
  "components": [
    {"name": "platform-api", "repository": "mcp-platform-api", "tag": "v0.5.0", "digest": "sha256:..."}
  ]
}
```

A component is updated when its repository or tag differs, or when the manifest
digest differs from the digest in the spec or the digest the pods are running.
A digest in the manifest pins the workload to `repo:tag@digest`, so reruns are
exact no-ops. A downgrade is refused unless you pass `--allow-downgrade`. If a
rollout fails, update stops, restores the previous images by default, and
prints `kubectl rollout undo` / `kubectl set image` recovery commands. Required
RBAC: `get`/`list` on namespaces, deployments, and pods, and `patch` on
deployments.

## cluster

**[Operator]**

> Full guide: [Deployment targets](deployment-targets.md)

```bash
mcp-runtime cluster init
mcp-runtime cluster config --ingress traefik
mcp-runtime cluster provision --provider kind --nodes 3
mcp-runtime cluster provision --provider eks --name prod-mcp

mcp-runtime cluster cert status
mcp-runtime cluster cert apply
mcp-runtime cluster cert wait --timeout 10m

mcp-runtime cluster doctor                                   # pre-setup readiness
KUBECONFIG=~/.kube/config mcp-runtime cluster diagnostics    # post-setup diagnostic
```

## Further reading

| Topic | Link |
|---|---|
| Build, push, deploy flow | [Publish an MCP Server](publish-mcp-server.md) |
| MCPServer, MCPAccessGrant, MCPAgentSession fields | [API reference](api.md) |
| HTTP proxy and stdio adapter | [Agent adapters](agent-adapters.md) |
| Multi-team namespaces and RBAC | [Multi-team isolation](multi-team.md) |
| Sentinel logs, events, restart | [Sentinel](sentinel.md) |
| Distro-specific cluster prerequisites | [Cluster readiness](cluster-readiness.md) |
| Kind, EKS, k3s deployment | [Deployment targets](deployment-targets.md) |
