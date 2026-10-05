# MCP Runtime

<p align="center">
  <img src="website/static/brand/mcp-runtime-banner.png" alt="MCP Runtime: deploy, govern, and broker MCP servers using a Kubernetes-native control plane" />
</p>

[![Unit + Integration Tests](https://img.shields.io/github/check-runs/mcp-runtime/mcp-runtime/main?nameFilter=Unit%20%2B%20Integration%20Tests&label=Unit%20%2B%20Integration%20Tests)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/ci.yaml?query=branch%3Amain+job%3AUnit%20%2B%20Integration%20Tests)
[![Docs](https://img.shields.io/website?url=https%3A%2F%2Fdocs.mcpruntime.org%2F&label=Docs)](https://docs.mcpruntime.org/)
[![Website](https://img.shields.io/website?url=https%3A%2F%2Fmcpruntime.org%2F&label=Website)](https://mcpruntime.org/)
[![Platform](https://img.shields.io/website?url=https%3A%2F%2Fplatform.mcpruntime.org%2F&label=Platform)](https://platform.mcpruntime.org/)
[![QA E2E](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/ci.yaml?event=pull_request&label=QA%20E2E&job=QA%20E2E)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/ci.yaml?query=event%3Apull_request+job%3AQA%20E2E)
[![Staging E2E](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/staging-e2e.yaml?branch=main&label=Staging%20E2E)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/staging-e2e.yaml)
[![Gosec Scan](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/security-gosec.yaml?branch=main&label=Gosec%20Scan)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/security-gosec.yaml)
[![Gitleaks Scan](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/security-gitleaks.yaml?branch=main&label=Gitleaks%20Scan)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/security-gitleaks.yaml)
[![Trivy FS Scan](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/security-trivy.yaml?branch=main&label=Trivy%20FS%20Scan&job=Trivy%20FS%20Scan)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/security-trivy.yaml?query=branch%3Amain+job%3ATrivy%20FS%20Scan)
[![Trivy Image Scan](https://img.shields.io/github/actions/workflow/status/mcp-runtime/mcp-runtime/security-trivy.yaml?branch=main&label=Trivy%20Image%20Scan&job=Trivy%20operator%20Image)](https://github.com/mcp-runtime/mcp-runtime/actions/workflows/security-trivy.yaml?query=branch%3Amain+event%3Apush)
[![Coverage](https://codecov.io/gh/mcp-runtime/mcp-runtime/branch/main/graph/badge.svg)](https://codecov.io/gh/mcp-runtime/mcp-runtime/branch/main)

MCP Runtime is a Kubernetes control plane for [Model Context Protocol](https://modelcontextprotocol.io/) servers. It deploys MCP servers into your cluster, enforces per-tool access policy on every call, and records each decision for audit.

The workflow has three steps:

1. **Describe your server** in `.mcp/servers.yaml`: each tool's name, the trust level it requires, and its side effect. `mcp-runtime server init` creates the file, and `--from-server` fills in tools from a running instance. Then `server build`, `server push`, and `server deploy` publish it.
2. **Grant access.** An access grant (`mcp-runtime access grant init`) lets a person, agent, or team call specific tools on that server, up to a maximum trust level and only with the side effects you allow.
3. **Open a session.** An agent session (`mcp-runtime access session init`) time-boxes that access for one agent, and you can revoke it at any time.

On every call, a gateway in front of the server checks the caller's identity, session, trust level, and the tool's side effect before the call reaches your code, and records the decision for audit. Under the hood these are the `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession` resources, and the operator creates the Deployment, Service, Ingress, and policy for you.

A public preview runs at [platform.mcpruntime.org](https://platform.mcpruntime.org/). The same stack installs into your own cluster with `mcp-runtime setup`.

- [Website](https://mcpruntime.org/) · [Docs](https://docs.mcpruntime.org/) ([`docs/`](docs/)) · [API reference](https://docs.mcpruntime.org/api-reference) · [Articles](https://articles.mcpruntime.org/)
- Running or evaluating an internal MCP platform? Open a [GitHub issue](https://github.com/mcp-runtime/mcp-runtime/issues) with your use case, cluster shape, or integration feedback.

> [!CAUTION]
> MCP Runtime is alpha software. APIs, commands, and behavior are still evolving. Use the docs, CRDs, and `api/v1alpha1` types as the source of truth before production use.

## Features

- `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession` are namespaced CRDs, so servers, access, and sessions are visible and reviewable with `kubectl`.
- The `mcp-gateway` sidecar applies deny-by-default tool rules, trust ceilings, side-effect limits, session expiry, and revocation on every `tools/call`.
- Every allow and deny decision is recorded with the identity, tool, reason, and policy version, and is queryable through the Sentinel API and dashboards.
- `adapter proxy` gives IDEs, agent frameworks, and scripts a local Streamable HTTP endpoint that adds a session-bound client certificate with automatic refresh and forwards OAuth when the target requires it.
- Team namespaces, RBAC, and `teamID` subject matching let several teams publish and govern servers on one cluster, with private, org-wide, or public catalogs.
- Setup, registry and image-pull wiring, ingress, rollout readiness, `cluster doctor`, `cluster diagnostics`, and status commands are included.
- Documented install paths cover Kind, k3s, self-managed clusters, and managed Kubernetes with external registries.

## What ships

- `mcp-runtime` CLI for `auth`, `bootstrap`, `setup`, `status`, `registry`, `server`, `catalog`, `cluster`, `access`, `team`, and `ops`
- `mcp-runtime adapter proxy` for governed Streamable HTTP agent integrations.
  It enrolls a session-bound client certificate with
  `--server <name> --agent <id>` once an enabled grant exists; `--auto-refresh`
  renews the certificate (see [Agent Adapter](docs/connect-clients.md))
- Platform UI for authenticated MCP catalog browsing, platform state, and web operations
- `MCPServer`, `MCPAccessGrant`, and `MCPAgentSession` CRDs
- Kubernetes operator for `Deployment`, `Service`, `Ingress`, and policy materialization
- Internal or provisioned registry workflows
- Optional gateway enforcement for identity, tool policy, trust, and audit emission
- Bundled Sentinel stack for ingest, processing, API, UI, and observability.
  Control-plane services run in `mcp-platform`. The event pipeline and
  telemetry stack run in `mcp-observability`. Promtail runs in
  `mcp-log-collector`. MCP servers stay in `mcp-servers`, `mcp-servers-org`,
  `mcp-servers-public`, or `mcp-team-{slug}`. See [Namespaces](docs/namespaces.md).

## Requirements

Using the hosted platform requires the CLI; release installation needs `curl`
or `wget` on macOS/Linux, or PowerShell on Windows.

For self-hosting and source/contributor workflows, host tools include:

- Go `1.26+` and Make for source builds and contributor workflows (release CLI installs do not need them)
- Docker or a Docker-compatible client, with the daemon running
- `kubectl` on `PATH`, configured for the target cluster
- `curl`, `jq`, and `python3` for documented dev and traffic-generation flows
- `kind` for local Kind-based clusters

Cluster prerequisites:

- A running Kubernetes cluster: kind, k3s, minikube, Docker Desktop Kubernetes, EKS, GKE, AKS, or equivalent
- Working DNS, default storage class, ingress, and load-balancing path for your distribution
- See [`docs/deployment-targets.md`](docs/deployment-targets.md) to choose the install shape, then [`docs/cluster-readiness.md`](docs/cluster-readiness.md) before running production-like installs

`mcp-runtime setup` installs the platform stack, including Sentinel services such as ClickHouse and Kafka. You do not install those separately for the default flow.

## Quick start

Install the CLI on macOS or Linux (the installer detects your OS and CPU):

```sh
curl -fsSL https://raw.githubusercontent.com/mcp-runtime/mcp-runtime/main/install.sh | sh
export PATH="$HOME/.local/bin:$PATH"
mcp-runtime --version
```

For Windows, installation options, and your first hosted deployment, follow the
[Quickstart](docs/hosted-quickstart.md). No Kubernetes cluster is needed to use the
hosted platform.

To install the platform on your own cluster, follow
[Getting Started](docs/self-hosting.md). Choose a
[deployment target](docs/deployment-targets.md) and check
[cluster readiness](docs/cluster-readiness.md) before setup.

To build from source or run a disposable local Kind cluster, start with the
[Contributor Guide](docs/contributor/README.md).

## Common commands

```bash
./bin/mcp-runtime bootstrap              # preflight cluster prerequisites
./bin/mcp-runtime setup                  # install platform stack
./bin/mcp-runtime status                 # show platform health
./bin/mcp-runtime auth login --api-url <platform-url>   # save platform credentials
./bin/mcp-runtime team create acme --name "Acme Corp"   # create a team namespace (admin)
./bin/mcp-runtime registry status        # inspect registry
./bin/mcp-runtime server status          # inspect MCP servers
./bin/mcp-runtime catalog tools          # search tools across visible servers
./bin/mcp-runtime access grant list      # inspect access grants
./bin/mcp-runtime adapter proxy --server <name> --agent <id> --auto-refresh   # connect an MCP client
./bin/mcp-runtime ops status        # inspect platform stack
```

## Comparison

Features and deployment models change; check each project's current documentation before choosing.

### MCP directories and catalogs

The [Official MCP Registry](https://registry.modelcontextprotocol.io/),
[Glama](https://glama.ai/mcp), [Smithery](https://smithery.ai/),
[Docker MCP Catalog](https://hub.docker.com/mcp),
[PulseMCP](https://www.pulsemcp.com/), [mcp.so](https://mcp.so/), and
client-specific catalogs help people find and install public MCP servers.
MCP Runtime runs MCP servers inside your own cluster and governs calls to them.
The two are complementary: a server you find in a directory can be deployed and
governed with MCP Runtime.

| | Directories and catalogs | MCP Runtime |
|---|---|---|
| Purpose | Find and install public servers | Host, deploy, govern, and audit your own servers |
| Data | Discovery metadata, popularity, install snippets | `MCPServer` resources, grants, sessions, policy decisions, audit events |
| Where it runs | Third-party hosted service or client feature | Your Kubernetes cluster |
| Request path | Ends once the client is configured | Every tool call passes through the gateway |

### MCP gateways and platforms

Several projects run MCP servers on Kubernetes or define Kubernetes APIs for MCP
traffic. MCP Runtime's focus is expressing server workloads **and** access
policy (trust ceilings, allowed side effects, per-tool rules, user consent,
expiry, revocation) as validated Kubernetes resources that the operator
reconciles and the gateway enforces.

| Project | Focus | Compared with MCP Runtime |
|---|---|---|
| [Archestra](https://github.com/archestra-ai/archestra) | MCP platform with Kubernetes server orchestration, gateway, registry, and agent/chat features | Overlaps on Kubernetes-hosted servers; broader agent platform. MCP Runtime centers access on grant and session resources. |
| [Obot](https://github.com/obot-platform/obot) | MCP hosting, registry, gateway, and an organization-facing AI experience | Also hosts MCP servers. MCP Runtime keeps workload and access policy as Kubernetes state. |
| [Microsoft MCP Gateway](https://github.com/microsoft/mcp-gateway) | Kubernetes-oriented MCP gateway and management APIs, adapter/tool lifecycle, identity integrations | Overlaps on Kubernetes. MCP Runtime models deployment, grants, and consented sessions as resources. |
| [Agent Router](https://github.com/theagentrouter/agent-router) | Kubernetes Gateway API routing for MCP and AI traffic | Overlaps on Kubernetes APIs, focused on routing. MCP Runtime also manages server workloads and session consent. |
| [agentgateway](https://github.com/agentgateway/agentgateway) | High-performance data-plane gateway for MCP, agents, and AI traffic | Focused on routing and policy in the data plane. MCP Runtime owns workload lifecycle and access state. |
| [IBM ContextForge](https://github.com/IBM/mcp-context-forge) | Federation and gateway for MCP, A2A, REST, and gRPC | Broader protocol federation. MCP Runtime focuses on Kubernetes workload and access governance. |
| [MCPJungle](https://github.com/mcpjungle/MCPJungle) | Self-hosted team gateway, unified endpoint, discovery, tool grouping | Centers aggregation. MCP Runtime ties access decisions to reconciled cluster resources. |
| [Unla](https://github.com/AmoyLab/Unla) | MCP/API gateway with API-to-MCP conversion | Focused on API conversion. MCP Runtime focuses on deployed workloads and governed sessions. |
| [OpenZiti MCP Gateway](https://github.com/openziti/mcp-gateway) | Zero-trust networking and remote access for MCP tools | Focused on networking. MCP Runtime governs workloads and agent access inside Kubernetes. |
| [Docker MCP Gateway](https://github.com/docker/mcp-gateway) | Local Docker-based MCP server lifecycle, catalog, and configuration | Focused on local developer workflow. MCP Runtime targets platform-managed Kubernetes. |
| [LiteLLM](https://github.com/BerriAI/litellm) | Model gateway with MCP access, provider routing, keys, and spend controls | Focused on model routing and spend. MCP Runtime focuses on MCP server lifecycle and consented access. |
| [Kong](https://github.com/Kong/kong) | General API gateway with MCP proxy and AI gateway features | General-purpose gateway. MCP Runtime is an MCP-specific control plane for workloads, grants, and sessions. |
| [Portkey](https://github.com/Portkey-AI/gateway) | AI gateway and managed MCP gateway | Focused on AI traffic and hosted gateways. MCP Runtime is self-managed and reconciled from Kubernetes resources. |
| [Composio](https://github.com/ComposioHQ/composio) | SaaS integrations, toolkits, and per-user OAuth for agents | Focused on integration breadth. MCP Runtime focuses on operating MCP workloads and access policy. |
| [Preloop](https://github.com/preloop/preloop) | Agent control plane with MCP firewall, model gateway, approvals, and budgets | Focused on model controls and approvals. MCP Runtime expresses grants and sessions as Kubernetes state. |

### When to choose MCP Runtime

Choose MCP Runtime when you want one system to deploy MCP servers into your
cluster and enforce who may call which tool, with what trust and consent.

Another project may fit better if your main need is broad SaaS integrations,
model routing and spend controls, API-to-MCP conversion, remote zero-trust
networking, or a full agent/chat product.

## Development checks

```bash
gofmt -s -l .
go build -o bin/mcp-runtime ./cmd/mcp-runtime
go test ./... -count=1 -race
go vet ./...
```

For targeted tests, e2e setup, and debugging runbooks, use [`AGENTS.md`](AGENTS.md) and the docs site.

## Agent tool configuration

The repo keeps Claude-specific local configuration in [`.claude/`](.claude/README.md). Its `skills` entry is expected to be a symlink to `../.codex/skills`, so Claude Desktop and the Codex CLI discover the same repository skills during local development.

## License

Apache License 2.0. See [LICENSE](LICENSE).
