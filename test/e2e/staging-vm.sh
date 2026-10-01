#!/usr/bin/env bash
set -Eeuo pipefail

# Staging E2E runner that executes ON the disposable VM.
#
# SAFETY: this installs and uninstalls k3s, prunes every Docker image and wipes
# kubelet/CNI state on the machine it runs on. It refuses to start unless
# test/e2e/lib/staging.sh proves this machine is the disposable VM: the E2E
# hostnames end in the disposable suffix, none of them (and none of this
# machine's addresses) is shared with the production hostnames, and the
# DISPOSABLE marker written by `test/e2e/staging-target.sh bootstrap` exists.
# The preserved backup directory is outside every cleanup path.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BIN="${BIN:-${ROOT_DIR}/bin/mcp-runtime}"
BACKUP_DIR="${E2E_BACKUP_DIR:-/var/lib/mcp-runtime-e2e-backup}"
export E2E_BACKUP_DIR="${BACKUP_DIR}"
RUN_ID="${E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
WORK_DIR="${E2E_WORK_DIR:-/var/tmp/mcp-runtime-e2e-${RUN_ID}}"
ARTIFACT_DIR="${BACKUP_DIR}/runs/${RUN_ID}"
KUBECONFIG_FILE="${E2E_KUBECONFIG:-/etc/rancher/k3s/k3s.yaml}"
PLATFORM_URL="${E2E_PLATFORM_URL:-https://platform.e2e.mcpruntime.org}"
MCP_URL="${E2E_MCP_URL:-https://mcp.e2e.mcpruntime.org}"
REGISTRY_HOST="${E2E_REGISTRY_HOST:-registry.e2e.mcpruntime.org}"
AUTH_URL="${E2E_AUTH_URL:-https://auth.e2e.mcpruntime.org}"

STAGING_LOG_PREFIX=staging-vm
STAGING_RUNNER_KIND=disposable-vm
export STAGING_RUNNER_KIND
# shellcheck source=test/e2e/lib/staging.sh
source "${ROOT_DIR}/test/e2e/lib/staging.sh"
# shellcheck source=test/e2e/lib/cluster-wait.sh
source "${ROOT_DIR}/test/e2e/lib/cluster-wait.sh"
# shellcheck source=test/e2e/lib/k3s-installer.sh
source "${ROOT_DIR}/test/e2e/lib/k3s-installer.sh"

log() { staging_log "$@"; }
fail() {
  staging_err "$*"
  return 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "missing required command: $1"
}

E2E_HOSTS=(
  "$(staging_url_host "${PLATFORM_URL}")"
  "$(staging_url_host "${MCP_URL}")"
  "$(staging_url_host "${REGISTRY_HOST}")"
  "$(staging_url_host "${AUTH_URL}")"
)

# The guard runs before anything is created, so a refused run leaves no trace
# beyond its console output.
verify_local_target() {
  staging_guard_hosts "" "${E2E_HOSTS[@]}" || return 1
  # shellcheck disable=SC2046 # word-split the address list on purpose
  staging_guard_local_ips $(hostname -I 2>/dev/null || true) || return 1
  staging_marker_valid "$(staging_marker_path)" "$(hostname)" || return 1
  staging_log "target guard: disposable marker verified on $(hostname)"
}
if ! verify_local_target; then
  staging_err "refusing to run staging E2E on this machine; nothing was changed"
  exit 1
fi

mkdir -p "${ARTIFACT_DIR}" "${WORK_DIR}"
chmod 700 "${BACKUP_DIR}" "${ARTIFACT_DIR}" "${WORK_DIR}"

# Keep the ten most recent run directories; each holds full diagnostics.
if [[ -d "${BACKUP_DIR}/runs" ]]; then
  find "${BACKUP_DIR}/runs" -mindepth 1 -maxdepth 1 -type d -printf '%T@ %p\n' 2>/dev/null |
    sort -rn | awk 'NR > 10 { print $2 }' | xargs -r rm -rf
fi

# The CLI resolves manifests as repo-relative paths (CRDs, ingress overlays,
# registry overlays), so setup must run from the repo root. The workflow starts
# this script over SSH, where the working directory is the login home.
cd "${ROOT_DIR}"

