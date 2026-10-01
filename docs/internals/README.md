# Internals

These pages describe how the MCP Runtime codebase is organized: the CLI, operator, Kubernetes API types, Sentinel services, manifests, and tests. Read them before changing one of those areas.

For platform usage, start with the [user docs](../README.md). This section is
for contributors who need to understand package boundaries, runtime flows, and
the checks that protect each subsystem.

## Mental model

MCP Runtime is a Kubernetes-native control plane for MCP servers. Most changes touch one of four surfaces:

1. The CLI turns user intent into Kubernetes manifests, registry actions, cluster checks, and API calls.
2. The CRDs define the durable contract: `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession`.
3. The operator reconciles those CRDs into Deployments, Services, Ingress routes, status, and policy materialization.
4. Sentinel services provide the runtime gateway, governance APIs, analytics ingest, processing, and UI.

```mermaid
flowchart LR
    User[User or automation] --> CLI[mcp-runtime CLI]
    CLI --> K8s[Kubernetes API]
    CLI --> Registry[Container registry]
    K8s --> CRDs[MCP Runtime CRDs]
    CRDs --> Operator[Operator controller]
    Operator --> Workloads[MCP server Deployments and Services]
    Operator --> Ingress[Ingress and gateway routes]
    Operator --> Policy[Grant/session policy state]
    Client[MCP client] --> Gateway[Sentinel gateway]
    Gateway --> Policy
    Gateway --> Workloads
    Gateway --> Ingest[Analytics ingest]
    Ingest --> Processor[Processor]
    Processor --> Store[(ClickHouse/Postgres)]
    UI[Sentinel UI] --> Ingress[Traefik /api/v1]
    Ingress --> PlatAPI[platform-api]
    Ingress --> RunAPI[runtime-api]
    Ingress --> AnaAPI[analytics-api]
    RunAPI --> K8s
    PlatAPI --> Store
    AnaAPI --> Store
```

## Repository map

| Area | Start here | Why it matters |
|---|---|---|
| CLI entrypoint | [`cmd-mcp-runtime.md`](cmd-mcp-runtime.md) | Shows how the binary starts, wires foldered Cobra commands, and reports errors. |
| CLI implementation | [`internal-cli.md`](internal-cli.md) | Covers the `internal/cli/root` routing layer plus setup, bootstrap, registry, server, access, adapter, auth, team, status, and sentinel behavior. |
| Kubernetes API types | [`api-types.md`](api-types.md) | Defines the public CRD shapes consumed by users, tests, and the operator. |
| Request flows | [`request-flows.md`](request-flows.md) | Maps CLI, UI/API, registry, adapter, MCP runtime, policy, analytics, tenancy, and pre-release paths to components and E2E scenarios. |
| Sentinel API services | [`../sentinel.md`](../sentinel.md) | Three-service split (platform-api, runtime-api, analytics-api): Traefik `/api/v1` routing, RBAC, `/internal/*` contracts, OpenAPI per service. |
| Generated Go reference | [`go-package-reference.md`](go-package-reference.md) | Captures `go doc` output for the main contributor-facing packages. |
| Agent adapter | `internal/agentadapter/`, `internal/cli/adapter/` | Streamable HTTP proxy behavior with session-bound client certificates; exposed via `mcp-runtime adapter proxy`. |
| Operator Secret access | [`operator-secret-access.md`](operator-secret-access.md) | Scoped tenant Secret permissions and the named public trust bundle exception. |
| Operator | [`cmd-operator.md`](cmd-operator.md) | Explains manager startup and reconciliation from desired state to Kubernetes resources. |
| Control-plane helpers | `pkg/controlplane/` | Shared MCPServer Kubernetes operations and status projection used outside HTTP/CLI glue. |
| Shared policy and events | `pkg/policy/`, `pkg/events/`, `pkg/clickhouse/` | Gateway policy contracts/evaluation plus Sentinel event envelopes and ClickHouse query/insert helpers. |
| Service and workload helpers | `pkg/serviceutil/`, `pkg/kubeworkload/` | Shared service HTTP/env/OTel helpers and restricted Kubernetes workload defaults. |
| API service internals | `services/runtime-api/internal/runtimeapi/`, `services/platform-api/internal/platformstore/`, `pkg/apihttp/`, `pkg/platformauth/`, `pkg/internalapi/` | Split API modules: runtime HTTP/Kubernetes orchestration, platform Postgres persistence, shared HTTP contract helpers. |
| Metadata helpers | [`pkg-metadata.md`](pkg-metadata.md) | Covers `.mcp` metadata loading, host resolution, and CRD generation helpers. |
| Manifests and examples | [`config-and-examples.md`](config-and-examples.md) | Explains Kustomize overlays, registry/ingress config, and example MCP servers. |
| Tests | [`tests.md`](tests.md) | Maps unit, golden, integration, and QA E2E coverage. |

