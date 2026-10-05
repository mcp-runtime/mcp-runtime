# Architecture

MCP Runtime has four planes: control, runtime, data and policy, and
observability. The linked guides at the end cover each part in detail.

## Planes

```mermaid
flowchart TB
    subgraph clients [Clients]
        CLI[mcp-runtime CLI]
        Browser[Platform dashboard in browser]
        Adapter[Agent adapter]
        MCP[MCP client]
    end
    subgraph platform [mcp-platform]
        UI[UI and session BFF]
        PlatAPI[platform-api]
        RunAPI[runtime-api]
        DB[(Postgres identity and agent directory)]
    end
    subgraph operatorns [mcp-runtime]
        Op[MCPServer operator]
    end
    subgraph observability [mcp-observability]
        AnaAPI[analytics-api]
        Ingest[ingest]
        Kafka[(Kafka)]
        Processor[processor]
        CH[(ClickHouse)]
    end
    subgraph servers [MCP server namespace]
        Grant[MCPAccessGrant]
        Session[MCPAgentSession]
        Server[MCPServer]
        Policy[Policy ConfigMap]
        GW[mcp-gateway sidecar]
        Srv[MCP application]
    end
    Ing[Traefik ingress]
    K8s[Kubernetes API]
    Reg[Container registry]
    CLI --> PlatAPI
    CLI --> RunAPI
    CLI -. setup or explicit admin Kubernetes mode .-> K8s
    Browser --> Ing
    Ing --> UI
    UI --> PlatAPI
    UI --> RunAPI
    UI --> AnaAPI
    PlatAPI --> DB
    RunAPI -->|resolve principal and directory| PlatAPI
    RunAPI --> K8s
    K8s -->|watch events| Op
    Server --> Op
    Grant --> Op
    Session --> Op
    Op -->|render| Policy
    Op -->|reconcile workload| Srv
    Policy -->|mounted snapshot| GW
    MCP --> Adapter
    Adapter -->|session and certificate enrollment| RunAPI
    Adapter -->|MCP over HTTPS| Ing
    Ing --> GW --> Srv
    RunAPI -->|in-cluster image push| Reg
    Reg -. pull image .-> Srv
    GW --> Ingest --> Kafka --> Processor --> CH
    AnaAPI -->|query| CH
```

Service placement is in [Namespaces](namespaces.md). The `/api/v1` surface is served by three services behind Traefik path routing:
**platform-api** (identity, auth, registry authorization, admin), **runtime-api**
(servers, grants, sessions, deployments, adapter sessions), and **analytics-api**
(events, stats, usage). See [Platform services](platform-services.md) for the per-service route and
RBAC split.

## What each layer owns

| Layer | Owns | Read next |
|-------|------|-----------|
| **Runtime** | Bootstrap, setup, registry workflow, `MCPServer` reconciliation, grants/sessions, rollout | [Runtime](runtime-operations.md) |
| **Platform services** | Gateway sidecar policy enforcement, analytics ingest, dashboards | [Platform services](platform-services.md) |
| **Split APIs** | platform-api (teams, identity, registry authz), runtime-api (deploy/push, grants, adapter sessions), analytics-api (events, usage) | [API](api-reference.md), [Platform services](platform-services.md) |
| **Multi-team** | Namespace isolation, team RBAC, Traefik watch scope | [Multi-Team Isolation](teams-and-access.md) |

## Typical request path

1. An MCP client (or agent adapter) calls `https://mcp.<domain>/<server>/mcp`.
2. Ingress routes to the server pod; the **mcp-gateway** sidecar evaluates
   `MCPAccessGrant` + `MCPAgentSession` policy before forwarding to the app.
3. Allowed and denied tool calls can emit analytics events through ingest → Kafka → processor
   → ClickHouse; Grafana surfaces usage and traces.

Local scaffolding commands (`server init`, `access grant init`, `access session init`)
write manifests on the workstation. `server init --from-server` also calls
the supplied MCP endpoint to discover tools; these commands do not write to
the platform API or Kubernetes.

Control-plane changes (setup, `auth login`, `server deploy`, `access grant apply`,
and admin-only `access session apply`) flow through the CLI or the split APIs
into Kubernetes; the operator materializes Deployments, Services, Ingress, and
policy ConfigMaps. `auth login` authenticates against platform-api, while
server, grant, and session writes land on runtime-api. Sessions for agent
traffic are usually issued through
`POST /api/v1/runtime/adapter/sessions`; explicit session manifests are
admin-only. Admin-only `--use-kube` or `kubectl apply`
bypasses platform auth and requires operator RBAC.

See [Request Flows](internals/request-flows.md) for allow/deny sequence diagrams,
component-level paths, and E2E scenario mapping.

## Deployment shapes

| Shape | When to use | Guide |
|-------|-------------|-------|
| Kind + `--test-mode` | Local contributor development | [Contributor Local Kind](contributor/local-kind.md) |
| k3s lab (HTTP registry) | Single-node evaluation | [Deployment Targets - k3s lab](deployment-targets.md#k3s-lab-example) |
| k3s / on-prem + bundled HTTPS | Public domain with Let's Encrypt | [Deployment Targets](deployment-targets.md), [Public Reference Deployment](reference-deployment.md) |
| Managed Kubernetes + external registry | EKS, GKE, AKS | [Deployment Targets - Managed Kubernetes](deployment-targets.md#managed-kubernetes) |

## Related reading

- [Getting Started](self-hosting.md): install and first server
- [Publish an MCP Server](publish-mcp-server.md): metadata, build, push, deploy
- [Agent Adapters](connect-clients.md): HTTP proxy
- [CLI](cli-reference.md): command reference
- [Cluster Readiness](cluster-readiness.md): registry, DNS, TLS, node trust checks
