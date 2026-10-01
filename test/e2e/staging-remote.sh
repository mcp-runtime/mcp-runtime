#!/usr/bin/env bash
set -Eeuo pipefail

# Staging E2E driven against a REMOTE disposable cluster.
#
# This models the way an operator actually installs MCP Runtime: the CLI runs on
# a workstation or CI runner and talks to the cluster through a kubeconfig. The
# VM is used only to host k3s; every CLI invocation, API call, and assertion
# happens locally.
#
# Contrast with staging-vm.sh, which ships the repo to the VM and runs the CLI
# there. Running locally removes a whole class of environment problems: no
# repo tarball (so no missing .git), no login-shell working directory, and the
# runner's own Go toolchain and Docker instead of whatever the VM happens to
# have installed.
#
# Image pushes still work without any registry reachability from here: setup
# does `docker save` locally and runs a helper pod inside the cluster that
# pushes into the internal registry, so only the Kubernetes API must be
# reachable.
#
# SAFETY: this wipes k3s and Docker state on the target. It refuses to start
# unless test/e2e/lib/staging.sh proves the target is the disposable VM (E2E
# hostnames under the disposable suffix, no address shared with production,
# and the DISPOSABLE marker present on the VM).
#
# Required:
#   E2E_VM_HOST        public address of the disposable VM
#   E2E_ACME_EMAIL     ACME contact for Let's Encrypt
# Auth (one of):
#   SSH agent/key for E2E_VM_USER (default root), or SSHPASS with sshpass.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${BIN:-${ROOT_DIR}/bin/mcp-runtime}"
RUN_ID="${E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
ARTIFACT_DIR="${E2E_ARTIFACT_DIR:-${ROOT_DIR}/e2e-artifacts/${RUN_ID}}"
WORK_DIR="${E2E_WORK_DIR:-$(mktemp -d)}"

VM_HOST="${E2E_VM_HOST:?set E2E_VM_HOST to the disposable VM address}"
VM_USER="${E2E_VM_USER:-root}"
VM_BACKUP_DIR="${E2E_BACKUP_DIR:-/var/lib/mcp-runtime-e2e-backup}"
KUBECONFIG_FILE="${WORK_DIR}/kubeconfig"

PLATFORM_URL="${E2E_PLATFORM_URL:-https://platform.e2e.mcpruntime.org}"
MCP_URL="${E2E_MCP_URL:-https://mcp.e2e.mcpruntime.org}"
REGISTRY_HOST="${E2E_REGISTRY_HOST:-registry.e2e.mcpruntime.org}"
AUTH_URL="${E2E_AUTH_URL:-https://auth.e2e.mcpruntime.org}"

mkdir -p "${ARTIFACT_DIR}" "${WORK_DIR}"
chmod 700 "${WORK_DIR}"
cd "${ROOT_DIR}"

STAGING_LOG_PREFIX=staging-remote
STAGING_RUNNER_KIND=remote-cluster
export STAGING_RUNNER_KIND
# shellcheck source=test/e2e/lib/staging.sh
source "${ROOT_DIR}/test/e2e/lib/staging.sh"
# shellcheck source=test/e2e/lib/cluster-wait.sh
source "${ROOT_DIR}/test/e2e/lib/cluster-wait.sh"

log() { staging_log "$@"; }
fail() {
  staging_err "$*"
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

staging_ssh_init
vm_ssh() { staging_vm_ssh "$@"; }

E2E_HOSTS=(
  "$(staging_url_host "${PLATFORM_URL}")"
  "$(staging_url_host "${MCP_URL}")"
  "$(staging_url_host "${REGISTRY_HOST}")"
  "$(staging_url_host "${AUTH_URL}")"
)
# Set only after the guard passes; cleanup never touches an unverified target.
TARGET_VERIFIED=0

# staging-vm.sh sources the VM's e2e.env directly because it runs there.
# Do the equivalent from here, so a local run needs no more configuration than
# an on-VM one. Values already in the environment win, which keeps CI secrets
# authoritative over whatever the VM happens to remember.
load_vm_env() {
  local line key
  while IFS= read -r line; do
    case "${line}" in
      E2E_*=*) ;;
      *) continue ;;
    esac
    key="${line%%=*}"
    if [[ -z "${!key:-}" ]]; then
      export "${key}=${line#*=}"
    fi
  done < <(vm_ssh "cat '${VM_BACKUP_DIR}/e2e.env' 2>/dev/null" 2>/dev/null || true)
}

