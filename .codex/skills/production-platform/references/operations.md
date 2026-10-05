
# Production Operations

Use this skill for MCP Runtime production Kubernetes operations. The current
`mcpruntime.org` deployment uses k3s. For
distribution-neutral target selection and kubeconfig setup, start with
[`docs/deployment-targets.md`](../../../docs/deployment-targets.md); this skill
and its scripts cover the tested public k3s implementation. Prefer documented
user-facing commands and scripts over private shortcuts.

## Source Of Truth

- Env profile: `config/deployments/mcpruntime-org.env`
- Example profile: `config/deployments/mcpruntime-org.env.example`
- Runbook: `docs/reference-deployment.md`
- Readiness/debug guide: `docs/cluster-readiness.md`
- Scripts (canonical): `hack/deploy/mcpruntime-org/{backup,setup,clean,restore,rollout,multitenancy-test}.sh`
- Script index: `hack/README.md`
- User path: `docs/hosted-quickstart.md` (published CLI install, hosted platform login,
  server publish, grant, adapter, and analytics UI)
- CLI release workflow: `.github/workflows/release.yaml`

## Current production VM

- SSH host: `root@${MCP_PRODUCTION_SSH_HOST}` from `config/deployments/mcpruntime-org.env`
- Preferred workstation key: `~/.ssh/id_ed25519`
- Production kubeconfig on the contributor workstation:
  `~/.kube/prod-mcp-runtime-config`, context `prod-mcp-runtime`. The default
  `~/.kube/config` stays on the test context `test-mcp-runtime`; do not merge
  production credentials into it. If the named production file is unavailable,
  follow [Obtain and select cluster access](../../../../docs/reference-deployment.md#obtain-and-select-cluster-access)
  to retrieve a kubeconfig securely and validate its API endpoint, context, and
  TLS. Never assume a contributor temp path exists or commit kubeconfig material.

Use the VM password only for a one-time interactive SSH-key installation. Never
store that password in this skill, `AGENTS.md`, repository env files, shell
history, or command arguments. After key installation, verify:

```bash
source config/deployments/mcpruntime-org.env
ssh root@"${MCP_PRODUCTION_SSH_HOST}" 'hostname && kubectl config current-context'
```

Then copy or provision the approved kubeconfig locally. Keep the default
context on test and pass the production file explicitly to each production
command. Confirm the context and run `cluster doctor` before any production
mutation:

```bash
PROD_KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config"
kubectl --kubeconfig "$PROD_KUBECONFIG" --context prod-mcp-runtime get nodes
KUBECONFIG="$PROD_KUBECONFIG" ./bin/mcp-runtime cluster doctor
```

## Non-Negotiables

- For production debugging, inspect Prometheus metrics, Loki logs, and Tempo
  traces at `https://platform.mcpruntime.org/grafana` for the same incident
  window. Load private credentials from `~/.mcpruntime/infra.env` without
  displaying them. Verify coverage, freshness, and request/trace correlation;
  do not infer health from missing telemetry. Follow the
  [production observability workflow](../../../../docs/reference-deployment.md#production-observability-and-debugging).
  For each concrete maintainability or debugging gap, find or create an
  evidence-based repository issue and attach it to
  [Maintainability and Debuggability Improvement](https://github.com/orgs/mcp-runtime/projects/1).

- Manage supported operations through the CLI and platform UI. If team,
  publishing, deployment, access, update, or cleanup commands fail, reproduce
  the failure, add regression tests, fix the product path, open a focused PR,
  and re-run the same CLI/UI workflow. Do not use manual Kubernetes, database,
  or direct API writes to bypass failures. Read-only diagnostics are allowed;
  direct Kubernetes mutations require an explicitly requested Kubernetes path
  or a test of that path and never count as CLI/UI acceptance.
- Before a production setup redeployment, run
  `hack/deploy/mcpruntime-org/backup.sh --setup`. It captures Kubernetes
  resource definitions (including PVC/PV specs), Secrets, and platform restore
  inputs without copying live PVC contents. Before destructive cleanup or full
  node recovery, use `hack/deploy/mcpruntime-org/backup.sh --full
  --online-copy` to also capture the K3s database and local-path files. Live
  volume copies are not application-consistent and may need database WAL
  recovery. Bundles are not encrypted; store them on encrypted storage and
  copy them off-host.
- Before every production deployment, ask which MCP Runtime branch or ref to
  build and deploy. Show the current branch/ref and working-tree state as
  context, but do not silently choose `main` or the currently checked-out ref.
- Build `./bin/mcp-runtime` from the selected ref before setup, rollout, or
  validation. Confirm the binary was built from that ref before using it;
  stale CLI binaries can stamp stale registry image refs. Do not switch refs or
  discard local changes without the user's direction.
- For production image builds, use the workstation's selected Docker daemon
  and set `MCP_IMAGE_PLATFORM` to the target node architecture (currently
  `linux/amd64`). Keep the default kubeconfig on test; set `KUBECONFIG` to the
  production file only for each production command. Use
  `MCP_REGISTRY_PUSH_MODE=public` to push images to
  `registry.<domain>/<image>:<unique-tag>`.
- mcp-auth is a separate release track. Leave its Deployment unchanged unless
  an update is requested. The default candidate source is the published Docker
  Hub image `docker.io/princekrroshan01/mcp-auth-server:latest`; copy it into
  the Runtime registry under a unique `MCP_AUTH_IMAGE_TAG`. Build from
  `/Users/proshan/mcp-auth` only when intentionally testing source changes;
  then require a selected `MCP_AUTH_BUILD_REF`, a clean checkout whose HEAD
  matches that ref, and `MCP_AUTH_IMAGE_SOURCE=local`. Both paths preserve the
  existing auth config, data PVC, signing key, and TLS Secret.
- CIMD support in mcp-auth is enabled by default. Set
  `MCP_AUTH_CLIENT_ID_METADATA_ENABLED=false` only when explicitly opting out.
  After rollout, verify the public authorization metadata advertises support
  and use a new or cleared Claude/Codex OAuth client entry so cached DCR
  credentials do not mask CIMD behavior. See the TypeScript SDK resource example
  in `examples/oauth-example-typescript-2025-06-18/`.
- Ask whether this rollout should update mcp-auth. If yes, ask whether to
  deploy published Docker Hub `latest` (recommended) or intentionally build a
  selected local mcp-auth ref for source testing. For local-source testing,
  inspect `/Users/proshan/mcp-auth`, check out the requested ref only with the
  user's direction, and require a clean worktree. For a published-image
  update, use `MCP_AUTH_IMAGE_SOURCE=published`; do not build the sibling repo.
  If no update is requested, retain the currently deployed mcp-auth image.
- Treat the selected Runtime ref and the mcp-auth build choice as required
  inputs to each production rollout, even when a user has already authorized
  deployment generally. Do not begin production mutation until both are clear.
- Keep candidate deployment and release publication separate. A platform image
  rollout does not publish new CLI binaries, and a GitHub CLI release does not
  deploy platform images. Do not tag/publish the Runtime or mcp-auth release
  until the candidate has passed the user path below and the user explicitly
  asks to publish it.
- Before running the deployment command, inspect its `--help`, the rollout
  script, and the production env profile, then ask about deployment flags or
  overrides that affect this rollout. Present only relevant choices together
  with the profile's current values and recommended defaults; do not invent
  values or silently override production settings. Wait for answers to any
  unresolved flag choices before mutating production.
- Preserve existing production certificates. Prefer the targeted rollout path
  for application image updates; do not run setup in a way that requests or
  reissues certificates as part of an ordinary deploy. Before any setup or TLS
  operation, inspect existing Certificate/Secret readiness and the configured
  ClusterIssuer, and retain the current issuer and TLS Secret references. Only
  request certificate issuance when the user explicitly asks for a TLS change
  or inspection shows issuance is necessary, and explain that consequence
  before proceeding.
- After rollout, verify the candidate through the documented customer journey:
  use the built CLI against `https://platform.mcpruntime.org` to log in,
  publish a uniquely named temporary `qa-audit-*` MCP server, grant an agent,
  call a tool through the adapter, and confirm the event in **Analytics →
  Tools** in the platform UI. Run browser checks for signed-out and signed-in
  UI surfaces when credentials are available. Clean up every temporary resource
  and report any skipped authenticated/browser flow as blocked; do not treat
  `cluster doctor` alone as release acceptance.
- The Staging E2E suites (`test/e2e/staging-remote.sh`, `staging-vm.sh`, see
  [Staging E2E](#staging-e2e-disposable-vm)) reset and provision the
  disposable VM. Never point them at the `mcpruntime.org` production cluster;
  use targeted temporary user resources for production acceptance instead.
- `cluster doctor` uses `KUBECONFIG` env, not `--kubeconfig`:

```bash
KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config" ./bin/mcp-runtime cluster doctor
```

- For public bundled HTTPS, platform and tenant pull refs should use the
  TLS-covered hostname, for example `registry.mcpruntime.org/...`.
- Do not use a registry Service ClusterIP as a bundled-HTTPS pull ref unless
  the cert has the matching IP SAN. The usual symptom is:
  `x509: cannot validate certificate for <ClusterIP> because it doesn't contain any IP SANs`.
- Pod DNS and node image-pull DNS are different. Pods can resolve
  `registry.registry.svc.cluster.local`; k3s/containerd on the node usually
  cannot unless `/etc/rancher/k3s/registries.yaml` explicitly mirrors that
  exact host.
- Public registry ingress is auth-protected. Platform workloads using
  `registry.<domain>` need an image pull secret; setup should create and attach
  `mcp-runtime-registry-pull` for platform namespaces before registry auth is
  re-enabled. Unauthenticated pulls may fail with `no basic auth credentials`.

## Public k3s Setup Validation

Run the actual script path:

```bash
bash -n hack/deploy/mcpruntime-org/setup.sh
bash hack/deploy/mcpruntime-org/setup.sh
KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config" ./bin/mcp-runtime cluster doctor
kubectl --kubeconfig "$HOME/.kube/prod-mcp-runtime-config" --context prod-mcp-runtime get pods -A
```

Healthy setup signs:

- setup ends with `Platform setup complete`
- no `ErrImagePull` / `ImagePullBackOff`
- operator and Sentinel images use `registry.<domain>:<tag>`, not a Service IP
- operator and Sentinel workloads reference `mcp-runtime-registry-pull` when
  they pull from the public registry hostname
- `cluster doctor` passes all checks

## Clean / Restore Validation

`hack/deploy/mcpruntime-org/clean.sh --restore-platform` must work after setup.
It restores TLS and cert-manager runtime material only; it must not restore
tenant/user data.

Validate:

```bash
bash -n hack/deploy/mcpruntime-org/clean.sh
MCP_DEPLOY_ENV=config/deployments/mcpruntime-org.env \
  hack/deploy/mcpruntime-org/clean.sh --restore-platform
```

If restore hits Kubernetes metadata conflicts, sanitize backup manifests before
apply. Do not apply stale `resourceVersion`, `uid`, `managedFields`, or
`kubectl.kubernetes.io/last-applied-configuration`.

## Rollout Validation

`hack/deploy/mcpruntime-org/rollout.sh` is a live script. It must:

- rebuild `./bin/mcp-runtime`
- build API/UI images for `MCP_IMAGE_PLATFORM` using the workstation's
  selected Docker daemon
- push image blobs into the bundled registry (public mode uses the
  `registry.<domain>` hostname and the existing platform registry credential)
- optionally update mcp-auth only when requested: published Docker Hub image
  by default, or local source when intentionally testing a confirmed clean ref
  with `MCP_AUTH_IMAGE_SOURCE=local`
- deploy API/UI refs as `registry.<domain>/<repo>:<tag>`
- ensure `mcp-platform/mcp-runtime-registry-pull` exists and is attached to
  API/UI deployments
- finish both rollout status checks successfully

Run with a unique tag. For the public production cluster, set
`MCP_IMAGE_PLATFORM=linux/amd64`, set `MCP_REGISTRY_PUSH_MODE=public`, and
point `KUBECONFIG` and `MCP_SETUP_KUBECONFIG` to
`$HOME/.kube/prod-mcp-runtime-config` and select `prod-mcp-runtime`. Do not
change the default test context. The production profile remains the source for
the domain and other deployment settings.

The user-facing release check is separate from the rollout command. Follow
`docs/hosted-quickstart.md` with the candidate CLI, then verify the same server,
connect configuration, and Analytics → Tools output in the hosted UI. The
GitHub release workflow only publishes CLI binaries; do not publish a new CLI
or mcp-auth release until these checks pass.

Run with a unique tag:

```bash
MCP_ROLLOUT_TAG=verify-rollout-$(date +%m%d%H%M%S) \
  bash hack/deploy/mcpruntime-org/rollout.sh
```

Then verify:

```bash
kubectl --kubeconfig "$KUBECONFIG" -n mcp-platform \
  get deploy mcp-platform-api mcp-runtime-api mcp-ui \
  -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{range .spec.template.spec.imagePullSecrets[*]}{.name}{","}{end}{"|"}{range .spec.template.spec.containers[*]}{.image}{";"}{end}{"|"}{.status.readyReplicas}{"/"}{.status.replicas}{"\n"}{end}'
kubectl --kubeconfig "$KUBECONFIG" -n mcp-observability \
  get deploy mcp-analytics-api \
  -o jsonpath='{range .items[*]}{.metadata.name}{"|"}{range .spec.template.spec.imagePullSecrets[*]}{.name}{","}{end}{"|"}{range .spec.template.spec.containers[*]}{.image}{";"}{end}{"|"}{.status.readyReplicas}{"/"}{.status.replicas}{"\n"}{end}'

KUBECONFIG="$HOME/.kube/prod-mcp-runtime-config" ./bin/mcp-runtime cluster doctor
```

## Registry Debug Shortcuts

Get admin key:

```bash
ADMIN_KEY="$(kubectl --kubeconfig "$KUBECONFIG" \
  get secret mcp-ui-credentials -n mcp-platform \
  -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
```

Check public registry route:

```bash
curl -k -i -H "x-api-key: $ADMIN_KEY" https://registry.mcpruntime.org/v2/
curl -k -I -u "platform-service:$ADMIN_KEY" \
  https://registry.mcpruntime.org/v2/mcp-platform-api/manifests/<tag>
```

Expected:

- admin or Basic-auth request returns 200 for existing manifests
- no-auth public request returns 401/403
- Traefik 404 means ingress/router is wrong, not image data

## Multitenancy Script

`hack/deploy/mcpruntime-org/multitenancy-test.sh` is intentionally platform-API-only. It unsets
`KUBECONFIG`. Validate it with public endpoints:

This production QA must exercise `server build image`, `server push`, and
`server deploy` through the platform CLI. To additionally verify the
namespace-local image pull Secret after each deploy, run with
`VERIFY_DEPLOY_PULL_SECRET=1` and a production `KUBECONFIG`; the script keeps
KUBECONFIG unset for CLI calls and uses the saved path only for read-only checks.

```bash
PLATFORM_URL=https://platform.mcpruntime.org \
MCP_URL=https://mcp.mcpruntime.org \
REGISTRY_HOST=registry.mcpruntime.org \
ADMIN_EMAIL=admin@mcpruntime.org \
ADMIN_PASSWORD='...' \
hack/deploy/mcpruntime-org/multitenancy-test.sh
```

Use `SKIP_SETUP=1` only after the generated teams, users, servers, grants, and
sessions already exist for the selected `RUN_ID`.

## Staging E2E (disposable VM)

Runbook: [`docs/contributor/staging-e2e.md`](../../../docs/contributor/staging-e2e.md).
The on-VM staging workflow runs automatically after relevant changes land on
`main`; the remote workflow is manual. CI runs QA E2E on PRs or manual dispatch,
using `test/e2e/qa-e2e.sh` with Kind, and skips it on main pushes.

Workflows `Staging E2E (Disposable VM)` / `Staging E2E (Remote Cluster)` drive
the full strict-prod install on the disposable VM (`*.e2e.mcpruntime.org`) and
upload `summary.md`, `summary.json`, `stages/NN-<stage>.log`, and
`diagnostics/`. Start triage from the first failed stage in `summary.md`.
Both runners enable the content-hash GHCR image cache for setup. The remote
runner logs in to GHCR after the target guard; the on-VM runner receives the
workflow's package-read token over SSH stdin and removes its temporary Docker
config during cleanup. A cache miss builds locally without publishing from
Staging. Use `E2E_IMAGE_CACHE=0` when testing a full image rebuild.

Safety rules:

- The runners refuse to start unless `test/e2e/lib/staging.sh` proves the
  target is disposable: E2E hosts under `.e2e.mcpruntime.org`, no address
  shared with the production hostnames (resolved by DNS at run time; never
  hardcode production addresses), E2E hosts resolving to the VM, and the
  `/var/lib/mcp-runtime-e2e-backup/DISPOSABLE` marker.
- The marker is created only by the explicit one-time bootstrap on a freshly
  provisioned disposable VM:
  `E2E_VM_HOST=<vm> E2E_CONFIRM_DISPOSABLE_VM=<vm> bash test/e2e/staging-target.sh bootstrap`
  (or one workflow dispatch with `bootstrap-disposable-marker=true`). Never
  bootstrap the production VM, and never set the `E2E_GUARD_ALLOW_*` escape
  hatches in CI.
- Keep `E2E_ACME_STAGING` on. Use `fresh-certificate=true` sparingly; routine
  reruns reuse the TLS snapshot.
- The two workflows share a concurrency group; run one at a time and do not
  cancel other people's runs.

Gotchas seen on real runs:

- `x509: certificate signed by unknown authority` pulling
  `registry.e2e.mcpruntime.org/...`: the node does not trust the Let's Encrypt
  staging roots. The `staging-roots` stage installs them before k3s starts;
  containerd must be (re)started after they are added.
- "TLS snapshot incomplete": setup died before the platform certificate was
  issued, so the previous snapshot was kept on purpose.
- `adapter-enrollment` needs the opt-in adapter-certificate platform feature:
  the runners export `MCP_ADAPTER_CERTIFICATES=true`, `MCP_TRUST_DOMAIN`, and
  `MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE=mcp-servers` before setup and the
  stage enrolls on a gateway-enabled MCPServer. OAuth is optional and is
  enabled only when `spec.auth` is present; adapter PKI is platform-wide. On failure, open the
  stage's `adapter-enrollment/` evidence directory first.
- Exit 255 in the on-VM workflow is an SSH drop, not a test failure; the step
  uses keepalives, and the remote runner avoids the long-lived session.
