
# Kubernetes Hardening Audit

Prefer the deterministic inventory/hygiene script first:

```bash
export KUBECONFIG="${KUBECONFIG:-$HOME/.kube/test-mcp-runtime-config}"
bash hack/cluster-ops/k8s-hardening-check.sh
```

Use this reference for RBAC graph judgment (rakkess), CIS depth, and admission
policy review.

## Overview

For a design critique of proposed RBAC, Pod Security, or NetworkPolicy choices,
read [the shared design principles](../../_shared/design-principles.md). Skip it
for a posture audit of existing manifests and clusters.

Use this skill to assess MCP Runtime's cluster posture: RBAC graph, Pod
Security Standards, NetworkPolicy enforcement, manifest hygiene, and CIS
benchmark compliance. Findings use the shared template at
`../_shared/FINDINGS-TEMPLATE.md`.

For runtime authn/authz, gateway policy, and protocol fuzzing, use
`security-audit`. For images and supply chain, use
`security-audit`.

## Step 1 — Inventory

Select the cluster explicitly before running live commands. For contributor
QA, use `$HOME/.kube/test-mcp-runtime-config` with context
`test-mcp-runtime`; never let these commands inherit the production context.
For a requested production posture audit, select the approved production
kubeconfig explicitly and label every live finding with that context.

Repo-owned namespaces (per `CLAUDE.md`):

- `mcp-runtime` — operator.
- `mcp-platform` — platform-api, runtime-api, UI, gateway, Postgres, optional mcp-auth.
- `mcp-observability` — analytics-api, ingest, processor, ClickHouse, Kafka, Grafana, Prometheus, Loki, Tempo, otel-collector.
- `mcp-log-collector` — Promtail.
- `mcp-servers` — user MCP server workloads, gateway sidecars.
- `registry` — Distribution v2 registry.
- `traefik` — ingress controller (or `kube-system/traefik` on k3s).

Only inspect namespaces that exist in the selected cluster. k3s uses the
external Traefik installation in `kube-system`, while a repo-managed install
may use `traefik`.

For each existing namespace:

```sh
NS=mcp-platform
kubectl -n "$NS" get all,sa,role,rolebinding,networkpolicy -o name
kubectl get clusterrole,clusterrolebinding -o name | grep -iE 'mcp|sentinel'
```

Record SA → Role/ClusterRole bindings as a graph; this becomes the input to
RBAC analysis.

## Step 2 — RBAC graph analysis

Install the helpers:

```sh
go install github.com/corneliusweig/rakkess/cmd/rakkess@latest
go install github.com/reactiveops/rbac-lookup@latest
```

Per ServiceAccount:

```sh
for sa in $(kubectl -n mcp-platform get sa -o name); do
  echo "=== $sa ==="
  rakkess --sa "$(echo $sa | cut -d/ -f2)" --namespace mcp-platform
done
```

Findings to flag:

- `*` verbs on `secrets` → **High** unless explicitly justified by a single
  controller and scoped to a single namespace.
- Cluster-scoped roles on workload SAs → **High**; scope to a Role unless
  the controller genuinely watches cluster-wide.
- `pods/exec`, `pods/portforward`, `pods/attach` → **High** outside
  break-glass paths.
- `serviceaccounts/token` create → **High**; token impersonation primitive.
- `escalate` or `bind` on roles → **Critical**.
- Bindings to `system:authenticated` or `system:unauthenticated` → **Critical**.

Confirm Traefik narrowing (CLAUDE.md): bundled manifests should watch only
`registry`, `mcp-platform`, `mcp-observability`, `mcp-servers`. Any extra namespace with a
`traefik-watch` Role binding without a documented reason is a Medium finding.

## Step 3 — Pod Security Standards

The repo claims "restricted pod-security labels in repo-owned namespaces."
Verify every namespace label:

```sh
kubectl get ns mcp-runtime mcp-platform mcp-observability mcp-log-collector mcp-servers registry traefik \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.metadata.labels}{"\n"}{end}'
```

Compare each existing namespace with the namespace manifest or provisioning
code that owns it; do not assume one PSS level applies to every namespace.
For example, the operator namespace is restricted, while the bundled registry
namespace intentionally uses `enforce=baseline` and `audit/warn=restricted`
(`config/registry/base/namespace.yaml`). External Traefik namespaces such as
`kube-system` are not repo-owned. Treat unexplained weakening from the owning
manifest as a finding.

Where the owning namespace policy requires restricted, verify:

- `pod-security.kubernetes.io/enforce=restricted`
- `pod-security.kubernetes.io/audit=restricted`
- `pod-security.kubernetes.io/warn=restricted`


Then run `kube-linter` against checked-in manifests:

```sh
go install golang.stackrox.io/kube-linter/cmd/kube-linter@latest
kube-linter lint k8s/ config/
```

Triage every check; common ones to confirm pass on every Deployment:

- `run-as-non-root`
- `read-only-root-filesystem`
- `drop-net-raw-capability` and `drop-all-capabilities`
- `no-host-network`, `no-host-pid`, `no-host-ipc`
- `privileged-container` absent
- `seccomp-profile-set` (`RuntimeDefault`)
- `cpu-requests`, `memory-requests`, `memory-limits` present
- `liveness-probe` and `readiness-probe` defined
- `image-tag-not-latest` (tag is a digest or stable version, not `latest`)