persist_vm_env() {
  local key="$1" value="$2"
  vm_ssh "set -eu
    install -d -m 700 '${VM_BACKUP_DIR}'
    touch '${VM_BACKUP_DIR}/e2e.env'
    chmod 600 '${VM_BACKUP_DIR}/e2e.env'
    printf '%s=%s\n' '${key}' '${value}' >>'${VM_BACKUP_DIR}/e2e.env'"
}

# Certificates are the only snapshot material the E2E needs: losing them means
# re-issuing from Let's Encrypt on every run, and its rate limits make repeat
# runs fail. Credentials are deliberately excluded. Setup generates fresh ones
# and runs syncPostgresPasswordClientGo against the new database, so re-applying
# an older mcp-sentinel-secrets afterwards would leave the API pods holding a
# password the database no longer accepts.
E2E_TLS_SNAPSHOT_FILES=(
  letsencrypt-prod-clusterissuer.yaml
  letsencrypt-staging-clusterissuer.yaml
  registry-tls.yaml
  registry-cert.yaml
  mcp-sentinel-platform-tls.yaml
  mcp-sentinel-platform-cert.yaml
)
LOCAL_SNAPSHOT_DIR="${WORK_DIR}/platform-runtime"
VM_SNAPSHOT_DIR="${VM_BACKUP_DIR}/platform-runtime/tls"

snapshot_tls_state() {
  kubectl get nodes >/dev/null 2>&1 || staging_skip "the Kubernetes API is unavailable"
  mkdir -p "${LOCAL_SNAPSHOT_DIR}"
  local captured=0
  _grab() {
    local file="$1"
    shift
    if kubectl "$@" -o yaml >"${LOCAL_SNAPSHOT_DIR}/${file}" 2>/dev/null &&
      [[ -s "${LOCAL_SNAPSHOT_DIR}/${file}" ]]; then
      captured=$((captured + 1))
      return 0
    fi
    rm -f "${LOCAL_SNAPSHOT_DIR}/${file}"
  }
  _grab letsencrypt-prod-clusterissuer.yaml get clusterissuer letsencrypt-prod
  _grab letsencrypt-staging-clusterissuer.yaml get clusterissuer letsencrypt-staging
  _grab registry-tls.yaml get secret registry-tls -n registry
  _grab registry-cert.yaml get certificate registry-cert -n registry
  _grab mcp-sentinel-platform-tls.yaml get secret mcp-sentinel-platform-tls -n mcp-sentinel
  _grab mcp-sentinel-platform-cert.yaml get certificate mcp-sentinel-platform-tls -n mcp-sentinel

  # A failed run can reach this with nothing issued yet. Publishing that would
  # replace a usable snapshot with an empty one, so only ship a capture that
  # actually holds the public certificates.
  if [[ ! -s "${LOCAL_SNAPSHOT_DIR}/registry-tls.yaml" ||
    ! -s "${LOCAL_SNAPSHOT_DIR}/mcp-sentinel-platform-tls.yaml" ]]; then
    staging_skip "TLS snapshot incomplete (${captured} object(s): registry-tls and mcp-sentinel-platform-tls are both required); keeping the previous snapshot"
  fi
  vm_ssh "install -d -m 700 '${VM_SNAPSHOT_DIR}.new'" >/dev/null
  tar -C "${LOCAL_SNAPSHOT_DIR}" -czf - . |
    vm_ssh "tar -C '${VM_SNAPSHOT_DIR}.new' -xzf - && rm -rf '${VM_SNAPSHOT_DIR}' && mv '${VM_SNAPSHOT_DIR}.new' '${VM_SNAPSHOT_DIR}'"
  log "stored TLS snapshot (${captured} objects) on the VM"
}

