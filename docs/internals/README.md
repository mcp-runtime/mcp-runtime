# Implementation Details

<span id="internals"></span>

These pages describe how the MCP Runtime codebase is organized: the CLI, operator, Kubernetes API types, platform services, manifests, and tests. Read them before changing one of those areas.

For platform usage, start with the [user docs](../README.md). This section is
for contributors who need to understand package boundaries, runtime flows, and
the checks that protect each subsystem.

## Reading path

Start with [API Types](api-types.md) and [Request Flows](request-flows.md), then
follow the component involved in your change. Use
[Development and Testing](../contributor/README.md) for the local contribution loop.

| Guide group | What it covers | Entry points |
|---|---|---|
| Contracts and Flows | Resource shapes, request paths, server metadata, and manifests | [API Types](api-types.md), [Request Flows](request-flows.md) |
| Components | CLI startup and commands, operator reconciliation, and workload inventory | [CLI Implementation](cli.md), [Operator](operator.md), [Component Inventory](component-inventory.md) |
| Security and Lifecycle | Credential consumers, Secret access, rollouts, and log collector admission | [Credential Ownership](credential-consumers.md), [Operator Secret Access](operator-secret-access.md), [Dependency Rollouts](dependency-rollouts.md) |
| Developer References | Package documentation, design background, and test coverage | [Go package docs](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime), [Tests and Coverage](testing.md) |

## Mental model

MCP Runtime is a Kubernetes-native control plane for MCP servers. Most changes touch one of four surfaces:

1. The CLI turns user intent into Kubernetes manifests, registry actions, cluster checks, and API calls.
2. The CRDs define the durable contract: `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession`.
3. The operator reconciles those CRDs into Deployments, Services, Ingress routes, status, and policy materialization.
4. Platform services provide the runtime gateway, governance APIs, analytics ingest, processing, and UI.

```mermaid
flowchart LR
    User[User or automation] --> CLI[mcp-runtime CLI]
    CLI --> RunAPI[runtime-api]
    CLI --> PlatAPI[platform-api]
    CLI -. setup or explicit admin Kubernetes mode .-> K8s[Kubernetes API]
    CLI --> Registry[Container registry]
    K8s --> CRDs[MCP Runtime CRDs]
    CRDs --> Operator[Operator controller]
    Operator --> Workloads[MCP server Deployments and Services]
    Operator --> Ingress[Ingress and gateway routes]
    Operator --> Policy[Grant/session policy state]
    Client[MCP client] --> Gateway[mcp-gateway]
    Gateway --> Policy
    Gateway --> Workloads
    Gateway --> Ingest[Analytics ingest]
    Ingest --> Kafka[(Kafka)]
    Kafka --> Processor[Processor]
    Processor --> Store[(ClickHouse)]
    DB[(Postgres)]
    UI[Platform UI] --> Ingress[Traefik /api/v1]
    Ingress --> PlatAPI[platform-api]
    Ingress --> RunAPI[runtime-api]
    Ingress --> AnaAPI[analytics-api]
    RunAPI --> K8s
    PlatAPI --> DB
    AnaAPI --> Store
```

## Repository map