if [[ -f "${BACKUP_DIR}/e2e.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "${BACKUP_DIR}/e2e.env"
  set +a
fi
export E2E_MTLS_CLUSTER_ISSUER="${E2E_MTLS_CLUSTER_ISSUER-mcp-runtime-ca}"
# Adapter certificates on gateway routes are an opt-in platform feature; setup
# reads MCP_ADAPTER_CERTIFICATES, MCP_TRUST_DOMAIN and
# MCP_DEFAULT_INGRESS_TLS_SECRET_NAMESPACE from the environment.
staging_configure_adapter_certificates

export KUBECONFIG="${KUBECONFIG_FILE}"
export MCP_PLATFORM_API_URL="${PLATFORM_URL}"
export MCP_PLATFORM_INGRESS_HOST="${E2E_HOSTS[0]}"
export MCP_MCP_INGRESS_HOST="${E2E_HOSTS[1]}"
export MCP_REGISTRY_INGRESS_HOST="${E2E_HOSTS[2]}"
export MCP_AUTH_INGRESS_HOST="${E2E_HOSTS[3]}"
export E2E_ARTIFACT_DIR="${ARTIFACT_DIR}"
export MCPRUNTIME_ORG_ROOT="${ROOT_DIR}"
export MCP_TLS_BACKUP_DIR="${BACKUP_DIR}/platform-runtime"
# Reuse QA E2E's content-hash images from GHCR. A miss builds locally; this VM
# has no GHCR write credential, so publishing remains the QA runner's job.
export E2E_IMAGE_CACHE="${E2E_IMAGE_CACHE:-1}"
export E2E_GHCR_PUSH="${E2E_GHCR_PUSH:-0}"
export E2E_IMAGE_CACHE_REGISTRY="${E2E_IMAGE_CACHE_REGISTRY:-ghcr.io/mcp-runtime/mcp-runtime}"
export BIN PLATFORM_URL MCP_URL REGISTRY_HOST AUTH_URL WORK_DIR RUN_ID ROOT_DIR

# kubelet resolves names through the node's resolver, not CoreDNS, so it cannot
# reach the default in-cluster pull host registry.registry.svc.cluster.local.
# For a bundled-HTTPS public install the supported endpoint is the public
# registry hostname; setup provisions the matching pull secret. The in-cluster
# skopeo helper rewrites this back to Service DNS when pushing.
export MCP_REGISTRY_ENDPOINT="${E2E_REGISTRY_ENDPOINT:-${REGISTRY_HOST}}"

PLATFORM_BACKUP_HELPERS_LOADED=0
load_platform_backup_helpers() {
  if [[ "${PLATFORM_BACKUP_HELPERS_LOADED}" == "1" ]]; then
    return 0
  fi
  # Reuse the deployment backup implementation so the VM-side snapshot covers
  # TLS, platform credentials, auth-server secrets, and OIDC configuration.
  # Do not load the deployment dotenv here: the E2E runner owns its environment.
  # shellcheck disable=SC1091
  source "${ROOT_DIR}/hack/deploy/mcpruntime-org/lib/backup.sh"
  PLATFORM_BACKUP_HELPERS_LOADED=1
}

backup_platform_runtime() {
  [[ -f "${KUBECONFIG}" ]] || staging_skip "no kubeconfig; k3s never came up"
  kubectl get nodes >/dev/null 2>&1 || staging_skip "the Kubernetes API is unavailable"
  # The snapshot helper repoints `latest` before capturing anything while
  # treating missing objects as successful skips. Without this guard a run that
  # died before issuance silently replaces the last usable snapshot with an
  # empty one.
  if ! kubectl -n registry get secret registry-tls >/dev/null 2>&1 ||
    ! kubectl -n mcp-sentinel get secret mcp-sentinel-platform-tls >/dev/null 2>&1; then
    staging_skip "public certificates are not issued yet; keeping the previous snapshot"
  fi
  load_platform_backup_helpers
  log "capturing platform runtime backup before cluster cleanup"
  mcpruntime_org_backup_platform_runtime
}

install_dependencies() {
  if command -v apt-get >/dev/null 2>&1; then
    apt-get update
    apt-get install -y ca-certificates curl git jq make openssh-client openssl
    # Setup builds the platform images with Docker (driven by make) on the
    # machine running the CLI, which for this runner is the VM itself.
    if ! command -v docker >/dev/null 2>&1; then
      apt-get install -y docker.io
    fi
    if ! docker buildx version >/dev/null 2>&1; then
      apt-get install -y docker-buildx || apt-get install -y docker-buildx-plugin || true
    fi
  elif command -v dnf >/dev/null 2>&1; then
    dnf install -y ca-certificates curl git jq make openssh-clients openssl
  elif command -v apk >/dev/null 2>&1; then
    apk add --no-cache ca-certificates curl git jq make openssh-client openssl
  else
    log "no supported package manager found; assuming curl, git, jq, make, openssl and docker are preinstalled"
  fi
  if command -v systemctl >/dev/null 2>&1 && ! docker info >/dev/null 2>&1; then
    systemctl enable --now docker || true
  fi
  local cmd
  for cmd in curl git jq make openssl docker; do
    require_command "${cmd}"
  done
  docker info >/dev/null 2>&1 || fail "the Docker daemon is not running"
  docker version --format 'docker {{.Server.Version}}'
  docker buildx version || log "docker buildx is unavailable; setup falls back to docker build"
}

# Rank a "goX.Y[.Z]" string as X*1000+Y so toolchains can be compared.
go_version_rank() {
  local raw="${1#go}" major minor
  major="${raw%%.*}"
  raw="${raw#*.}"
  minor="${raw%%.*}"
  [[ "${major}" =~ ^[0-9]+$ ]] || return 1
  [[ "${minor}" =~ ^[0-9]+$ ]] || return 1
  printf '%d' "$((major * 1000 + minor))"
}

go_directive_version() {
  awk '/^go [0-9]/ { print $2; exit }' "${ROOT_DIR}/go.mod"
}

install_go_toolchain() {
  local want arch url
  want="$(go_directive_version)"
  [[ -n "${want}" ]] || { fail "could not read the go directive from go.mod"; return 1; }
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *) fail "unsupported architecture $(uname -m) for Go installation"; return 1 ;;
  esac
  url="https://go.dev/dl/go${want}.linux-${arch}.tar.gz"
  log "installing Go ${want} for ${arch}"
  curl -fsSL "${url}" -o "${WORK_DIR}/go.tar.gz" || { fail "failed to download ${url}"; return 1; }
  # Unpack beside the existing toolchain and swap only once the new tree is
  # complete, so a partial download never leaves the VM without a working Go.
  rm -rf /usr/local/go.new /usr/local/go.prev
  mkdir -p /usr/local/go.new
  if ! tar -C /usr/local/go.new --strip-components=1 -xzf "${WORK_DIR}/go.tar.gz"; then
    rm -rf /usr/local/go.new
    fail "failed to unpack Go ${want}"
    return 1
  fi
  if [[ -d /usr/local/go ]]; then
    mv /usr/local/go /usr/local/go.prev
  fi
  mv /usr/local/go.new /usr/local/go
  rm -rf /usr/local/go.prev
  rm -f "${WORK_DIR}/go.tar.gz"
}

# Ubuntu ships an old distro Go on PATH while a current toolchain often sits
# unused under /usr/local/go. go.mod pins a far newer release, and Go only
# learned to fetch toolchains on demand in 1.21, so the distro binary rejects
# the go directive outright. Put the newest usable toolchain first on PATH,
# installing one if needed, then build the CLI.
stage_go_toolchain() {
  local candidate version rank best="" best_rank=0 best_version=""
  for candidate in /usr/local/go/bin/go /usr/lib/go-*/bin/go "$(command -v go 2>/dev/null || true)"; do
    [[ -n "${candidate}" && -x "${candidate}" ]] || continue
    version="$(GOTOOLCHAIN=local "${candidate}" env GOVERSION 2>/dev/null || true)"
    [[ -n "${version}" ]] || continue
    rank="$(go_version_rank "${version}" 2>/dev/null || true)"
    [[ -n "${rank}" ]] || continue
    if ((rank > best_rank)); then
      best_rank="${rank}"
      best="${candidate}"
      best_version="${version}"
    fi
  done

  if ((best_rank < 1021)); then
    if ((best_rank == 0)); then
      log "no Go toolchain found on the VM"
    else
      log "Go ${best_version} cannot honor the go directive in go.mod"
    fi
    install_go_toolchain
    best="/usr/local/go/bin/go"
    best_version="$(GOTOOLCHAIN=local "${best}" env GOVERSION 2>/dev/null || echo unknown)"
  fi

  PATH="$(dirname "${best}"):${PATH}"
  export PATH
  staging_state_set PATH "${PATH}"
  log "using Go toolchain ${best_version} from $(dirname "${best}")"
  if [[ ! -x "${BIN}" ]]; then
    go build -o "${BIN}" ./cmd/mcp-runtime
  fi
  "${BIN}" --help >/dev/null
}

# kubelet verifies the registry certificate against this node's trust store, and
# with the staging CA that certificate is signed by a root nothing trusts, so
# image pulls fail with "x509: certificate signed by unknown authority". Install
# the roots before k3s exists so containerd starts with them.
stage_staging_roots() {
  if ! staging_acme_staging; then
    staging_skip "E2E_ACME_STAGING=0: production CA roots are already trusted"
  fi
  install -d -m 755 /usr/local/share/ca-certificates
  curl -fsSL https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x1.pem \
    -o /usr/local/share/ca-certificates/le-staging-x1.crt
  curl -fsSL https://letsencrypt.org/certs/staging/letsencrypt-stg-root-x2.pem \
    -o /usr/local/share/ca-certificates/le-staging-x2.crt
  update-ca-certificates
  if systemctl is-active --quiet k3s 2>/dev/null; then systemctl restart k3s; fi
  staging_state_set STAGING_CA_BUNDLE /etc/ssl/certs/ca-certificates.crt
}

stage_k3s() {
  if [[ ! -f "${KUBECONFIG}" ]]; then
    log "installing k3s on the disposable VM"
    local installer="${WORK_DIR}/k3s-install.sh"
    staging_download_k3s_installer "${installer}" || fail "could not download the k3s installer from either official source"
    sh "${installer}" --write-kubeconfig-mode 644
    for _ in {1..60}; do
      [[ -f "${KUBECONFIG}" ]] && break
      sleep 2
    done
  fi
  [[ -f "${KUBECONFIG}" ]] || fail "k3s kubeconfig was not created"
  require_command kubectl
  wait_for_node_registration 180
  kubectl wait --for=condition=Ready nodes --all --timeout=180s
  kubectl get nodes -o wide
  df -h /
}

# The backup is intentionally outside WORK_DIR and every cleanup target. Legacy
# hooks/manifests may provision prerequisites before setup.
stage_restore_hooks() {
  if [[ -x "${BACKUP_DIR}/restore.sh" ]]; then
    log "restoring E2E certificates and credentials through backup hook"
    E2E_BACKUP_DIR="${BACKUP_DIR}" KUBECONFIG="${KUBECONFIG}" bash "${BACKUP_DIR}/restore.sh"
  elif [[ -d "${BACKUP_DIR}/manifests" ]]; then
    log "restoring E2E Kubernetes backup manifests"
    kubectl apply -R -f "${BACKUP_DIR}/manifests"
  else
    staging_skip "no restore hook or manifests in the backup directory"
  fi
}

# Runs before setup, not after. Restoring afterwards re-applied an older
# mcp-sentinel-secrets over a database setup had just initialised with freshly
# generated credentials, so the API pods came back holding a password the
# database no longer accepted. Restoring first means setup treats these as the
# existing state. It also lets cert-manager find already issued certificates,
# which is the only way to avoid a fresh ACME order.
stage_restore_snapshot() {
  if staging_flag_enabled "${E2E_SKIP_TLS_RESTORE:-0}"; then
    staging_skip "E2E_SKIP_TLS_RESTORE is set; certificates will be issued fresh"
  fi
  if [[ ! -L "${BACKUP_DIR}/platform-runtime/latest" || ! -d "${BACKUP_DIR}/platform-runtime/latest" ]]; then
    staging_skip "no platform runtime snapshot on the VM; certificates will be issued fresh"
  fi
  local namespace
  for namespace in registry mcp-sentinel; do
    kubectl create namespace "${namespace}" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  done
  load_platform_backup_helpers
  log "restoring platform runtime snapshot $(readlink "${BACKUP_DIR}/platform-runtime/latest")"
  # Certificates and ClusterIssuers fail to apply before setup installs the
  # cert-manager CRDs; the Secrets, which are what avoids a new ACME order, do
  # not need them.
  mcpruntime_org_restore_platform_runtime || log "some snapshot objects could not be applied before setup (expected for cert-manager kinds)"
  local file pem
  for file in registry-tls mcp-sentinel-platform-tls; do
    kubectl -n "$([[ ${file} == registry-tls ]] && echo registry || echo mcp-sentinel)" get secret "${file}" \
      -o jsonpath='{.data.tls\.crt}' 2>/dev/null | base64 --decode >"${WORK_DIR}/snapshot-${file}.pem" 2>/dev/null || continue
    pem="${WORK_DIR}/snapshot-${file}.pem"
    openssl x509 -in "${pem}" -noout -subject -issuer -enddate 2>/dev/null | sed "s/^/[snapshot ${file}] /" || true
  done
}

# Advisory only: this is a baseline snapshot of a cluster that has not been set
# up yet. The post-setup diagnostics stage is the gate.
stage_doctor_before() {
  if ! "${BIN}" cluster doctor 2>&1 | tee "${ARTIFACT_DIR}/doctor-before.log"; then
    log "pre-setup doctor reported unmet requirements; continuing because setup provisions them"
  fi
}

# Production setup refuses to run without a platform admin identity. Default
# the address to the ACME contact, and mint a password on first run so the
# credential never lives in the repo. It is persisted in the 0600 env file
# inside the preserved backup directory so repeat runs keep the same account.
ensure_platform_admin_config() {
  local env_file="${BACKUP_DIR}/e2e.env"
  local email="${E2E_PLATFORM_ADMIN_EMAIL:-${E2E_ACME_EMAIL}}"
  local password="${E2E_PLATFORM_ADMIN_PASSWORD:-}"

  if [[ -z "${password}" ]]; then
    # `tr </dev/urandom | head -c` dies of SIGPIPE, which pipefail turns into a
    # script abort, so bound the randomness upstream and slice it in bash.
    local raw
    raw="$(head -c 512 /dev/urandom | LC_ALL=C tr -dc 'A-Za-z0-9')"
    password="${raw:0:32}"
    [[ ${#password} -eq 32 ]] || fail "failed to generate a platform admin password"
    touch "${env_file}"
    chmod 600 "${env_file}"
    printf 'E2E_PLATFORM_ADMIN_PASSWORD=%s\n' "${password}" >>"${env_file}"
    log "generated a platform admin password and stored it in ${env_file}"
  fi

  export MCP_PLATFORM_ADMIN_EMAIL="${email}"
  export MCP_PLATFORM_ADMIN_PASSWORD="${password}"
  export E2E_PLATFORM_ADMIN_EMAIL="${email}"
  export E2E_PLATFORM_ADMIN_PASSWORD="${password}"
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
    --kubeconfig "${KUBECONFIG}"
  )
  # The production CA caps this suite at five runs a week. Set
  # E2E_ACME_STAGING=0 to use it anyway.
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

teardown_vm() {
  log "cleaning disposable Kubernetes/VM state; preserving ${BACKUP_DIR}"
  if [[ -x /usr/local/bin/k3s-uninstall.sh ]]; then
    /usr/local/bin/k3s-uninstall.sh >"${ARTIFACT_DIR}/k3s-uninstall.log" 2>&1 || true
  fi
  rm -rf /etc/rancher /var/lib/rancher /var/lib/kubelet /var/lib/cni /etc/cni /run/k3s /run/flannel "${WORK_DIR}"
  rm -rf /var/tmp/mcp-runtime-e2e-* /tmp/mcp-runtime-e2e.tgz
  rm -f "${ROOT_DIR}"/mcp-img-*.tar
  # Setup builds a service image per component and nothing reclaimed them, so
  # successive runs filled the disk until the kubelet evicted pods under
  # ephemeral-storage pressure.
  if command -v docker >/dev/null 2>&1; then
    docker system prune -af --volumes >"${ARTIFACT_DIR}/docker-prune.log" 2>&1 || true
  fi
  # ROOT_DIR is the directory this script is running from, so it cannot be
  # removed here without risking bash's incremental reads of its own source.
  # The workflow wipes it before each run.
  rm -f "${ROOT_DIR}/bin/mcp-runtime"
  [[ ! -d /var/lib/rancher ]] || fail "k3s state is still present after teardown"
  df -h / | awk 'NR==2 {print "free after cleanup: " $4 " (" $5 " used)"}'
}

cleanup() {
  local status=$?
  set +e
  if [[ -f "${KUBECONFIG}" ]]; then
    STAGING_ABORTED=0 staging_run_stage collect-diagnostics soft "diagnostics collection failed; the cluster API may be down" \
      staging_collect_diagnostics "${ARTIFACT_DIR}/diagnostics"
  fi
  { df -h /; free -m; } >"${ARTIFACT_DIR}/diagnostics/vm-resources.txt" 2>&1
  if staging_flag_enabled "${E2E_CLEANUP:-1}"; then
    STAGING_ABORTED=0
    staging_run_stage snapshot soft "the platform snapshot could not be captured; the next run may issue fresh certificates" backup_platform_runtime
    # The summary must be written before teardown removes WORK_DIR, so record
    # teardown in its own stage and re-render afterwards.
    staging_run_stage teardown soft "k3s/Docker teardown did not complete; the next run may start on a dirty VM" teardown_vm
  fi
  local summary_rc=0
  staging_finish "${RUN_ID}" || summary_rc=1
  if [[ ${status} -eq 0 && ${summary_rc} -ne 0 ]]; then
    status=1
  fi
  log "run ${RUN_ID} finished with status ${status}; artifacts at ${ARTIFACT_DIR}; backup preserved at ${BACKUP_DIR}"
  exit "${status}"
}

# Stage hand-off state (it holds the admin key) lives in WORK_DIR, which is
# outside the uploaded artifacts and removed by teardown.
staging_stages_init "${ARTIFACT_DIR}" "${WORK_DIR}/state.env"
staging_record target-guard passed 0 "" "marker, suffix and production-address checks passed" ""
trap cleanup EXIT

: "${E2E_ACME_EMAIL:?set E2E_ACME_EMAIL in ${BACKUP_DIR}/e2e.env}"
log "options: acme-staging=${E2E_ACME_STAGING:-1} fresh-certificate=${E2E_FRESH_CERTIFICATE:-0} multitenancy=${E2E_RUN_MULTITENANCY:-1} mcp-auth=${E2E_WITH_MCP_AUTH:-0} mtls-issuer=${E2E_MTLS_CLUSTER_ISSUER:-<none>} adapter-certificates=${MCP_ADAPTER_CERTIFICATES:-false} trust-domain=${MCP_TRUST_DOMAIN:-<none>}"

staging_run_stage dependencies critical "a VM package install failed (apt/dnf lock or network)" install_dependencies
staging_run_stage staging-roots critical "could not fetch/install the Let's Encrypt staging roots; image pulls would fail x509 verification" stage_staging_roots
staging_run_stage k3s critical "k3s did not install or the node never became Ready" stage_k3s
staging_run_stage restore-hooks soft "the backup restore hook/manifests failed" stage_restore_hooks
staging_run_stage go-toolchain critical "no usable Go toolchain or the CLI failed to build on the VM" stage_go_toolchain
staging_run_stage traefik critical "the bundled Traefik never became ready" wait_for_traefik 300
staging_run_stage doctor-before soft "pre-setup doctor could not run at all" stage_doctor_before
ensure_platform_admin_config
staging_run_stage restore-snapshot soft "the platform snapshot could not be applied; setup will issue fresh certificates" stage_restore_snapshot
staging_run_stage setup critical "setup failed; read the last ERROR in setup.log (image pull x509/auth, ACME, webhook, or rollout timeout) and diagnostics/events.txt" stage_setup
staging_run_platform_stages