restore_tls_state() {
  mkdir -p "${LOCAL_SNAPSHOT_DIR}"
  if staging_flag_enabled "${E2E_SKIP_TLS_RESTORE:-0}"; then
    staging_skip "E2E_SKIP_TLS_RESTORE is set; certificates will be issued fresh"
  fi
  if ! vm_ssh "test -d '${VM_SNAPSHOT_DIR}'" >/dev/null 2>&1; then
    staging_skip "no TLS snapshot on the VM; certificates will be issued fresh"
  fi
  vm_ssh "tar -C '${VM_SNAPSHOT_DIR}' -czf - ." | tar -C "${LOCAL_SNAPSHOT_DIR}" -xzf -
  # This runs before setup on purpose. cert-manager only skips issuance when a
  # valid secret is already present when it reconciles the Certificate, so
  # restoring afterwards would overwrite a cert ACME had just issued rather than
  # avoiding the request. The secrets need namespaces, which setup has not
  # created yet.
  local namespace
  for namespace in registry mcp-sentinel; do
    kubectl create namespace "${namespace}" --dry-run=client -o yaml 2>/dev/null | kubectl apply -f - >/dev/null
  done

  load_platform_backup_helpers
  local file restored=0
  for file in "${E2E_TLS_SNAPSHOT_FILES[@]}"; do
    # ClusterIssuers and Certificates need the cert-manager CRDs, which setup
    # installs; only the Secrets can be restored ahead of it.
    case "${file}" in
      *-tls.yaml) ;;
      *) continue ;;
    esac
    [[ -s "${LOCAL_SNAPSHOT_DIR}/${file}" ]] || continue
    mcpruntime_org_backup_strip_and_apply "${LOCAL_SNAPSHOT_DIR}/${file}" "${file%.yaml}"
    restored=$((restored + 1))
  done
  log "restored ${restored} issued certificate secret(s); cert-manager will reuse them instead of asking ACME"
  local pem
  for file in registry-tls mcp-sentinel-platform-tls; do
    [[ -s "${LOCAL_SNAPSHOT_DIR}/${file}.yaml" ]] || continue
    pem="${WORK_DIR}/snapshot-${file}.pem"
    awk '$1 == "tls.crt:" { print $2 }' "${LOCAL_SNAPSHOT_DIR}/${file}.yaml" | base64 --decode >"${pem}" 2>/dev/null || true
    openssl x509 -in "${pem}" -noout -subject -issuer -enddate 2>/dev/null | sed "s/^/[snapshot ${file}] /" || true
  done
}

PLATFORM_BACKUP_HELPERS_LOADED=0
load_platform_backup_helpers() {
  [[ "${PLATFORM_BACKUP_HELPERS_LOADED}" == "1" ]] && return 0
  # backup.sh only defines functions; it never loads the deployment dotenv.
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/hack/deploy/mcpruntime-org/lib/backup.sh"
  PLATFORM_BACKUP_HELPERS_LOADED=1
}

# Let's Encrypt allows five certificates per exact set of identifiers per week,
# so a production-CA run can only succeed five times before every further run
# dies at Step 3 with a 429 and a retry-after roughly a day out. Staging has far
# higher limits and exercises the identical ACME order, HTTP-01 challenge and
# cert-manager path; only the signing CA differs. Its roots are not publicly
# trusted, so teach this process to trust them before anything calls the hosts,
# rather than weakening the checks with curl -k.
trust_acme_staging_roots() {
  local bundle="${WORK_DIR}/acme-staging-ca.pem" url system
  : >"${bundle}"
  for url in https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem \
    https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x2.pem; do
    curl -fsSL "${url}" >>"${bundle}" || fail "could not fetch ${url}; staging certificates will not verify"
  done
  # The bundle replaces the default trust store rather than adding to it, so it
  # must carry the platform roots as well or every other HTTPS call this run
  # makes would stop verifying.
  local found=""
  for system in /etc/ssl/certs/ca-certificates.crt /etc/pki/tls/certs/ca-bundle.crt /etc/ssl/cert.pem; do
    if [[ -r "${system}" ]]; then
      cat "${system}" >>"${bundle}"
      found="${system}"
      break
    fi
  done
  [[ -n "${found}" ]] || fail "no system CA bundle found to extend with the staging roots"
  staging_state_set CURL_CA_BUNDLE "${bundle}"
  staging_state_set SSL_CERT_FILE "${bundle}"
  staging_state_set STAGING_CA_BUNDLE "${bundle}"
  log "trusting Let's Encrypt staging roots for this run"
}

