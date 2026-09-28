# CLI Internals

The `internal/cli` tree implements the command behavior behind the
`mcp-runtime` binary. The top-level Cobra command folders live under
`internal/cli/root` and `internal/cli/<command>`, while shared CLI-only kernel
code lives in `internal/cli/core`. All layers are intentionally internal so the
CLI can evolve without becoming a public Go API.

`go doc` is still useful for exported constructors and manager types:

```bash
go doc -all ./internal/cli/core
```

Most command behavior is unexported and should be understood through this page,
tests, and the command help snapshots.

## Design Principles

- Keep command wiring separate from side effects where practical.
- Prefer runner interfaces for `kubectl`, `docker`, and process execution so
  behavior can be tested without a live cluster.
- Return structured errors from shared helpers and print concise user-facing
  messages at command boundaries.
- Keep command help accurate and update golden snapshots for intentional output
  changes.
- Treat Kubernetes manifests and CRD types as source of truth; CLI structs should
  not drift from `api/v1alpha1`.

## Common Infrastructure

| File group | Responsibility |
|---|---|
| `core/constants.go` | namespace, deployment, service, and resource names shared by commands |
| `core/errors.go` | sentinel error values and wrapping helpers |
| `core/exec.go`, `core/kubectl_runner.go` | external command execution and test seams |
| `core/runtime.go` | composition root for shared CLI dependencies (`Config`, logger, kubectl, executor, printer) |
| `core/printer.go` | terminal output formatting |
| `kubeerr/` | shared kubectl error-detail extraction and cluster setup hints |
| `core/config.go` | environment/config defaults for registry, ingress, and setup |
| `kube/` | manifest apply, namespace, and kubectl-oriented helpers shared by command paths |
| `platformapi/` | Sentinel platform API client for auth-backed access and runtime reads |
| `platformapi/baseurl.go` | platform API base URL normalization used by auth and platform API clients |
| `platformstatus/` | shared workload catalog, readiness rows, and quiet kubectl status probes for `status` and `sentinel status` |
| `certmanager/` | cert-manager, private CA, and ACME helpers shared by setup and `cluster cert` |
| `cluster/ingress.go` | ingress configuration option structs shared by setup and cluster managers |
| `registry/` | registry manager, registry deployment, registry push, and platform registry defaults |
| `registry/config/` | provisioned external registry config file loading and precedence |
| `registry/ref/` | shared image reference parsing used by setup image publishing and registry push |
| `registry/resolve/` | registry URL and image tag resolution shared by setup, server build, and registry push |

When adding a helper, put it near the command that owns it unless two or more
commands genuinely share it.

## Setup

Setup is split across `internal/cli/setup/`:

- `setup.go`: Cobra command and flag wiring.
- `platform/platform.go`: setup orchestration, image publishing, manifest application,
  verification, and deployment diagnostics.
- `platform/flow.go`: setup flow validation and user-facing warnings.
- `platform/steps.go`: step-level helpers used by setup orchestration.
- `plan/`: planning and dependency injection seams used by tests.
- `assetpath/`: repo-root and asset path resolution used by setup builds
  and manifest rendering.
- `ingressmanifest/`: platform UI ingress manifest rendering.

`setup --test-mode` relaxes production guardrails but still builds and pushes
the operator, gateway proxy, and Sentinel images with `latest` tags. Pull hosts
must still be reachable and trusted by node container runtimes. On k3s with the
bundled HTTP registry, that means a `registries.yaml` mirror for the exact
registry host/port used in pod image refs.

Important setup contracts:

- CRDs and namespaces are applied before runtime components.
- Ingress is installed or reused before registry routes are needed.
- Registry info is resolved before runtime images are named.
- Internal registry pushes validate platform credentials, then use an
  in-cluster helper when direct host pushes are not appropriate.
- Operator setup prepares admission webhook TLS, enables webhook serving on the
  manager deployment, and applies the generated webhook service/configuration
  with the matching CA bundle.
- Sentinel rollouts use `MCP_DEPLOYMENT_TIMEOUT`.
- Setup verification should fail with diagnostic context instead of reporting
  success after partial deployment.

Setup command wiring lives in `internal/cli/setup/`; the platform install flow
lives in `internal/cli/setup/platform/` with tests beside the workflow files,
including `helpers_test.go`, `plan_flow_test.go`, `steps_test.go`,
`config_plan_test.go`, and `tls_flags_test.go`.

## Cluster and Doctor

`cluster.go` provides cluster initialization, status, ingress configuration, and
provider-oriented provisioning helpers. `bootstrap.go` performs preflight checks
and has the only automated apply path for k3s CoreDNS/local-path prerequisites.

`internal/cli/cluster/doctor/` owns post-install diagnostics. It checks CRDs,
workloads, registry reachability, image pull failures, ingress, and platform
components. Registry protocol mismatch detection must inspect regular
containers and init containers, and it must surface failed pod inspections
instead of returning a false pass.

Tests: `cluster_test.go`, `doctor/doctor_impl_test.go`, and bootstrap-related
tests.

## Registry