| Area | Start here | Why it matters |
|---|---|---|
| CLI entrypoint | [`cli-entrypoint.md`](cli-entrypoint.md) | Shows how the binary starts, wires foldered Cobra commands, and reports errors. |
| CLI implementation | [`cli.md`](cli.md) | Covers the `internal/cli/root` routing layer plus setup, bootstrap, registry, server, access, adapter, auth, team, status, and sentinel behavior. |
| Kubernetes API types | [`api-types.md`](api-types.md) | Defines the public CRD shapes consumed by users, tests, and the operator. |
| Request flows | [`request-flows.md`](request-flows.md) | Maps CLI, UI/API, registry, adapter, MCP runtime, policy, analytics, tenancy, and pre-release paths to components and E2E scenarios. |
| Platform API services | [`../platform-services.md`](../platform-services.md) | Three-service split (platform-api, runtime-api, analytics-api): Traefik `/api/v1` routing, RBAC, `/internal/*` contracts, OpenAPI per service. |
| Go package docs | [pkgsite](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime) | Browse the full source tree with package indexes, symbols, and source links. See [`pkgsite.md`](pkgsite.md) for hosting. |
| Agent adapter | `internal/agentadapter/`, `internal/cli/adapter/` | Streamable HTTP proxy behavior with session-bound client certificates; exposed via `mcp-runtime adapter proxy`. |
| Operator Secret access | [`operator-secret-access.md`](operator-secret-access.md) | Scoped tenant Secret permissions and the named public trust bundle exception. |
| Operator | [`operator.md`](operator.md) | Explains manager startup and reconciliation from desired state to Kubernetes resources. |
| Component inventory | [`component-inventory.md`](component-inventory.md) | Stable workload identity, ownership and validated namespace layout resolution. |
| Control-plane helpers | `pkg/controlplane/` | Shared MCPServer Kubernetes operations and status projection used outside HTTP/CLI glue. |
| Shared policy and events | `pkg/policy/`, `pkg/events/`, `pkg/clickhouse/` | Gateway policy contracts/evaluation plus event envelopes and ClickHouse query/insert helpers. |
| Service and workload helpers | `pkg/serviceutil/`, `pkg/kubeworkload/` | Shared service HTTP/env/OTel helpers and restricted Kubernetes workload defaults. |
| API service internals | `services/runtime-api/internal/runtimeapi/`, `services/platform-api/internal/platformstore/`, `pkg/apihttp/`, `pkg/platformauth/`, `pkg/internalapi/` | Split API modules: runtime HTTP/Kubernetes orchestration, platform Postgres persistence, shared HTTP contract helpers. |
| Metadata helpers | [`metadata.md`](metadata.md) | Covers `.mcp` metadata loading, host resolution, and CRD generation helpers. |
| Manifests and examples | [`config-and-examples.md`](config-and-examples.md) | Explains Kustomize overlays, registry/ingress config, and example MCP servers. |
| Tests | [`testing.md`](testing.md) | Maps unit, golden, integration, and QA E2E coverage. |

## Control-plane flow

A normal server deployment starts in the CLI, passes through runtime-api and
the Kubernetes API, and is completed by reconciliation. Setup and explicit
admin Kubernetes mode write to Kubernetes directly.

```mermaid
sequenceDiagram
    participant U as User
    participant CLI as mcp-runtime CLI
    participant API as runtime-api
    participant K as Kubernetes API
    participant O as Operator
    participant R as Registry
    participant W as Workloads

    U->>CLI: setup / server deploy (platform API) or server apply --use-kube
    alt normal server deploy
        CLI->>API: Authenticated MCPServer request
        API->>K: Apply MCPServer after authorization
    else setup or explicit admin Kubernetes mode
        CLI->>K: Apply CRDs or selected manifests
    end
    CLI->>R: build or push images when requested
    K-->>O: watch MCPServer changes
    O->>K: create or update Deployment, Service, Ingress
    O->>K: update MCPServer status
    K-->>CLI: status and resource queries
    O-->>W: desired state becomes running pods
```

When changing this path, check the relevant CLI command, the `api/v1alpha1` contract, the operator reconciliation code, and at least one test that proves the generated or reconciled resource shape.

## Runtime request flow

With the gateway enabled, ingress sends requests through the gateway before
they reach the application. OAuth validation applies when `spec.auth` is set;
grant/session enforcement applies to allow-list policy. Observe mode forwards
calls without those policy checks. With the gateway explicitly disabled, the
application handles authentication and authorization itself. The optional agent adapter runs beside a client that needs a local HTTP proxy; it forwards OAuth and adds identity from a verified client certificate.

```mermaid
flowchart TD
    Client[MCP client] --> Adapter[Optional HTTP adapter]
    Adapter --> Route[Ingress route]
    Client --> Route
    Route --> Gateway[mcp-gateway sidecar]
    Gateway --> Authz[pkg/policy evaluation]
    Authz -->|allow| Server[MCP server pod]
    Authz -->|deny| Deny[JSON-RPC error]
    Gateway --> Events[Audit and analytics event]
    Events --> Ingest[services/ingest]
    Ingest --> Kafka[(Kafka)]
    Kafka --> Processor[services/processor]
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
    CLIRoot --> CLICommands[CLI command packages]
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
    RuntimeAPI[services/runtime-api] --> Workload
    RuntimeAPI[services/runtime-api] --> RuntimeAPIPkg[services/runtime-api/internal/runtimeapi]
    PlatformAPI --> PlatformStore[services/platform-api/internal/platformstore]
    PlatformAPI --> APIAuth[services/platform-api/internal/apiauth]
    ClickHouse --> Events
    ControlPlane --> API
    ControlPlane --> K8sClient
    Metadata --> API
```

