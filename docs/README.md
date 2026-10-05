# MCP Runtime

MCP Runtime is an open-source Kubernetes control plane for MCP servers. It
deploys servers into your cluster and can route tool calls through a gateway
sidecar for policy checks and audit. Use allow-list policy to enforce grants,
sessions, and per-tool rules; observe mode records decisions without enforcing
them.

<div class="docs-home">
<p class="docs-brand-banner"><img src="assets/brand/mcp-runtime-banner.png" alt="MCP Runtime: deploy, govern, and broker MCP servers using a Kubernetes-native control plane" /></p>
<section class="docs-hero">
  <div class="docs-hero-copy">
  <p class="docs-eyebrow">Kubernetes control plane for MCP servers</p>

  <p class="docs-lead">Build and push an MCP server image, deploy it as an <code>MCPServer</code> resource, and control which agents may call which tools with grants and sessions.</p>

  <div class="docs-actions">
    <a class="docs-button docs-button-primary" href="hosted-quickstart/">Try in 10 min</a>
    <a class="docs-button" href="core-concepts/">Concepts</a>
    <a class="docs-button" href="architecture/">Architecture</a>
    <a class="docs-button" href="self-hosting/">Self-host</a>
    <a class="docs-button" href="api-reference/">API reference</a>
    <a class="docs-button" href="https://docs.pkg.mcpruntime.org/github.com/mcp-runtime/mcp-runtime">Go packages</a>
  </div>
  </div>
</section>
</div>

## Which setup should I use?

| Your goal | Start with | Have ready | Success check |
|---|---|---|---|
| Try an existing platform | [Hosted quickstart](hosted-quickstart.md) | Platform account and team; Git, Go, and Docker for the example | Login succeeds and your deployed server becomes ready |
| Run MCP Runtime on your own cluster | [Self-hosting](self-hosting.md) | Prepared Kubernetes cluster, Docker, and kubectl | Setup passes its smoke gate and authenticated platform access works |
| Adapt the project's deployment | [Reference deployment](reference-deployment.md) | A chosen Kubernetes distribution and deployment profile | Runtime and identity-provider endpoints work, with separate backups |
| Develop or test changes locally | [Local Kind and test mode](contributor/local-kind.md) | Source checkout and contributor prerequisites | Local setup and cluster health checks pass |
| Learn grants and client identity | [Guided tutorials](learn/README.md) | Running platform; adapter-certificate identity for successful governed calls | An allowed tool call succeeds and a denied call is rejected |

### Where to go next

After deployment, [connect a client](connect-clients.md). To control who can
call tools, read [identity and authorization](identity-and-authorization.md)
and work through [multi-team access](learn/03-multi-team-access.md). For routine
administration, use [runtime operations](runtime-operations.md).

For the complete hosting lifecycle, start with the
[Self-Host and Operate overview](hosting-overview.md). This section uses our
public platform as the reference deployment and connects infrastructure
choices, provisioning, installation, identity, backups, and daily operations.

A ready server confirms deployment; a successful governed tool call also needs
a matching grant, valid session, and verified client identity. The
[hosted quickstart](hosted-quickstart.md) explains the public instance's identity
limitation. If a call fails, start with the matching error in
[troubleshooting](troubleshooting.md), then check the
[CLI reference](cli-reference.md) for the command's flags.

## Start here

1. [Hosted Quickstart](hosted-quickstart.md): install the CLI and try an existing platform.
2. [Self-host MCP Runtime](self-hosting.md): prepare a cluster and install the platform.
3. [Guided tutorials](learn/README.md): learn concepts, deploy a server, and set up teams.

OAuth setup and identity-provider configuration: [MCP authorization](mcp-oauth.md).

## How it works

- An `MCPServer` resource describes a server: image, port, route, and the tools it
  exposes with their required trust and side effect. The operator turns it into a
  Deployment, Service, Ingress, and a policy ConfigMap.
- An `MCPAccessGrant` says which agent or team may call which tools, up to what
  trust level. An `MCPAgentSession` carries the trust a person consented to, an
  expiry, and a revoke switch.
- With allow-list policy enabled, the `mcp-gateway` sidecar checks each
  `tools/call` against the grant, any required session, and the tool metadata
  before forwarding it. It emits an audit event with the decision. Observe mode
  forwards calls without enforcing policy.

See [Concepts](core-concepts.md) for details.

## Deploy an MCP server

```bash
mcp-runtime auth login --api-url https://platform.mcpruntime.org
mcp-runtime server init my-server --from-server http://localhost:8088
mcp-runtime server build image my-server --tag v1
# ^ prints the exact image ref, e.g. registry.mcpruntime.org/myteam/my-server:v1
mcp-runtime server push --image registry.mcpruntime.org/myteam/my-server:v1 --scope tenant
mcp-runtime server deploy my-server --scope tenant --metadata-dir .mcp
```

Run these commands from your server’s source directory, with a Dockerfile
and Docker running. Use the exact image reference printed by the build command.
The CLI generates the Kubernetes resources. To connect a client, run the
[adapter](connect-clients.md) and point Claude Desktop, Cursor, or any MCP client
at it. The adapter presents a session-bound client certificate the gateway
uses for grant and session checks (and forwards OAuth when the target enables it).

## Who is this for?

| You are | MCP Runtime gives you |
|---|---|
| **Platform engineer** | Operator, registry, and ingress wiring generated from one resource |
| **Security team** | Per-tool audit trail, trust levels, session revocation, deny rules, compliance evidence |
| **Team lead** | Isolated namespace per team, grants scoped to teams, cross-team access without sharing credentials |
| **Developer** | Use the hosted platform to deploy with the CLI and connect through the adapter, without managing Kubernetes |

