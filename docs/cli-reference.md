# CLI reference

Examples on this page use the example servers in the repository.
Access and adapter examples show a sample managed agent ID
(`agt_01arz3ndektsv4rrffq69g5fav`). For live commands, replace it with an
active ID from `mcp-runtime agent list <team-slug> --status active`; display
names such as `cursor` are not IDs. A team owner or admin can create an agent
with `mcp-runtime agent create <team-slug> --name <name>`.

## Quick reference

| Goal | Commands |
|---|---|
| Log in | `auth login` → `auth use <profile>` |
| Deploy a server | `server init` → `server validate` → `server build image` → `server push` → `server deploy` |
| Grant an agent access | `access grant init` → `server validate --grant-file` → `access grant apply` |
| Create a session manually | `access session init` → `access session apply` |
| Connect an MCP client | `adapter proxy --server ... --agent ... --auto-refresh` |
| Find a tool | `catalog tools` · `catalog tool <name>` |
| Create a team and assign users | `team create` → `team user create` or `team user add` |
| Check platform health | `status` |
| Inspect a running server | `server list` · `server get` · `server policy inspect` |
| View analytics logs | `sentinel status` · `sentinel logs api` |
| Check setup readiness | `cluster doctor` |
| Diagnose an installed cluster | `cluster diagnostics` |
| Check an OIDC provider before mcp-auth | `auth provider-check` |

**Example servers in this repo:**