Keep shared behavior in `pkg/` only when multiple binaries or services need it. CLI top-level command routing belongs in `internal/cli/root` and `internal/cli/<command>`; CLI-only shared infrastructure belongs in `internal/cli/core`; reconciliation behavior belongs in `internal/operator`; Kubernetes-facing MCPServer operations that are reused outside the operator belong in `pkg/controlplane`; rendered gateway policy evaluation belongs in `pkg/policy`; reusable event and storage contracts belong in `pkg/events` and `pkg/clickhouse`; shared pod hardening defaults belong in `pkg/kubeworkload`; runtime orchestration belongs in `services/runtime-api/internal/runtimeapi`; platform identity, team, key, and audit persistence belongs in `services/platform-api/internal/platformstore`; API principal context helpers belong in `services/platform-api/internal/apiauth`; HTTP service glue belongs near the service that owns the endpoint unless it is repeated across services.

## Learning path

1. Read [API types](api-types.md) first. The CRDs are the contract that every other subsystem follows.
2. Read [CLI internals](cli.md) and [cmd/mcp-runtime](cli-entrypoint.md) to see how users create, inspect, and deploy resources.
3. Read [operator internals](operator.md) to understand how `MCPServer` state becomes Kubernetes workloads and ingress.
4. Read [config and examples](config-and-examples.md), then run or inspect the example server manifests.
5. Read [request flows](request-flows.md) when a change crosses CLI, UI, API, registry, gateway, policy, analytics, or tenant boundaries.
6. Read [tests](testing.md) before making changes; it shows the fastest feedback loop and the broader CI safety net.
7. Use the change playbooks below to choose the narrowest useful tests before
   broadening to full CI coverage.

## Package Reference

These pages are contributor guides. For exported types, functions, and doc
comments, use [pkgsite](https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime),
which is rebuilt from `main`, or run `go doc <package>` against your checkout.
Keep the narrative internals pages focused on stable contracts and contributor
workflows.

## Change playbooks

| Change | Read first | Verify with |
|---|---|---|
| Add or change a CLI flag | `internal/cli/root`, `internal/cli/<command>`, `internal/cli/core`, `cmd/mcp-runtime`, golden CLI tests | `go test ./internal/cli/... ./test/golden/... -count=1` |
| Change a CRD field | `api/v1alpha1`, CRD YAML, operator reconciliation, docs/API reference | `go test ./api/v1alpha1/... ./internal/operator/... -count=1` |
| Change generated manifests | `pkg/metadata`, `pkg/manifest`, `config/`, examples | targeted package tests plus manifest diff review |
| Change reconciliation behavior | `internal/operator`, API types, k8s helpers | `go test ./internal/operator/... -race -count=1` |
| Change governance policy | `pkg/access`, `pkg/policy`, `services/runtime-api`, `services/mcp-gateway`, access CRDs | targeted package/service tests plus e2e policy scenario |
| Change agent adapters | `internal/agentadapter`, `internal/cli/adapter`, `docs/connect-clients.md` | `go test ./internal/agentadapter ./internal/cli/adapter -count=1` |
| Change team provisioning or membership | `internal/cli/team`, `services/runtime-api/internal/runtimeapi`, `services/platform-api/internal/platformstore`, `docs/teams-and-access.md` | `go test ./internal/cli/team -count=1` plus service API tests inside `services/platform-api` and `services/runtime-api` |
| Change event storage | `pkg/events`, `pkg/clickhouse`, `services/ingest`, `services/processor`, `services/analytics-api`, `services/mcp-gateway` | package tests plus touched service tests |
| Change docs site behavior | `docs/mkdocs.yml`, `docs/nginx.conf`, Markdown pages | MkDocs build or docs container build |

## Contributor checklist

Before opening a change, confirm:

- The code follows the closest existing package pattern.
- Public behavior is reflected in `docs/`, `README.md`, or `AGENTS.md` when relevant.
- CRD changes update both Go types and generated YAML.
- CLI help changes update golden snapshots intentionally.
- Narrow tests pass for touched packages.
- Full `go test ./... -count=1 -race` is run before merge when the change touches shared behavior.