cleanup() {
  local status=$?
  set +e
  if [[ "${TARGET_VERIFIED}" == "1" && -f "${KUBECONFIG_FILE}" ]]; then
    log "collecting diagnostics under ${ARTIFACT_DIR}/diagnostics"
    STAGING_ABORTED=0 staging_run_stage collect-diagnostics soft "diagnostics collection failed; the cluster API may be down" \
      staging_collect_diagnostics "${ARTIFACT_DIR}/diagnostics"
    vm_ssh "df -h /; free -m" >"${ARTIFACT_DIR}/diagnostics/vm-resources.txt" 2>&1
  fi
  if [[ "${TARGET_VERIFIED}" == "1" ]] && staging_flag_enabled "${E2E_CLEANUP:-1}"; then
    STAGING_ABORTED=0
    staging_run_stage snapshot soft "could not store the TLS snapshot on the VM; the next run may issue fresh certificates" snapshot_tls_state
    staging_run_stage teardown soft "the VM teardown was not confirmed; the next run may start on a dirty VM" teardown_vm
  elif [[ "${TARGET_VERIFIED}" != "1" ]]; then
    log "target not verified as disposable; leaving the VM untouched"
  fi
  local summary_rc=0
  staging_finish "${RUN_ID}" || summary_rc=1
  rm -rf "${WORK_DIR}"
  if [[ ${status} -eq 0 && ${summary_rc} -ne 0 ]]; then
    status=1
  fi
  log "run ${RUN_ID} finished with status ${status}; artifacts in ${ARTIFACT_DIR}"
  exit "${status}"
}

teardown_vm() {
  log "wiping disposable state on the VM; preserving only ${VM_BACKUP_DIR}"
  # Reclaim everything that does not require tearing down the network first,
  # because uninstalling k3s drops CNI and resets this very SSH session --
  # anything sequenced after it is simply lost.
  vm_ssh "set -u
    rm -rf /opt/mcp-runtime-e2e /var/tmp/mcp-runtime-e2e-* /tmp/mcp-runtime-e2e.tgz /tmp/mcp-img-*.tar
    if command -v docker >/dev/null 2>&1; then docker system prune -af --volumes || true; fi" \
    >"${ARTIFACT_DIR}/vm-cleanup.log" 2>&1 || true

  # Detach the k3s teardown so it survives the connection it kills, then
  # reconnect to confirm rather than trusting a command whose output cannot
  # come back.
  vm_ssh "setsid nohup sh -c '
    if [ -x /usr/local/bin/k3s-uninstall.sh ]; then /usr/local/bin/k3s-uninstall.sh; fi
    rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /var/lib/cni /etc/cni /run/k3s /run/flannel
  ' >/tmp/k3s-uninstall.log 2>&1 </dev/null &" >>"${ARTIFACT_DIR}/vm-cleanup.log" 2>&1 || true

  local waited=0
  while ((waited < 180)); do
    if vm_ssh "test ! -d /etc/rancher && test ! -d /var/lib/rancher" >/dev/null 2>&1; then
      log "VM teardown confirmed"
      vm_ssh "df -h / | awk 'NR==2 {print \"free after cleanup: \" \$4 \" (\" \$5 \" used)\"}'" 2>/dev/null || true
      return 0
    fi
    sleep 10
    waited=$((waited + 10))
  done
  fail "VM teardown not confirmed within ${waited}s"
}

# --- stage bodies ----------------------------------------------------------

stage_target_guard() {
  require_command ssh
  if [[ -n "${SSHPASS:-}" ]]; then
    require_command sshpass
  fi
  staging_verify_remote_target "${E2E_HOSTS[@]}"
}

