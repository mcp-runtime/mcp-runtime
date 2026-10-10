# Staging E2E on the disposable VM

Staging E2E exercises the full production install path using
`setup --strict-prod`, public TLS, the bundled HTTPS registry, the platform
API/UI, tenants, grants, adapters, and analytics on a dedicated **disposable** VM whose
hostnames live under `*.e2e.mcpruntime.org`. It is intentionally separate from
QA E2E, which uses a local Kind test-mode cluster. Staging was previously called
"Production E2E"; the name changed
because it never touches the production cluster.

## Runner

The `Staging E2E (Disposable VM)` workflow packages the checkout, copies it to
the disposable VM, and runs `test/e2e/staging-vm.sh` there over SSH, so the CLI
runs next to k3s. The assertions, the target guard, and the stage runner live
in `test/e2e/lib/staging.sh`. The workflow holds one concurrency group so only
one run drives the VM at a time.

Adapter fixtures rotate their own server namespace pull credential using
`registry enable-auth --pull-namespace` after the fixture deployments exist,
so the broker grants their exact repositories. They never reuse another
namespace's node credential; registry denial checks remain enabled.

The runner authenticates to GHCR with the workflow's short-lived
package-read token and reuses content-hash platform images when available.
The workflow passes that token over SSH stdin after the disposable-target
guard, and the runner removes its temporary Docker credentials during cleanup. Cache misses
build locally; Staging does not publish to GHCR. Pull-request QA E2E does not
use GHCR: it keeps one local `:latest` image per component on the VM and
compares the next checkout to that image's content-hash label. Set
`E2E_IMAGE_CACHE=0` for a Staging run that must rebuild every platform image.

## Safety: the disposable-target guard

The suite installs and uninstalls k3s, prunes every Docker image, and wipes
kubelet/CNI state. It must never run against the live production install
(`platform.mcpruntime.org`, `registry.mcpruntime.org`, `mcp.mcpruntime.org`,
`auth.mcpruntime.org`). Before anything is copied to or run on the VM, the
workflow runs `test/e2e/staging-target.sh check`, and the runner repeats the
check before its first destructive action. The guard refuses unless:

1. every E2E hostname ends in `.e2e.mcpruntime.org`
   (`E2E_DISPOSABLE_DOMAIN_SUFFIX` overrides it for another disposable zone;
   a suffix equal to, or a parent of, the production domain is rejected);
2. no E2E hostname, the VM host, or (on the VM) any local address resolves to
   an address that a production hostname resolves to. Production addresses are
   derived by DNS at run time and never stored in the repository; if the
   production names cannot be resolved the guard fails closed;
3. the E2E hostnames resolve to the VM host being wiped; and
4. the VM carries `/var/lib/mcp-runtime-e2e-backup/DISPOSABLE`, whose first
   line is `mcp-runtime-disposable-e2e-vm` and which records the VM hostname
   and the disposable suffix. A marker for another hostname or suffix, or on a
   machine named like a production host (`devbox` by default), is rejected.

The marker is created only by an explicit, one-time bootstrap on a freshly
provisioned disposable VM, after the same DNS checks pass:

```bash
E2E_VM_HOST=<vm-address> E2E_CONFIRM_DISPOSABLE_VM=<vm-address> \
  bash test/e2e/staging-target.sh bootstrap
```

or, from CI, by dispatching the workflow once with
`bootstrap-disposable-marker=true`. Never bootstrap a machine that is not the
disposable E2E VM, and never set the guard escape hatches
(`E2E_GUARD_ALLOW_UNRESOLVED_PRODUCTION`, `E2E_GUARD_ALLOW_VM_DNS_MISMATCH`)
in CI.

## Configuration

The workflows require these GitHub Actions secrets:

| Secret | Meaning |
| --- | --- |
| `E2E_VM_HOST` | Dedicated disposable VM hostname or address |
| `E2E_VM_USER` | SSH user with permission to install k3s |
| `E2E_VM_PASSWORD` | SSH password for that VM |
| `E2E_VM_KNOWN_HOSTS` | Pinned `known_hosts` line(s) for the VM SSH host key |
| `E2E_ACME_EMAIL` | Email passed to the TLS/ACME setup on the disposable VM |