## Control-plane flow

A normal deployment starts in the CLI, passes through the Kubernetes API, and is completed by reconciliation.

```mermaid
sequenceDiagram
    participant U as User
    participant CLI as mcp-runtime CLI
    participant K as Kubernetes API
    participant O as Operator
    participant R as Registry
    participant W as Workloads

    U->>CLI: setup / server deploy (platform API) or server apply --use-kube
    CLI->>K: apply CRDs, manifests, MCPServer (platform API or --use-kube)
    CLI->>R: build or push images when requested
    K-->>O: watch MCPServer changes
    O->>K: create or update Deployment, Service, Ingress
    O->>K: update MCPServer status
    K-->>CLI: status and resource queries
    O-->>W: desired state becomes running pods
```

When changing this path, check the relevant CLI command, the `api/v1alpha1` contract, the operator reconciliation code, and at least one test that proves the generated or reconciled resource shape.

## Runtime request flow

At request time, clients do not call server pods directly. Traffic flows through the gateway, which validates OAuth and checks grants and sessions before forwarding MCP JSON-RPC calls. The optional agent adapter runs beside a client that needs a local HTTP proxy; it forwards OAuth and adds identity from a verified client certificate.

```mermaid
flowchart TD
    Client[MCP client] --> Adapter[Optional HTTP adapter]
    Adapter --> Route[Ingress route]
    Client --> Route
    Route --> Gateway[Sentinel gateway or proxy]
    Gateway --> Authz[pkg/policy evaluation]
    Authz -->|allow| Server[MCP server pod]
    Authz -->|deny| Deny[JSON-RPC error]
    Gateway --> Events[Audit and analytics event]
    Events --> Ingest[services/ingest]
    Ingest --> Processor[services/processor]
    Processor --> Analytics[(analytics store)]
    RuntimeAPI[runtime-api] --> Grants[MCPAccessGrant]
    RuntimeAPI --> Sessions[MCPAgentSession]
    Grants --> Authz
    Sessions --> Authz
```

Governance-related changes usually span `api/v1alpha1/access_types.go`, `pkg/access/`, `pkg/policy/`, `services/runtime-api`, `services/mcp-gateway`, `services/ingest`, and the e2e policy scenarios.

## Package dependency guide

```mermaid
flowchart TB
    Cmd[cmd/mcp-runtime] --> CLIRoot[internal/cli/root]
    CLIRoot --> CLIAdapter
    CLIAdapter[internal/cli/adapter] --> AgentAdapter[internal/agentadapter]
    CLIRoot --> CLICommands[internal/cli/<command>]
    CLICommands --> CLICore[internal/cli/core]
    CLICommands --> Metadata[pkg/metadata]
    CLICommands --> Manifest[pkg/manifest]
    CLICommands --> K8sClient[pkg/k8sclient]
    CLICommands --> Access[pkg/access]
    CmdOp[cmd/operator] --> Operator[internal/operator]
    Operator --> API[api/v1alpha1]
    Operator --> ControlPlane[pkg/controlplane]
    Operator --> Policy[pkg/policy]
    Operator --> Workload[pkg/kubeworkload]
    Operator --> K8sClient
    Operator --> Manifest
    Operator --> Access
    Services[services/*] --> ServiceUtil[pkg/serviceutil]
    Services --> ControlPlane
    Services --> Access
    Services --> ClickHouse[pkg/clickhouse]
    Services --> Events[pkg/events]
    Gateway[services/mcp-gateway] --> Policy
    PlatformAPI[services/platform-api] --> Workload
    RuntimeAPI[services/runtime-api] --> RuntimeAPIPkg[services/runtime-api/internal/runtimeapi]
    PlatformAPI --> PlatformStore[services/platform-api/internal/platformstore]
    PlatformAPI --> APIAuth[services/platform-api/internal/apiauth]
    ClickHouse --> Events
    ControlPlane --> API
    ControlPlane --> K8sClient
    Metadata --> API
```