stage_prerequisites() {
  local cmd
  for cmd in kubectl docker curl jq openssl; do
    require_command "${cmd}"
  done
  if [[ ! -x "${BIN}" ]]; then
    require_command go
    log "building the CLI"
    go build -o "${BIN}" ./cmd/mcp-runtime
  fi
  "${BIN}" --help >/dev/null
}

# kubelet verifies the registry certificate against the node's own trust store,
# not the runner's. With the staging CA the registry certificate is signed by a
# root no node trusts, so every image pull fails with "x509: certificate signed
# by unknown authority" and the operator never becomes ready. Install the roots
# before k3s exists, so containerd has them from its first start -- Go caches the
# system pool per process, so adding them afterwards needs a restart.
stage_staging_roots() {
  if ! staging_acme_staging; then
    staging_skip "E2E_ACME_STAGING=0: production CA roots are already trusted"
  fi
  trust_acme_staging_roots
  vm_ssh "set -eu
    install -d -m 755 /usr/local/share/ca-certificates
    curl -fsSL https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem \
      -o /usr/local/share/ca-certificates/le-staging-x1.crt
    curl -fsSL https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x2.pem \
      -o /usr/local/share/ca-certificates/le-staging-x2.crt
    update-ca-certificates >/dev/null 2>&1
    grep -c 'BEGIN CERTIFICATE' /etc/ssl/certs/ca-certificates.crt
    if systemctl is-active --quiet k3s; then systemctl restart k3s; fi"
  log "installed Let's Encrypt staging roots on the VM trust store"
}

stage_k3s() {
  # k3s already adds the node's public IP to the API server certificate SANs, so
  # the kubeconfig only needs its loopback server URL rewritten to reach it.
  vm_ssh "set -eu
    # Builds from earlier on-VM runs leave images behind, and the cluster's
    # ephemeral storage shares this disk. k3s uses containerd, so pruning Docker
    # never touches running workloads.
    command -v docker >/dev/null 2>&1 && docker system prune -af >/dev/null 2>&1 || true
    if [ ! -f /etc/rancher/k3s/k3s.yaml ]; then
      curl -sfL https://get.k3s.io | sh -s - --write-kubeconfig-mode 644 --tls-san '${VM_HOST}'
    fi
    for _ in \$(seq 1 60); do
      [ -f /etc/rancher/k3s/k3s.yaml ] && break
      sleep 2
    done
    install -d -m 700 '${VM_BACKUP_DIR}'"

  log "fetching kubeconfig and pointing it at the VM"
  vm_ssh 'cat /etc/rancher/k3s/k3s.yaml' >"${KUBECONFIG_FILE}" 2>/dev/null ||
    fail "could not read the k3s kubeconfig from the VM"
  [[ -s "${KUBECONFIG_FILE}" ]] || fail "the kubeconfig fetched from the VM is empty"
  sed -i.bak "s#server: https://127.0.0.1:6443#server: https://${VM_HOST}:6443#" "${KUBECONFIG_FILE}"
  rm -f "${KUBECONFIG_FILE}.bak"
  chmod 600 "${KUBECONFIG_FILE}"

  require_node_disk "${E2E_MIN_DISK_GIB:-6}"
  wait_for_node_registration 180
  kubectl wait --for=condition=Ready nodes --all --timeout=180s
  kubectl get nodes -o wide
  wait_for_traefik 300
}

# The kubelet evicts pods once ephemeral storage runs low, which setup reports
# only as "deployment timeout" ten minutes later. Say so up front instead.
require_node_disk() {
  local min_gib="${1:-6}" avail
  avail="$(vm_ssh "df -BG --output=avail / 2>/dev/null | tail -1 | tr -dc '0-9'" 2>/dev/null || true)"
  if [[ -z "${avail}" ]]; then
    log "WARNING: could not determine free disk on the VM"
    return 0
  fi
  if ((avail < min_gib)); then
    fail "only ${avail}GiB free on the VM; setup needs about ${min_gib}GiB and the kubelet evicts pods under ephemeral-storage pressure"
    return 1
  fi
  log "${avail}GiB free on the VM"
}