Each violation is a finding. Severity: **High** for missing non-root and
read-only root FS on internet-facing pods; **Medium** for missing seccomp
or limits; **Low** for missing probes on jobs.

## Step 4 — NetworkPolicy audit

```sh
kubectl get networkpolicy -A
```

For each namespace that holds workloads, confirm:

- A default-deny ingress policy exists.
- A default-deny egress policy exists or egress is explicitly allowed only
  to documented destinations (DNS, ingest service, k8s API).
- The mcp-server gateway sidecar can reach the ingest endpoint, the k8s
  API (for SAR/auth), and the user MCP container — and nothing else.
- User MCP server containers cannot egress to `mcp-platform` or `mcp-observability` directly,
  only via the sidecar.
- The registry namespace egress is limited (no random outbound).

The probe creates a temporary pod and sends traffic. Run it only in an
explicitly selected disposable test namespace; do not run it against production
without specific authorization. Clean up the pod after the probe.

```sh
kubectl -n mcp-servers run probe --rm -i --image=busybox:1.36 --restart=Never -- sh -c '
  wget -T2 -qO- http://mcp-platform-api.mcp-platform.svc:8080/health || echo BLOCKED
'
```

Successful unauthorized cross-namespace traffic where the design says it
should be blocked is **High**.

## Step 5 — Manifest hygiene per Deployment / StatefulSet

For every workload manifest in `k8s/` and `config/`:

- `automountServiceAccountToken: false` unless the workload calls the k8s
  API.
- `securityContext` at pod and container level: `runAsNonRoot: true`,
  `runAsUser` ≥ 1000, `allowPrivilegeEscalation: false`,
  `readOnlyRootFilesystem: true`, `capabilities.drop: [ALL]`,
  `seccompProfile.type: RuntimeDefault`.
- Volumes mounted read-only when possible; no `hostPath` outside dev/test
  manifests (and when present in `k8s/`, the file name should make the
  dev intent obvious — `*-hostpath.yaml`).
- Resource `requests` and `limits` set; CPU limit may be omitted for
  latency-sensitive components, but flag the absence.
- `imagePullPolicy: IfNotPresent` for pinned digests; `Always` only for
  `latest` (which itself should be flagged).
- No `env` blocks containing literal secrets — all secret material via
  `valueFrom.secretKeyRef`.

`kubectl get deploy,sts -A -o json | jq` filters can quickly enumerate
violations across the live cluster:

```sh
kubectl get deploy,sts -A -o json | jq -r '
  .items[]
  | select(.spec.template.spec.containers[]
           | .securityContext.readOnlyRootFilesystem != true)
  | "\(.kind)/\(.metadata.namespace)/\(.metadata.name)"
'
```

Repeat with filters for `runAsNonRoot`, `allowPrivilegeEscalation`, and
`automountServiceAccountToken`.

## Step 6 — Secret access and storage

- Confirm every Secret in `mcp-platform` is mounted by exactly the
  workloads that need it; no broad `secrets get` on workload SAs.
- Confirm secrets are not committed to git: `gitleaks` (handled by
  `security-audit`) and `grep -RIn 'kind: Secret' k8s/ config/` —
  the only tracked Secrets should be `02-secrets.yaml.example` (no real
  values) and the bootstrap job that rotates `PLATFORM_ADMIN_PASSWORD`.
- Confirm the cluster has encryption-at-rest configured for `etcd`
  (cluster-level, often out of scope for MCP Runtime to enforce, but call
  out as a recommendation in the report).
- Confirm `ServiceAccount` token volumes use projected tokens with a
  bounded `expirationSeconds` (default in modern k8s, but verify).

## Step 7 — CIS benchmark (live cluster)

```sh
docker run --rm --pid=host -v /etc:/etc:ro -v /var:/var:ro \
  -t docker.io/aquasec/kube-bench:latest run --targets master,node,etcd,policies
```

Triage `[FAIL]` lines; many will be cluster-operator concerns (kubelet
flags, etcd config). Keep MCP Runtime-relevant ones in the report:

- 5.1.x — RBAC and ServiceAccount hardening.
- 5.2.x — Pod Security Standards.
- 5.3.x — NetworkPolicy and CNI.
- 5.7.x — General policies (default SA, default NetworkPolicy).

For each MCP-Runtime-relevant FAIL, propose the manifest change to fix it.

## Step 8 — Admission policies

If the cluster runs Kyverno, OPA Gatekeeper, or Sigstore policy controller:

```sh
kubectl get clusterpolicies.kyverno.io -A 2>/dev/null
kubectl get constraints -A 2>/dev/null
```

Confirm policies enforce the hygiene rules above so a malicious or
mis-configured manifest is rejected at admission, not just flagged in CI.
Absence of any admission-time enforcement is at least a Low finding for an
alpha project, Medium when targeting production.

## Step 9 — Report

Use `../_shared/FINDINGS-TEMPLATE.md`. In the Summary include:

- Namespaces audited and their PSS levels.
- ServiceAccount count and how many have `*` or cluster-scoped verbs.
- Deployments scanned and the pass rate per `kube-linter` rule.
- NetworkPolicy coverage (% of pods covered by a default-deny).
- Notable CIS FAILs that the repo can fix.

Cross-reference findings with `security-audit` and
`security-audit` to avoid double counting (e.g., a CVE in the operator
image is logged once in supply-chain, not twice).
