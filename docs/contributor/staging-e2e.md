# Staging E2E on the disposable VM

Staging E2E exercises the full production install path using
`setup --strict-prod`, public TLS, the bundled HTTPS registry, the platform
API/UI, tenants, grants, adapters, and analytics on a dedicated **disposable** VM whose
hostnames live under `*.e2e.mcpruntime.org`. It is intentionally separate from
the Kind suite. It was previously called "Production E2E"; the name changed
because it never touches the production cluster.

## Two runners

| Runner | Workflow | Where the CLI runs |
| --- | --- | --- |
| `test/e2e/staging-remote.sh` | `Staging E2E (Remote Cluster)` | On the runner, against the VM's kubeconfig |
| `test/e2e/staging-vm.sh` | `Staging E2E (Disposable VM)` | On the VM itself, over SSH |

Both share their assertions, the target guard, and the stage runner through
`test/e2e/lib/staging.sh`, and both workflows share one concurrency group so
they never drive the VM at the same time.

The remote runner models how an operator actually installs MCP Runtime, using the
CLI on a workstation or CI runner and the cluster reached through a kubeconfig.
It also avoids environment failures the on-VM runner is exposed to (no repository
tarball, no login-shell working directory, the runner's own Go and Docker, and
no dependence on one SSH session staying open through multi-minute image
builds). The on-VM runner exercises the path where the CLI runs next to k3s.

Image pushes need no registry reachability from the runner. Setup runs
`docker save` locally and starts a short-lived helper pod inside the cluster
that pushes into the internal registry, so only the Kubernetes API must be
reachable. k3s already lists the node's public IP in the API server
certificate SANs, so the fetched kubeconfig only needs its loopback server URL
rewritten.

## Safety: the disposable-target guard

The suite installs and uninstalls k3s, prunes every Docker image, and wipes
kubelet/CNI state. It must never run against the live production install
(`platform.mcpruntime.org`, `registry.mcpruntime.org`, `mcp.mcpruntime.org`,
`auth.mcpruntime.org`). Before anything is copied to or run on the VM, the
workflows run `test/e2e/staging-target.sh check`, and each runner repeats the
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

or, from CI, by dispatching either workflow once with
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
layered on OAuth MCPServer routes, so the runners also export
`MCP_ADAPTER_CERTIFICATES=true`, `MCP_TRUST_DOMAIN` (default
`e2e.mcpruntime.org`; override with `E2E_ADAPTER_TRUST_DOMAIN`), and
`MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-servers` before setup. Path routes
share `mcp.e2e.mcpruntime.org`, so the operator keeps one Traefik `default`
TLSOption in that namespace that requests, but never requires, a client
certificate. Set `E2E_ADAPTER_CERTIFICATES=0`, or `E2E_MTLS_CLUSTER_ISSUER` to
an empty value, to skip the adapter-enrollment stage.

The connector name is only an E2E configuration choice. MCP Runtime's auth
server is provider-agnostic; Keycloak gives the test an isolated OIDC issuer
and test user.

## Workflow inputs

| Input | Default | Effect |
| --- | --- | --- |
| `run-multitenancy` | `true` | Multi-team build/push/deploy, grants, adapter calls, governance deny paths, tenant analytics |
| `fresh-certificate` | `false` | Issue a brand-new staging certificate for `run-<id>.e2e.mcpruntime.org`, serve it through Traefik, and verify it; routine runs reuse the TLS snapshot |
| `bootstrap-disposable-marker` | `false` | One-time marker bootstrap (see above) |