Keep shared behavior in `pkg/` only when multiple binaries or services need it. CLI top-level command routing belongs in `internal/cli/root` and `internal/cli/<command>`; CLI-only shared infrastructure belongs in `internal/cli/core`; reconciliation behavior belongs in `internal/operator`; Kubernetes-facing MCPServer operations that are reused outside the operator belong in `pkg/controlplane`; rendered gateway policy evaluation belongs in `pkg/policy`; reusable Sentinel event and storage contracts belong in `pkg/events` and `pkg/clickhouse`; shared pod hardening defaults belong in `pkg/kubeworkload`; runtime orchestration belongs in `services/runtime-api/internal/runtimeapi`; platform identity, team, key, and audit persistence belongs in `services/platform-api/internal/platformstore`; API principal context helpers belong in `services/platform-api/internal/apiauth`; HTTP service glue belongs near the service that owns the endpoint unless it is repeated across services.

## Learning path

1. Read [API types](api-types.md) first. The CRDs are the contract that every other subsystem follows.
2. Read [CLI internals](internal-cli.md) and [cmd/mcp-runtime](cmd-mcp-runtime.md) to see how users create, inspect, and deploy resources.
3. Read [operator internals](cmd-operator.md) to understand how `MCPServer` state becomes Kubernetes workloads and ingress.
4. Read [config and examples](config-and-examples.md), then run or inspect the example server manifests.
5. Read [request flows](request-flows.md) when a change crosses CLI, UI, API, registry, gateway, policy, analytics, or tenant boundaries.
6. Read [tests](tests.md) before making changes; it shows the fastest feedback loop and the broader CI safety net.
7. Use the change playbooks below to choose the narrowest useful tests before
   broadening to full CI coverage.

## Refreshing Package Reference

These pages are contributor guides. The Go package reference is generated
into [Go Package Reference](go-package-reference.md). Refresh it from the current
checkout with:

```bash
python3 docs/scripts/generate_go_package_reference.py
```

Use the generated output to verify exported types, functions, and comments. Keep
the narrative internals pages focused on stable contracts and contributor
workflows.

## Change playbooks

| Change | Read first | Verify with |
|---|---|---|
| Add or change a CLI flag | `internal/cli/root`, `internal/cli/<command>`, `internal/cli/core`, `cmd/mcp-runtime`, golden CLI tests | `go test ./internal/cli/... ./test/golden/... -count=1` |
| Change a CRD field | `api/v1alpha1`, CRD YAML, operator reconciliation, docs/API reference | `go test ./api/v1alpha1/... ./internal/operator/... -count=1` |
| Change generated manifests | `pkg/metadata`, `pkg/manifest`, `config/`, examples | targeted package tests plus manifest diff review |
| Change reconciliation behavior | `internal/operator`, API types, k8s helpers | `go test ./internal/operator/... -race -count=1` |
| Change governance policy | `pkg/access`, `pkg/policy`, `services/runtime-api`, `services/mcp-gateway`, access CRDs | targeted package/service tests plus e2e policy scenario |
| Change agent adapters | `internal/agentadapter`, `internal/cli/adapter`, `docs/agent-adapters.md` | `go test ./internal/agentadapter ./internal/cli/adapter -count=1` |
| Change team provisioning or membership | `internal/cli/team`, `services/runtime-api/internal/runtimeapi`, `services/platform-api/internal/platformstore`, `docs/multi-team.md` | `go test ./internal/cli/team -count=1` plus service API tests inside `services/platform-api` and `services/runtime-api` |
| Change Sentinel event storage | `pkg/events`, `pkg/clickhouse`, `services/ingest`, `services/processor`, `services/analytics-api`, `services/mcp-gateway` | package tests plus touched service tests |
| Change docs site behavior | `docs/mkdocs.yml`, `docs/nginx.conf`, Markdown pages | MkDocs build or docs container build |

## Contributor checklist

Before opening a change, confirm:

- The code follows the closest existing package pattern.
- Public behavior is reflected in `docs/`, `README.md`, or `AGENTS.md` when relevant.
- CRD changes update both Go types and generated YAML.
- CLI help changes update golden snapshots intentionally.
- Narrow tests pass for touched packages.
- Full `go test ./... -count=1 -race` is run before merge when the change touches shared behavior.