## Why I built this

I started building MCP servers at work in 2024 and went on to build many of them. Implementing the MCP specification led me to think beyond individual servers: how could teams host MCP servers internally, and how could they authorize access to them? That led me to build an internal platform and an authorization server, drawing on what I learned at work. Now I am bringing those lessons together in MCP Runtime, a platform for hosting, governing, and connecting MCP servers. Here I am building it in the open.

I have been reading the MCP SEPs for gateway and identity management patterns. There are active proposals for exactly these problems. The gateway policy enforcement is a work in progress, and I am following the spec and iterating. MCP still has a long way to go here, and so do I.

## What MCP Runtime installs

`mcp-runtime setup` installs the CRDs, the namespaces in
[Namespaces](namespaces.md), an operator, registry integration, ingress
wiring, and the platform services. Those services include the gateway request
path, grant/session policy materialization, analytics ingest and processing,
dashboard and API services, and observability components.

## Comparison

For how MCP Runtime relates to MCP directories and to other MCP gateways, see
the [README](https://github.com/mcp-runtime/mcp-runtime#comparison).

## Governance, audit, and compliance

With allow-list policy enabled, the gateway evaluates `MCPAccessGrant` and any
required `MCPAgentSession` before tool calls reach a server. Checks include
tool-level allow/deny rules, side-effect allowances, trust requirements,
consented trust, expiry, and revocation. Observe mode records decisions but
does not enforce these checks.

Each decision can emit audit and analytics events with the server, namespace,
team ID, human ID, agent ID, session ID, tool name, policy version, decision,
reason, and trust and side-effect context. Use these records to review access,
investigate denied calls, and prepare compliance evidence.

## Before setup

You need a running Kubernetes cluster and a workstation with the CLI
prerequisites installed. `mcp-runtime setup` applies the runtime manifests,
installs the operator and platform services, and wires ingress and registry
resources for your environment.

For provider-specific prerequisites such as container runtime registry trust,
DNS, ingress, TLS, and Kubernetes distribution configuration, start with
[Deployment Targets](deployment-targets.md) to choose the right install shape,
then [Cluster readiness](cluster-readiness.md) for distribution-specific
preparation.

## Where to go next

<div class="docs-grid docs-grid-2">
<a class="docs-card" href="self-hosting/">
  <span class="docs-card-kicker">Start here</span>
  <strong>Get started</strong>
  <span>Install the CLI, prepare your cluster, configure the platform, and verify health.</span>
</a>

<a class="docs-card" href="architecture/">
  <span class="docs-card-kicker">Understand</span>
  <strong>Architecture</strong>
  <span>How the control plane, registry, operator, and platform services fit together.</span>
</a>
</div>

**Developer guide:** publish and govern MCP servers

<div class="docs-grid docs-grid-3">
<a class="docs-card" href="publish-mcp-server/">
  <span class="docs-card-kicker">Build</span>
  <strong>Publish an MCP server</strong>
  <span>Write metadata, build and push an image, deploy it, and verify what the platform creates.</span>
</a>

<a class="docs-card" href="agent-adapters/">
  <span class="docs-card-kicker">Connect</span>
  <strong>Agent adapters</strong>
  <span>A Streamable HTTP adapter that presents session-bound certificate identity and forwards OAuth when the target enables it.</span>
</a>

<a class="docs-card" href="multi-team/">
  <span class="docs-card-kicker">Govern</span>
  <strong>Multi-team isolation</strong>
  <span>Namespace-per-team isolation, RBAC, and cross-team server access.</span>
</a>
</div>

**Operator guide:** deploy and operate the platform

<div class="docs-grid docs-grid-3">
<a class="docs-card" href="deployment-targets/">
  <span class="docs-card-kicker">Plan</span>
  <strong>Deployment targets</strong>
  <span>Choose the right install shape for k3s, EKS, GKE, AKS, and other distributions.</span>
</a>

<a class="docs-card" href="runtime/">
  <span class="docs-card-kicker">Operate</span>
  <strong>Runtime</strong>
  <span>CRDs, reconciliation outputs, image resolution, ingress wiring, and rollout flow.</span>
</a>

<a class="docs-card" href="namespaces/">
  <span class="docs-card-kicker">Operate</span>
  <strong>Namespaces</strong>
  <span>Where the operator, control plane, telemetry, and MCP servers run.</span>
</a>

<a class="docs-card" href="platform-services/">
  <span class="docs-card-kicker">Observe</span>
  <strong>Platform services</strong>
  <span>Gateway policy evaluation, analytics, audit events, and observability services.</span>
</a>
</div>

**Reference**

<div class="docs-grid docs-grid-2">
<a class="docs-card" href="cli/">
  <span class="docs-card-kicker">CLI</span>
  <strong>Command reference</strong>
  <span>Every command with flags, examples, and a full end-to-end walkthrough.</span>
</a>

<a class="docs-card" href="api-reference/">
  <span class="docs-card-kicker">API</span>
  <strong>API and CRDs</strong>
  <span>MCPServer, MCPAccessGrant, MCPAgentSession fields and HTTP endpoints.</span>
</a>
</div>

## Project status

MCP Runtime is **alpha**. The architecture is stable enough to evaluate as governed MCP infrastructure, but API and UX details are still evolving. Treat the `v1alpha1` types as the source of truth. A security audit is planned but has not been completed, so do not use this in production without your own review.

## Community

- [GitHub Issues](https://github.com/mcp-runtime/mcp-runtime/issues): bug reports and feature requests
- [GitHub Discussions](https://github.com/mcp-runtime/mcp-runtime/discussions): questions, ideas, and general discussion
- [Releases](https://github.com/mcp-runtime/mcp-runtime/releases): changelog and binary downloads