`internal/cli/registry/` owns registry Cobra wiring, status, info,
provisioning, login, deployment, direct pushes, and in-cluster helper pushes.
Shared image reference parsing lives in
`internal/cli/registry/ref/`, registry URL/tag resolution lives in
`internal/cli/registry/resolve/`, and provisioned registry config lives in
`internal/cli/registry/config/` so setup and server build can reuse those rules
without depending on registry command internals.

Registry endpoint precedence is intentionally shared with setup and metadata:

- explicit CLI flags
- environment variables/config
- platform-derived registry host
- bundled registry service discovery

The in-cluster push path uses a temporary helper workload and should clean up
after itself even on failure. When editing this path, verify both success and
diagnostic failure output.

Tests live with the registry package, plus setup tests for runtime image
publishing.

## Server and Build

`server.go` implements CRUD-style operations for `MCPServer` resources and
status/log inspection. `build.go` supports metadata-driven image builds for the
`.mcp` workflow. Server-specific input validation lives with the server manager
in `internal/cli/server/validation.go`.

Keep these flows distinct:

- `server init` scaffolds `.mcp/servers.yaml` with `gateway.enabled: true`,
  policy, and session intent; it leaves gateway wiring and header defaults to
  the metadata loader and CRD defaulting.
- `server apply --use-kube` applies a manifest through kubectl (admin only).
- `server build image` builds and updates metadata but does not deploy.
- `server generate` renders manifests from metadata for review/GitOps.
- `server deploy --metadata-dir .mcp` deploys metadata-backed servers through the platform API.
- `server push` publishes images after platform credential validation.

Tests: `server_test.go`, `server_config_test.go`, and `build_test.go`.

Pipeline changes usually require checking:

- metadata schema and defaulting
- generated YAML shape
- docs for `.mcp` authoring
- examples under `examples/`

Tests: `server`/`metadata` package tests and golden CLI help snapshots.

## Access

`internal/cli/access/` provides commands for grants and sessions:

- `access grant init|list|get|apply|delete|enable|disable`
- `access session init|list|get|apply|delete|revoke|unrevoke`
- `access explain` evaluates a hypothetical request against live or local rendered policy

`init` scaffolds reviewable YAML; `apply` writes through the platform API by
default. Add `--use-kube` only for admin/operator direct Kubernetes flows
that bypass platform auth.

The implementation patches `spec.disabled` for grants and `spec.revoked` for
sessions. Input validation should prevent invalid names/namespaces before they
reach `kubectl`; that validation lives in `internal/cli/access/validation.go`.

Tests: `access/manager_test.go` and `access/validation_test.go`.

## Adapter

`internal/cli/adapter/` provides `adapter enroll` and `adapter proxy`. The CLI
layer resolves certificate and transport flags and platform-issued sessions
from `POST /api/v1/runtime/adapter/sessions`, then delegates HTTP proxy behavior
to `internal/agentadapter`.

Keep the boundary clear: command parsing, platform-session bootstrap, and
auto-refresh scheduling live in `internal/cli/adapter`; request forwarding,
governance-header stripping, and TLS transport live in `internal/agentadapter`.

Tests: `adapter/adapter_test.go`, `adapter/mtls_test.go`, and
`internal/agentadapter` tests.

## Team

`internal/cli/team/` owns platform team commands. `team list`, `team create`,
and `team user` call the platform API through `internal/cli/platformapi`.
`team init` is deprecated and rejects at runtime; use `team create`.

Team behavior spans CLI and API code: platform-backed team creation and
membership routes live in `services/runtime-api/internal/runtimeapi`, durable identity
state lives in `services/platform-api/internal/platformstore`, and user-facing guidance
lives in `docs/multi-team.md`.

Tests: `team/manager_test.go`; for platform team API changes, run focused tests
inside `services/platform-api` and `services/runtime-api`.

## Auth, Sentinel, and Platform API

`internal/cli/auth/` handles platform API login, logout, and credential profiles.
`internal/cli/sentinel/` and `internal/cli/platformapi/` provide CLI access to
the split Sentinel APIs and platform API URL normalization. These commands should stay
aligned with split API service routes (`/api/v1/*`) and the public docs.

Tests: `sentinel/*_test.go`, `auth/*_test.go`, and `platformapi/*_test.go`.

## Status

`internal/cli/status/` prints high-level platform health by querying Kubernetes.
It uses the shared `internal/cli/platformstatus/` workload catalog so top-level
status and `sentinel status` do not drift. Shared kubectl diagnostics live in
`internal/cli/kubeerr/`. Status should be quick, readable, and conservative.
Deeper diagnosis belongs in `cluster doctor`.

Tests: `status/*_test.go` and shared printer helpers in `status_test.go`.

## Adding a Command

1. Add or update the thin Cobra routing package under `internal/cli/<command>`.
2. Put command behavior in a focused manager/service file in that command
   package unless the behavior is genuinely shared.
3. Register the top-level command from `internal/cli/root/commands.go`.
4. Add tests with mocked runners or fake dependencies.
5. Build the CLI and inspect `--help`.
6. Update golden snapshots if help/output changes intentionally.
7. Update user docs when behavior is user-facing.

Run:

```bash
go test ./internal/cli/... ./cmd/mcp-runtime ./test/golden/... -count=1
```