Both runners use the Let's Encrypt **staging** CA by default (`E2E_ACME_STAGING=1`).
The production CA allows five certificates per exact set of identifiers per
week; staging exercises the identical ACME order, HTTP-01 challenge, and
cert-manager path with far higher limits. The runners install the staging roots
into the VM trust store before k3s starts (containerd needs them to pull from
the registry) and, on the remote runner, into the process trust bundle, so
every HTTPS check verifies the chain properly instead of using `curl -k`.
Use `fresh-certificate` sparingly and `E2E_ACME_STAGING=0` only for an
occasional production-CA run.

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
| `staging-roots` | Let's Encrypt staging roots trusted on the VM (and runner) |
| `k3s`, `traefik` | Node Ready, kubeconfig reachable, disk headroom, bundled Traefik exposed |
| `doctor-before` | Advisory pre-setup `cluster doctor` |
| `restore-snapshot` | TLS snapshot restored before setup so cert-manager reuses issued certificates |
| `setup` | `setup --strict-prod --with-tls --acme-staging --registry-mode bundled-https --mtls-cluster-issuer ...` |
| `diagnostics` | Post-setup `cluster diagnostics`, `cluster doctor`, `cluster status` |
| `rollouts` | Every Deployment/StatefulSet in `mcp-runtime`, `mcp-sentinel`, `registry`, `cert-manager`, Traefik rolled out; no pod stuck in image pull or crash loop |
| `cluster-issuer` | ACME ClusterIssuer Ready and pointed at the staging (or production) directory |
| `certificates` | `registry/registry-cert` and `mcp-sentinel/mcp-sentinel-platform-tls` Ready, expected issuer, SANs cover the hosts, not expiring |
| `tls-endpoints` | `openssl s_client` chain verification, hostname match, and staging issuer for platform/registry/mcp (and auth) |
| `fresh-certificate` | Gated fresh issuance for a unique host, served and verified end to end |
| `platform-login` | Admin token, `auth login`, `auth status`, `status`, `server list`, `registry info`, command help surfaces |
| `platform-api` | Admin API calls; anonymous and bad-key requests denied; admin password login (password over stdin) and wrong-password denial |
| `registry-auth` | Anonymous `/v2/`, manifest HEAD, and push denied; authenticated manifest HEAD, blob + manifest push, and pull allowed |
| `image-pulls` | Platform workloads pull from the public registry host with `mcp-runtime-registry-pull`; an in-cluster pull with the secret succeeds and one without it is refused |
| `ui` | Platform UI HTML, `/login`, security headers and plain-HTTP behavior recorded |
| `oidc` | mcp-auth discovery, authorization-server metadata, JWKS, `auth provider-check`, optional Keycloak test-user token (skipped when `E2E_WITH_MCP_AUTH` is off) |
| `adapter-enrollment` | On an OAuth MCPServer (doctor-smoke upstream) and a grant, `adapter enroll` with the admin's password-login token issues a `spiffe://<trust>/ns/mcp-servers/session/<id>` client certificate backed by an MCPAgentSession; once the gateway policy binds the session, a certificate-authenticated call through `https://mcp.e2e.mcpruntime.org/<server>/mcp` reaches the granted tool, while an ungranted tool (403), no certificate (401), and the same certificate on another OAuth server (401 `session_not_found`) are refused; an ungranted agent is refused a session. Skipped when the ref has no `adapter enroll`, no mTLS issuer, or the operator lacks `MCP_ADAPTER_CERTIFICATES=true`. On failure, `adapter-enrollment/` holds the servers, Traefik CRs, CertificateRequests, gateway policy, and operator/runtime-api/gateway/Traefik logs |
| `multitenancy` | `hack/deploy/mcpruntime-org/multitenancy-test.sh`: teams/users, `server build image` -> `server push` -> `server deploy`, pull-secret checks, grants, adapter tool calls, direct-call denial, server events |
| `governance` | Granted agent session allowed; ungranted agent and forged-session tool calls denied |
| `analytics` | Analytics API stats/events and ClickHouse rows for the tenant server |
| `collect-diagnostics`, `snapshot`, `teardown` | Diagnostics, TLS snapshot refresh (kept only when complete), VM wipe confirmed |

## Snapshot and cleanup

Before uninstalling k3s, the runners refresh the TLS snapshot on the VM (the
remote runner stores certificate Secrets and Certificates under
`/var/lib/mcp-runtime-e2e-backup/platform-runtime/tls`; the on-VM runner uses
the deployment backup helper under `platform-runtime/<timestamp>` with a
`latest` link). A run that never issued the public certificates keeps the
previous snapshot instead of replacing it with an empty one. The next run
restores the snapshot before setup so cert-manager reuses the certificates.
This VM-side directory is the source of truth; uploaded GitHub artifacts
contain diagnostics only.

If certificates, registry credentials, or an identity-provider installation
must survive a VM reset, place access-controlled backup material in the same
directory. The on-VM runner supports either an executable `restore.sh` hook or
declarative files below `manifests/`. Cleanup removes everything except the
backup directory: k3s with its CNI and kubelet state, all Docker images,
containers, volumes and build cache, and any repository or scratch directory an
earlier run left behind. The runners check free disk before setup (about 6 GiB,
`E2E_MIN_DISK_GIB` overrides it) because a full disk evicts pods and surfaces
only as an unexplained deployment timeout. On-VM run directories live in
`/var/lib/mcp-runtime-e2e-backup/runs/<run-id>`; the ten most recent are kept.

## Running it

Relevant changes pushed to `main` automatically run the **Staging E2E
(Disposable VM)** workflow with the multi-tenancy flow enabled and the existing
staging TLS snapshot. Pull requests do not receive the disposable-VM secrets;
dispatch a workflow manually when you need staging evidence before merge or
are iterating on a PR. The remote workflow keeps the repository on the runner;
the disposable-VM workflow packages it and runs it on the VM:

```bash
gh workflow run staging-e2e-remote.yaml --ref <branch> -f run-multitenancy=true
gh workflow run staging-e2e.yaml --ref <branch> -f run-multitenancy=true
# Add this input only when a fresh staging certificate is part of the check:
gh workflow run staging-e2e.yaml --ref <branch> -f run-multitenancy=true -f fresh-certificate=true
```

`gh workflow run` only dispatches workflow files that exist on the default
branch. The runners accept `1`/`true`/`yes`/`on` for boolean `E2E_*` values.

The remote runner can also be driven directly, which is the fastest way to
iterate on a failure:

```bash
E2E_VM_HOST=<vm-address> E2E_CLEANUP=0 bash test/e2e/staging-remote.sh
```

`E2E_CLEANUP=0` keeps the cluster up so a failure can be inspected. Any
`E2E_*` value already exported wins; anything missing is read from the VM's
`e2e.env`. When the cluster nodes and the machine running the CLI differ in
architecture, setup builds for the node's platform, so a non-amd64
workstation needs an emulator registered
(`docker run --privileged --rm tonistiigi/binfmt --install amd64`).

The on-VM runner additionally needs a Go toolchain on the VM new enough to
honor the `go` directive in `go.mod`; it selects the newest installed toolchain
and installs one when none is new enough.

The guard and stage runner have offline tests that CI runs:

```bash
bash test/e2e/staging_lib_test.sh
```

The local Kind suite remains the fast test-mode path:

```bash
E2E_VALIDATE_SCENARIOS_ONLY=1 E2E_SCENARIOS=all bash test/e2e/kind.sh
```