The VM needs the wildcard DNS record `*.e2e.mcpruntime.org` pointing at it;
the fresh-certificate stage also relies on the wildcard for its unique
`run-<id>.e2e.mcpruntime.org` host.

Populate `E2E_VM_KNOWN_HOSTS` from a host-key fingerprint verified against the
VM provider console, for example `ssh-keyscan -H <verified-vm-host>`. The
workflow rejects unknown or changed host keys and never uses host-key bypasses.

The workflow ensures `E2E_ACME_EMAIL` is present in the VM backup environment
file without overwriting the certificate or identity-provider backup. Keep
the remaining E2E-only values in `/var/lib/mcp-runtime-e2e-backup/e2e.env`,
outside the checkout and outside every cleanup path:

```bash
E2E_ACME_EMAIL=operations@example.com
E2E_PLATFORM_API_TOKEN=replace-with-an-e2e-only-admin-key
E2E_WITH_MCP_AUTH=1
E2E_MCP_AUTH_ISSUER_URL=https://auth.e2e.mcpruntime.org/realms/mcp
E2E_MCP_AUTH_CONNECTOR=keycloak
# Optional: exercise a Keycloak password grant for a test user.
E2E_OIDC_TEST_CLIENT_ID=...
E2E_OIDC_TEST_USERNAME=...
E2E_OIDC_TEST_PASSWORD=...
```

`E2E_PLATFORM_API_TOKEN` is optional. If it is absent or rejected, the runner
uses the first `ADMIN_API_KEYS` value generated by setup. The platform admin
password is generated on first run and persisted in `e2e.env`.

`E2E_MTLS_CLUSTER_ISSUER` defaults to `mcp-runtime-ca`, so setup runs with
`--mtls-cluster-issuer`. Adapter certificates are an opt-in platform feature
on gateway MCPServer routes (OAuth optional), so the runner also exports
`MCP_ADAPTER_CERTIFICATES=true`, `MCP_TRUST_DOMAIN` (default
`e2e.mcpruntime.org`; override with `E2E_ADAPTER_TRUST_DOMAIN`), and
`MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-servers` before setup. Path routes
share `mcp.e2e.mcpruntime.org`, so the operator keeps one Traefik `default`
TLSOption in that namespace that requests, but never requires, a client
certificate. The adapter-enrollment stage always proves cert-only HTTPS
access (omit `spec.auth`). Set `E2E_MCP_OAUTH_ACCESS_TOKEN` to also exercise
OAuth + certificate on a second pair of servers. Set
`E2E_ADAPTER_CERTIFICATES=0`, or `E2E_MTLS_CLUSTER_ISSUER` to an empty value,
to skip the adapter-enrollment stage.

The connector name is only an E2E configuration choice. MCP Runtime's auth
server is provider-agnostic; Keycloak gives the test an isolated OIDC issuer
and test user.

## Workflow inputs

| Input | Default | Effect |
| --- | --- | --- |
| `run-multitenancy` | `true` | Multi-team build/push/deploy, grants, adapter calls, governance deny paths, tenant analytics |
| `fresh-certificate` | `false` | Issue a brand-new staging certificate for `run-<id>.e2e.mcpruntime.org`, serve it through Traefik, and verify it; routine runs reuse the TLS snapshot |
| `bootstrap-disposable-marker` | `false` | One-time marker bootstrap (see above) |

The runner uses the Let's Encrypt **staging** CA by default (`E2E_ACME_STAGING=1`).
The production CA allows five certificates per exact set of identifiers per
week; staging exercises the identical ACME order, HTTP-01 challenge, and
cert-manager path with far higher limits. The runner installs the staging roots
into the VM trust store before k3s starts (containerd needs them to pull from
the registry), so every HTTPS check verifies the chain properly instead of
using `curl -k`.
Use `fresh-certificate` sparingly and `E2E_ACME_STAGING=0` only for an
occasional production-CA run.