# Advisory: a cluster that has not been set up yet cannot satisfy checks that
# cover components setup installs. Post-setup diagnostics is the gate.
stage_doctor_before() {
  if ! "${BIN}" cluster doctor 2>&1 | tee "${ARTIFACT_DIR}/doctor-before.log"; then
    log "pre-setup doctor reported unmet requirements; continuing because setup provisions them"
  fi
}

# The admin credential lives on the VM in the preserved backup directory so
# repeat runs reuse the same account even though this runner is ephemeral.
ensure_platform_admin_config() {
  local email password
  email="${E2E_PLATFORM_ADMIN_EMAIL:-${E2E_ACME_EMAIL}}"
  password="${E2E_PLATFORM_ADMIN_PASSWORD:-}"

  if [[ -z "${password}" ]]; then
    local raw
    raw="$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')"
    password="${raw:0:32}"
    [[ ${#password} -eq 32 ]] || fail "failed to generate a platform admin password"
    persist_vm_env E2E_PLATFORM_ADMIN_PASSWORD "${password}"
    log "generated a platform admin password and stored it on the VM"
  fi

  export MCP_PLATFORM_ADMIN_EMAIL="${email}"
  export MCP_PLATFORM_ADMIN_PASSWORD="${password}"
  log "platform admin configured for ${email}"
}

stage_setup() {
  local args=(
    setup
    --strict-prod
    --platform-mode "${E2E_PLATFORM_MODE:-tenant}"
    --ingress traefik
    --with-tls
    --acme-email "${E2E_ACME_EMAIL}"
    --registry-mode "${E2E_REGISTRY_MODE:-bundled-https}"
    --kubeconfig "${KUBECONFIG_FILE}"
  )
  # Default to the staging CA so repeat runs are not capped at five a week. Set
  # E2E_ACME_STAGING=0 for an occasional run against the production CA.
  if staging_acme_staging; then
    args+=(--acme-staging)
  fi
  if [[ -n "${E2E_MTLS_CLUSTER_ISSUER:-}" ]]; then
    args+=(--mtls-cluster-issuer "${E2E_MTLS_CLUSTER_ISSUER}")
  fi
  if staging_flag_enabled "${E2E_WITH_MCP_AUTH:-0}"; then
    args+=(--with-mcp-auth-server)
    [[ -n "${E2E_MCP_AUTH_CONNECTORS_FILE:-}" ]] && args+=(--mcp-auth-connectors-file "${E2E_MCP_AUTH_CONNECTORS_FILE}")
    [[ -n "${E2E_MCP_AUTH_CONNECTOR:-}" ]] && args+=(--mcp-auth-connector "${E2E_MCP_AUTH_CONNECTOR}")
    [[ -n "${E2E_MCP_AUTH_ISSUER_URL:-}" ]] && args+=(--mcp-auth-issuer-url "${E2E_MCP_AUTH_ISSUER_URL}")
    [[ -n "${E2E_MCP_AUTH_TLS_SECRET:-}" ]] && args+=(--mcp-auth-tls-secret "${E2E_MCP_AUTH_TLS_SECRET}")
    [[ -n "${E2E_MCP_AUTH_SIGNING_KEY_SECRET:-}" ]] && args+=(--mcp-auth-signing-key-secret "${E2E_MCP_AUTH_SIGNING_KEY_SECRET}")
  fi
  log "running production-style setup: ${args[*]}"
  "${BIN}" "${args[@]}" 2>&1 | tee "${ARTIFACT_DIR}/setup.log"
}

trap cleanup EXIT

staging_stages_init "${ARTIFACT_DIR}" "${WORK_DIR}/state.env"
STAGING_GIT_SHA="$(git -C "${ROOT_DIR}" rev-parse HEAD 2>/dev/null || echo unknown)"
export STAGING_GIT_SHA

staging_run_stage target-guard critical "the target is not provably the disposable VM (hostname suffix, production address overlap, or missing DISPOSABLE marker); do NOT override against production" stage_target_guard
if [[ "$(staging_stage_status target-guard)" == "passed" ]]; then
  TARGET_VERIFIED=1
fi
[[ "${TARGET_VERIFIED}" == "1" ]] || exit 1

# Fill any gaps from the VM's preserved env file, then require what setup needs.
load_vm_env
: "${E2E_ACME_EMAIL:?set E2E_ACME_EMAIL, or record it in ${VM_BACKUP_DIR}/e2e.env on the VM}"
export E2E_MTLS_CLUSTER_ISSUER="${E2E_MTLS_CLUSTER_ISSUER-mcp-runtime-ca}"
# Adapter certificates on gateway routes are an opt-in platform feature; setup
# reads MCP_ADAPTER_CERTIFICATES, MCP_TRUST_DOMAIN and
# MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE from the environment.
staging_configure_adapter_certificates
log "options: acme-staging=${E2E_ACME_STAGING:-1} fresh-certificate=${E2E_FRESH_CERTIFICATE:-0} multitenancy=${E2E_RUN_MULTITENANCY:-1} mcp-auth=${E2E_WITH_MCP_AUTH:-0} mtls-issuer=${E2E_MTLS_CLUSTER_ISSUER:-<none>} adapter-certificates=${MCP_ADAPTER_CERTIFICATES:-false} trust-domain=${MCP_TRUST_DOMAIN:-<none>}"

export KUBECONFIG="${KUBECONFIG_FILE}"
export MCP_PLATFORM_API_URL="${PLATFORM_URL}"
export MCP_PLATFORM_INGRESS_HOST="${E2E_HOSTS[0]}"
export MCP_MCP_INGRESS_HOST="${E2E_HOSTS[1]}"
export MCP_REGISTRY_INGRESS_HOST="${E2E_HOSTS[2]}"
export MCP_AUTH_INGRESS_HOST="${E2E_HOSTS[3]}"
export E2E_ARTIFACT_DIR="${ARTIFACT_DIR}"
export MCPRUNTIME_ORG_ROOT="${ROOT_DIR}"
# Reuse the same content-hash GHCR images as QA E2E. Staging runners pull
# existing tags and build cache misses locally without GHCR write access.
export E2E_IMAGE_CACHE="${E2E_IMAGE_CACHE:-1}"
export E2E_GHCR_PUSH="${E2E_GHCR_PUSH:-0}"
export E2E_IMAGE_CACHE_REGISTRY="${E2E_IMAGE_CACHE_REGISTRY:-ghcr.io/mcp-runtime/mcp-runtime}"
export BIN PLATFORM_URL MCP_URL REGISTRY_HOST AUTH_URL WORK_DIR RUN_ID ROOT_DIR

# kubelet resolves names through the node's resolver, not CoreDNS, so it cannot
# reach the default in-cluster pull host registry.registry.svc.cluster.local and
# every platform pod lands in ImagePullBackOff with "lookup ...: Try again".
# For a bundled-HTTPS public install the supported endpoint is the public
# registry hostname: the node resolves it through public DNS, its certificate is
# trusted, and setup provisions the matching pull secret and attaches it to the
# operator. The in-cluster skopeo helper rewrites this back to Service DNS when
# pushing, because the registry stores images by repository path and is
# reachable under either name.
export MCP_REGISTRY_ENDPOINT="${E2E_REGISTRY_ENDPOINT:-${REGISTRY_HOST}}"

staging_run_stage prerequisites critical "a required local tool is missing or the CLI failed to build" stage_prerequisites
staging_run_stage staging-roots critical "could not fetch/install the Let's Encrypt staging roots (runner or VM); image pulls would fail x509 verification" stage_staging_roots
staging_run_stage k3s critical "k3s did not install/become Ready, the kubeconfig is unreachable, the disk is full, or Traefik never became ready" stage_k3s
staging_run_stage doctor-before soft "pre-setup doctor could not run at all" stage_doctor_before
ensure_platform_admin_config
staging_run_stage restore-snapshot soft "the TLS snapshot could not be fetched or applied; setup will issue fresh certificates" restore_tls_state
staging_run_stage setup critical "setup failed; read the last ERROR in setup.log (image pull x509/auth, ACME, webhook, or rollout timeout) and diagnostics/events.txt" stage_setup
staging_run_platform_stages