| Server | Language | Run command | Tools |
|---|---|---|---|
| `oauth-example-go-2025-11-25-gateway` | Go | `go run .` | `whoami`, `aaa-ping`, `echo`, `add`, `upper`, `lower`, `slugify`, `create_task`, `draft_release_note` |
| `example-python-2025-11-25-gateway` | Python | `python app.py` | `echo`, `add`, `multiply`, `upper`, `lower`, `ping`, `reverse` |
| `example-rust-2025-11-25-gateway` | Rust | `cargo run` | `repeat`, `word_count`, `extract_keywords` |
| `oauth-example-typescript-2025-06-18-standalone` | TypeScript | See [the example README](https://github.com/mcp-runtime/mcp-runtime/blob/main/examples/oauth-example-typescript-2025-06-18/README-oauth-example-typescript-2025-06-18.md) | `whoami` (server-side OAuth) |

The Go, Python, and Rust examples listen on `http://localhost:8088/mcp` by
default. The Go server validates OAuth tokens directly when OAuth settings are
configured and runs without server auth when they are omitted. The TypeScript
OAuth example listens on port `8081` and uses its standalone server-side OAuth
route. Gateway-enabled examples apply Runtime grants, sessions, policy, and
gateway audit; standalone OAuth validates tokens in the MCP server itself.
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
| `auth provider-check` | Operator | Inspect an OIDC provider before `setup --with-mcp-auth-server` | [MCP authorization](mcp-oauth.md) |
| `status` | User | Platform health at a glance | [status](#status) |
| `catalog` | User | Search tools across visible servers | [catalog](#catalog) |
| `server` | User / Admin | Scaffold, validate, build, push, deploy, manage | [Publish a server](publish-mcp-server.md) |
| `registry` | Operator | Inspect or configure a registry | [registry](#registry) |
| `access` | User / Admin | Grants and sessions for gateway policy | [API reference](api-reference.md) |
| `adapter` | User | Certificate-authenticated HTTP proxy and enrollment for agents | [Agent adapter](connect-clients.md) |
| `team` | Admin | Create teams and add password users | [Multi-team](teams-and-access.md) |
| `sentinel` | Operator | Inspect and operate the analytics stack | [Platform services](platform-services.md) |
| `bootstrap` | Operator | Pre-install cluster checks | [Cluster Requirements](cluster-readiness.md) |
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

See [MCP authorization](mcp-oauth.md) for the full provider flow.

## status

**[User]** (saved platform login required)

```bash
mcp-runtime status                                         # quick platform API check
mcp-runtime registry status                               # registry pod + endpoint
KUBECONFIG=~/.kube/config mcp-runtime sentinel status     # sentinel stack
```

`status` checks whether the platform API is reachable and accepts your saved
login, with a five-second timeout. It prints the platform URL and `READY`,
`NOT READY`, `LOGIN REQUIRED`, or `NOT CONFIGURED`; a failed check exits nonzero.
`READY` means the authenticated API request succeeded. Use `server list` for
servers and `cluster status` or `sentinel status` for cluster and workload health.
The command does not query Kubernetes or list servers.

## server

**[User]** by default, **[Admin]** with `--use-kube`

> Full guide: [Publish an MCP Server](publish-mcp-server.md)

The developer flow: **init → validate → build → push → deploy**.

### server init

`server init` creates `.mcp/servers.yaml` with tool names, trust levels, side effects,
and policy. Tool names must exactly match what your server implements.

Use `--from-server` to discover them from a running local instance:

```bash
# oauth-example-go-2025-11-25-gateway (Go)
cd examples/oauth-example-go-2025-11-25
go run . &
SERVER_PID=$!
mcp-runtime server init workspace-demo --from-server http://localhost:8088
kill $SERVER_PID
# Discovered: aaa-ping, add, create_task, draft_release_note, echo, lower, slugify, upper
```

```bash
# example-python-2025-11-25 (Python)
cd examples/example-python-2025-11-25
pip install "mcp[cli]"
python app.py &
SERVER_PID=$!
mcp-runtime server init data-util --from-server http://localhost:8088
kill $SERVER_PID
# Discovered: add, echo, lower, multiply, ping, reverse, upper
```

```bash
# example-rust-2025-11-25 (Rust)
cd examples/example-rust-2025-11-25
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
cd examples/oauth-example-go-2025-11-25
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

### Full example: oauth-example-go-2025-11-25-gateway

```bash
cd examples/oauth-example-go-2025-11-25

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

> Full reference: [API reference](api-reference.md)

Tool names in grants must exactly match `.mcp/servers.yaml`. Run
`server validate --grant-file grant.yaml` before applying to catch mismatches
that cause `tool_side_effect_unknown` at the gateway.

### Grants

```bash
# Allow specific tools (read side-effect, low trust)
mcp-runtime access grant init workspace-ops \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
  --tool echo \
  --tool add \
  --tool upper \
  --output grant.yaml

# Mixed allow/deny rules: --tool-rule name:allow|deny:low|medium|high
mcp-runtime access grant init workspace-ops \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
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
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
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
  --agent-id agt_01arz3ndektsv4rrffq69g5fav \
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
    --agent-id agt_01arz3ndektsv4rrffq69g5fav \
    --trust low \
    --expires-in 4h \
    --output session-cross.yaml
MCP_PLATFORM_API_PROFILE=admin \
  mcp-runtime access session apply --file session-cross.yaml
```

See [Multi-team isolation](teams-and-access.md).

The grant expiry is the maximum lifetime of the delegation. Any session issued
from it expires no later than that time, and `--auto-refresh` cannot renew a
session after the grant expires. Use `--expires-at <RFC3339 timestamp>` when a
fixed deadline is more appropriate than a duration.

## adapter

**[User]**

> Full guide: [Agent adapters](connect-clients.md)

The adapter identifies its enrolled session with a client certificate. When the
target configures OAuth (`spec.auth`), it also forwards the local MCP client's
bearer token. Use `adapter enroll` to save a certificate under
`MCP_RUNTIME_CONFIG_DIR/certs` (default `~/.mcpruntime/certs`), or let `proxy`
enroll one in memory at startup with `--server` and `--agent`. The gateway
derives session identity from the verified certificate and, on OAuth-enabled
targets, requires its human identity to match the OAuth subject. Governance
identity headers are not supported.

The platform issues certificates only when an enabled `MCPAccessGrant` matches the server, signed-in user, and agent. Apply the grant first with `access grant apply`.

`--platform-url` takes scheme and host only, with no `/api` path. It defaults to the URL saved by `auth login` or `$MCP_PLATFORM_API_URL`.

```bash
# Enroll and save client.crt, client.key, and ca.crt under ~/.mcpruntime/certs
mcp-runtime adapter enroll \
  --platform-url https://platform.example.com \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent agt_01arz3ndektsv4rrffq69g5fav \
  --trust-domain mcpruntime.org

# Run with an in-memory certificate and refresh it before expiry
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --server workspace-demo \
  --namespace mcp-team-acme \
  --agent agt_01arz3ndektsv4rrffq69g5fav \
  --auto-refresh \
  --listen 127.0.0.1:8099

# Reuse files produced by enroll
mcp-runtime adapter proxy \
  --runtime-url https://mcp.example.com/workspace-demo/mcp \
  --tls-client-cert ~/.mcpruntime/certs/<scope>/client.crt \
  --tls-client-key ~/.mcpruntime/certs/<scope>/client.key \
  --tls-ca-bundle ~/.mcpruntime/certs/<scope>/ca.crt
```

The enrollment output prints the actual certificate directory and TLS file
paths. Private keys use mode `0600`; the certificate directory uses mode
`0700`. Set `MCP_RUNTIME_CONFIG_DIR` to store them under another config root.

The local MCP client normally sends OAuth `Authorization` through the proxy.
`--auth-header` (or `$MCP_RUNTIME_AUTH_HEADER`) supplies a static value such as
`Bearer <token>` for clients that cannot attach one. It does not set adapter
identity. Certificate identity requires an `https` runtime URL.

See [Agent adapters](connect-clients.md#enterprise-mtls-and-spiffe) for gateway and Traefik certificate setup.

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
and view active agents covered by an applicable grant or their own active
session. The administration workspace includes an
**Agents** directory page.
Agent subjects must be selected from the active directory for the subject's
team. The API rejects unknown, malformed, inactive, and wrong-team agent IDs.

## team

Platform admins create teams. A team owner can add users to their own team
or update their team role.

> Full guide: [Multi-team isolation](teams-and-access.md)

```bash
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team list

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team create acme --name "Acme Corp"

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user create acme \
  --email alice@acme.com --password '...' --role owner

# Add an existing platform user to a team or update their team role. Use their
# immutable user ID; their password stays unchanged.
MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user add acme <user-id> --role member

MCP_PLATFORM_API_PROFILE=admin mcp-runtime team user list acme
```

A saved login token keeps the membership from the moment it was issued. Run
`mcp-runtime auth login` again before `server build`, `server push`, or
`server deploy` for that team.

Team users log in with:

```bash
mcp-runtime auth login \
  --api-url https://platform.example.com \
  --email alice@acme.com --password '...' \
  --profile alice
```

`team user create` creates a new password-login account. If the email already
exists, use `team user add` with the existing user's ID. Team owners can manage
users in their own teams. `team init` is deprecated; use `team create`.

## sentinel

**[Operator]** Requires `KUBECONFIG` with cluster-admin RBAC.

> Full guide: [Platform services](platform-services.md)

```bash
KUBECONFIG=~/.kube/config mcp-runtime sentinel status
KUBECONFIG=~/.kube/config mcp-runtime sentinel events

# Logs support --follow, --tail, --since, and --previous
KUBECONFIG=~/.kube/config mcp-runtime sentinel logs api --since 15m --follow
KUBECONFIG=~/.kube/config mcp-runtime sentinel logs ingest --tail 200

# Restart
KUBECONFIG=~/.kube/config mcp-runtime sentinel restart gateway
KUBECONFIG=~/.kube/config mcp-runtime sentinel restart --all

# Grafana admin credential drift: read-only check, then deliberate recovery
KUBECONFIG=~/.kube/config mcp-runtime sentinel grafana check
KUBECONFIG=~/.kube/config mcp-runtime sentinel grafana reset-admin-password --yes

# Port-forward a component locally
KUBECONFIG=~/.kube/config mcp-runtime sentinel port-forward ui
KUBECONFIG=~/.kube/config mcp-runtime sentinel port-forward grafana
```

`sentinel events` lists operator, platform, observability, and log collector
events by namespace so failures in the telemetry stack remain visible.

Component names for `logs` and `restart`:
`clickhouse`, `kafka`, `ingest`, `processor`, `api`, `ui`,
`gateway`, `prometheus`, `grafana`, `otel-collector`, `tempo`, `loki`, `promtail`

## bootstrap

**[Operator]** Run before `setup` on a fresh cluster.

> Full guide: [Cluster Requirements](cluster-readiness.md)

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
certificates; name your enterprise issuer, or use a pre-provisioned bundled
`mcp-runtime-ca` in production. Only `--test-mode` generates a missing bundled
CA and defaults the workload issuer to `mcp-runtime-ca`. Full
flow: [Agent adapters](connect-clients.md#enterprise-mtls-and-spiffe).

#### Bundled workload CA lifecycle

When `--mtls-cluster-issuer mcp-runtime-ca` is selected, setup validates the
`cert-manager/mcp-runtime-ca` Secret before accepting it: the certificate and
key must match, the certificate must be a CA with `keyCertSign`, and it must be
inside its validity window. Setup never prints key material.

- **Production** setup fails if the Secret is missing (it will not mint a new
  root silently), invalid, expired, or has under 180 days remaining. Prefer an
  enterprise issuer for production.
- **Test mode** generates a missing CA, still fails on invalid or expired CAs,
  and only warns when the root is near expiry.
- cert-manager does not rotate a CA Secret or reissue leaves when it changes.
  Rotate with overlap: back up the current Secret to encrypted off-host storage
  first; create the new root and publish a trust bundle containing both roots;
  switch the issuer to the new root and reissue leaf certificates
  (adapter, gateway/Traefik, registry); remove the old root only after every
  leaf is reissued. Restore a lost Secret from the encrypted backup, then re-run
  setup to re-validate. Limit access to the Secret in `cert-manager`.
- The bundled registry/internal TLS still shares this root. Splitting the
  workload and registry signing roots is tracked under #535 and is not yet done.

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
[MCP authorization](mcp-oauth.md).

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

Deeper guides: [Cluster Requirements](cluster-readiness.md),
[Deployment targets](deployment-targets.md), and
[Getting started](self-hosting.md#4-production-style-install).

## update

**[Operator]** Update an installed MCP Runtime platform to a release.

```bash
mcp-runtime update --to v0.5.0 --dry-run
mcp-runtime update --release-manifest ./platform-manifest.json
mcp-runtime update --to v0.5.0 --only ui,platform-api --yes
mcp-runtime update --release-manifest ./platform-manifest.json --crds ./platform-crds.yaml --yes
mcp-runtime update --to v0.3.1 --release-manifest ./platform-manifest.json --build --source . --yes
```

The target comes from a release component manifest (service -> image
repository, tag, optional digest), selected with `--to` (fetches the manifest
attached to that GitHub release) or `--release-manifest` (local path or https
URL). update compares it with the images running in the cluster and patches
only the Deployments whose images changed, one at a time, waiting for each
rollout. Every component the release changes is included; unchanged components
are left alone.

When a release sets `crdChange`, the published `platform-manifest.json` embeds
CustomResourceDefinition YAML in the `crds` field (and the release also ships
`platform-crds.yaml`). update applies only CRD objects whose live spec differs
from the release, waits until each written CRD is Established, then rolls
images. Pass `--crds` only when a local manifest omits the embedded bundle.
With `--only`, CRD apply is skipped so a scoped image update cannot mutate
cluster schemas; re-run without `--only` to apply schema changes. `--dry-run`
prints the plan only and does not apply CRDs or roll images. update never
modifies Secrets, PVCs, ConfigMaps, cert-manager Issuers/Certificates,
Services, or Ingresses, and never deletes or recreates workloads. mcp-auth and
cert-manager are skipped unless selected with `--include-auth`,
`--include-cert-manager`, or `--only`.

With `--build`, update builds and pushes only Built-component images from the
release plan that are missing from the registry (from `--source`, default `.`),
then rolls Deployments that still run an older tag. Builds run sequentially by
default (`--build-parallelism 1`); raise carefully. Failed builds retry once
and cancel sibling builds. Tags already in the registry are reused. Without
`--build`, images must already be published.

The plan always shows the kube context and cluster ID. Without `--dry-run`,
update asks for confirmation (or requires `--yes` when not interactive).
Relative repositories resolve against the registry of the running image.

| Flag | Default | Notes |
|---|---|---|
| `--to` | none | Target release version (for example v0.5.0); fetches that release's platform-manifest.json unless `--release-manifest` is set |
| `--release-manifest` | none | Release component manifest path or https URL; its version must match `--to` when both are set |
| `--crds` | none | CRD multi-document YAML path or https URL when the manifest omits embedded `crds` |
| `--dry-run` | `false` | Print the update plan and exit without changing the cluster |
| `--yes` | `false` | Apply without the interactive confirmation prompt |
| `--only` | all non-opt-in components | Comma-separated components to consider |
| `--include-auth` | `false` | Include the mcp-auth authorization server |
| `--include-cert-manager` | `false` | Include cert-manager images (patch releases only; CRDs are not upgraded) |
| `--allow-downgrade` | `false` | Allow a target version lower than the installed version |
| `--rollback-on-failure` | `true` | Restore previous images of workloads changed in this run if a rollout fails |
| `--timeout` | `5m0s` | Rollout wait timeout per workload |
| `--output` | `text` | Output format: text or json |
| `--build` | `false` | Build and push missing Built-component images from `--source` before rolling |
| `--source` | `.` | Repository root used with `--build` |
| `--image-platform` | `MCP_IMAGE_PLATFORM` or `linux/amd64` | Docker `--platform` for `--build` |
| `--build-parallelism` | `1` | Max concurrent image builds with `--build` |
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
  "crdChange": true,
  "crds": "apiVersion: apiextensions.k8s.io/v1\nkind: CustomResourceDefinition\n...",
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
RBAC: `get`/`list` on namespaces, deployments, and pods; `patch` on
deployments; and when `crdChange` is set, cluster-scoped
`get`/`create`/`update` on `customresourcedefinitions.apiextensions.k8s.io`
plus API discovery.

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
| MCPServer, MCPAccessGrant, MCPAgentSession fields | [API reference](api-reference.md) |
| Certificate-authenticated HTTP adapter | [Agent adapter](connect-clients.md) |
| Multi-team namespaces and RBAC | [Multi-team isolation](teams-and-access.md) |
| Platform service logs, events, restart | [Platform services](platform-services.md) |
| Distro-specific cluster prerequisites | [Cluster Requirements](cluster-readiness.md) |
| Kind, EKS, k3s deployment | [Deployment targets](deployment-targets.md) |