To keep a disposable VM on publicly trusted certificates, set
`E2E_ACME_STAGING=0` and `E2E_ACME_EMAIL` in its preserved `e2e.env`.
Leave `fresh-certificate=false`: cleanup snapshots the registry and platform
certificate Secrets outside the wiped paths, and the next setup restores them
before cert-manager runs. Switching from the staging CA requires production
issuance once; subsequent runs reuse valid certificates. Backup reuse avoids
routine issuance, but renewal and a lost or incomplete snapshot can still
require new ACME orders and are subject to Let's Encrypt limits.

## Stages and evidence

Every run writes per-stage logs to `stages/NN-<stage>.log`, a `summary.json`
and `summary.md` with pass/fail/skip per stage (also appended to the GitHub
job summary), and always collects `diagnostics/` (nodes, pods, events,
describe, per-deployment logs, certificates/orders/challenges, ClusterIssuers,
ingress and Traefik CRs, Secret names only, and the served public
certificates). A failed stage prints the likely cause and the artifact to open
first; a failed critical stage skips the stages that depend on it.

| Stage | Asserts |
| --- | --- |
| `target-guard` | The disposable-target guard above |
| `prerequisites` / `dependencies`, `go-toolchain` | Local tools; the CLI builds |
| `staging-roots` | Let's Encrypt staging roots trusted on the VM |
| `k3s`, `traefik` | Node Ready, kubeconfig reachable, disk headroom, bundled Traefik exposed |
| `doctor-before` | Advisory pre-setup `cluster doctor` |
| `restore-snapshot` | TLS snapshot restored before setup so cert-manager reuses issued certificates |
| `setup` | `setup --strict-prod --with-tls --acme-staging --registry-mode bundled-https --mtls-cluster-issuer ...` |
| `diagnostics` | Post-setup `cluster diagnostics`, `cluster doctor`, `cluster status` |
| `rollouts` | Every Deployment/StatefulSet in `mcp-runtime`, `mcp-platform`, `mcp-observability`, `mcp-log-collector`, `registry`, `cert-manager`, Traefik rolled out; no pod stuck in image pull or crash loop |
| `cluster-issuer` | ACME ClusterIssuer Ready and pointed at the staging (or production) directory |
| `certificates` | `registry/registry-cert` and `mcp-platform/mcp-platform-tls` Ready, expected issuer, SANs cover the hosts, not expiring |
| `tls-endpoints` | `openssl s_client` chain verification, hostname match, and staging issuer for platform/registry/mcp (and auth) |
| `fresh-certificate` | Gated fresh issuance for a unique host, served and verified end to end |
| `platform-login` | Admin token, `auth login`, `auth status`, `status`, `server list`, `registry info`, command help surfaces |
| `platform-api` | Admin API calls; anonymous and bad-key requests denied; admin password login (password over stdin) and wrong-password denial |
| `registry-auth` | Anonymous `/v2/`, manifest HEAD, and push denied; authenticated manifest HEAD, blob + manifest push, and pull allowed |
| `image-pulls` | Platform workloads pull from the public registry host with `mcp-runtime-registry-pull`; an in-cluster pull with the secret succeeds and one without it is refused |
| `ui` | Platform UI HTML, `/login`, security headers and plain-HTTP behavior recorded |
| `oidc` | mcp-auth discovery, authorization-server metadata, JWKS, `auth provider-check`, optional Keycloak test-user token (skipped when `E2E_WITH_MCP_AUTH` is off) |
| `adapter-enrollment` | On a cert-only gateway MCPServer (omit `spec.auth`) and a grant, `adapter enroll` issues a session-bound SPIFFE certificate; HTTPS calls through `https://mcp.e2e.mcpruntime.org/<server>/mcp` with that client certificate reach the granted tool, while an ungranted tool (403), tools/call without a certificate (401), and the same certificate on another server (401 `session_not_found`) are refused. When `E2E_MCP_OAUTH_ACCESS_TOKEN` is set for the managed Globex member and MCP resource, a second OAuth+cert pair also proves bearer requirement, wrong-audience/`session_not_found` denial, and the same grant/deny matrix. Ungranted agents are refused a session. Skipped when the ref has no `adapter enroll`, no mTLS issuer, or the operator lacks `MCP_ADAPTER_CERTIFICATES=true`. On failure, `adapter-enrollment/` holds the redacted routing, certificate, policy, and service diagnostics. |
| `multitenancy` | `hack/deploy/mcpruntime-org/multitenancy-test.sh`: teams/users, `server build image` -> `server push` -> `server deploy`, pull-secret checks, grants, adapter tool calls, direct-call denial, server events |
| `governance` | Granted agent session allowed; ungranted agent and forged-session tool calls denied |
| `analytics` | Analytics API stats/events and ClickHouse rows for the tenant server |
| `collect-diagnostics`, `snapshot`, `teardown` | Diagnostics, TLS snapshot refresh (kept only when complete), VM wipe confirmed |

## Snapshot and cleanup

Before uninstalling k3s, the runner refreshes the TLS snapshot on the VM with
the deployment backup helper under
`/var/lib/mcp-runtime-e2e-backup/platform-runtime/<timestamp>` and a `latest`
link. A run that never issued the public certificates keeps the
previous snapshot instead of replacing it with an empty one. The next run
restores the snapshot before setup so cert-manager reuses the certificates.
This VM-side directory is the source of truth; uploaded GitHub artifacts
contain diagnostics only.

If certificates, registry credentials, or an identity-provider installation
must survive a VM reset, place access-controlled backup material in the same
directory. The runner supports either an executable `restore.sh` hook or
declarative files below `manifests/`. Cleanup removes everything except the
backup directory: k3s with its CNI and kubelet state, all Docker images,
containers, volumes and build cache, and any repository or scratch directory an
earlier run left behind. The runner checks free disk before setup (about 6 GiB,
`E2E_MIN_DISK_GIB` overrides it) because a full disk evicts pods and surfaces
only as an unexplained deployment timeout. On-VM run directories live in
`/var/lib/mcp-runtime-e2e-backup/runs/<run-id>`; the ten most recent are kept.

## Running it

The **Pre-release Regression** workflow calls **Staging E2E (Disposable VM)**
with the multi-tenancy flow enabled and the existing staging TLS snapshot.
Staging is the only cluster suite in Pre-release Regression; Kind QA stays in
PR CI. The staging setup currently uses tenant platform mode.
Merges to `main` do not run it. The job holds the `staging-e2e-disposable-vm`
lock, so only one Staging run uses the VM at a time. Pull requests do not
receive the disposable-VM secrets; QA E2E runs on GitHub runners instead.
Dispatch the workflow manually when you need staging evidence before a release
or are iterating on a PR:

```bash
gh workflow run staging-e2e.yaml --ref <branch> -f run-multitenancy=true
# Add this input only when a fresh staging certificate is part of the check:
gh workflow run staging-e2e.yaml --ref <branch> -f run-multitenancy=true -f fresh-certificate=true
```

`gh workflow run` only dispatches workflow files that exist on the default
branch. The runner accepts `1`/`true`/`yes`/`on` for boolean `E2E_*` values.

The runner needs a Go toolchain on the VM new enough to
honor the `go` directive in `go.mod`; it selects the newest installed toolchain
and installs one when none is new enough.

Clean runs download the k3s installer from `https://get.k3s.io` with bounded
retries. If that endpoint fails, they use the official repository's `install.sh`
pinned to commit `fb46cc3da277692c5ec9ec6da20aaaf63f256864` (reviewed 2026-09-29).
The runner executes the file only after a complete download and shell syntax
check. Update that source pin when reviewing upstream installer changes.

The guard and stage runner have offline tests that CI runs:

```bash
bash test/e2e/staging_lib_test.sh
```

The local Kind suite remains the fast test-mode path:

```bash
E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS=all bash test/e2e/qa-e2e.sh
```
