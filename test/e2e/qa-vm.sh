#!/usr/bin/env bash
set -euo pipefail

# QA E2E on the disposable VM.
#
# Image reuse is the single local Docker tag for each component. The image
# carries mcp-runtime.e2e-content-hash from the last build. The next run
# reuses that image when the checkout hash matches and replaces it when the
# hash changes. Historical tags and GitHub Container Registry are not used.
#
# The Kind cluster and the upstream-image mirror stay on this machine
# (E2E_CACHE_MODE=1) so a later run does not create them again. Setup still
# runs when an image hash misses or the platform is not ready.

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"

# shellcheck source=lib/staging.sh
source "${ROOT_DIR}/test/e2e/lib/staging.sh"

if ! staging_marker_valid "$(staging_marker_path)" "$(hostname)"; then
  staging_err "refusing to run QA E2E on this machine; nothing was changed"
  exit 1
fi

export PATH="/usr/local/go/bin:${PATH}"
BACKUP_DIR="${E2E_BACKUP_DIR:-/var/lib/mcp-runtime-e2e-backup}"
RUN_ID="${E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)}"
ARTIFACT_DIR="${E2E_ARTIFACT_DIR:-${BACKUP_DIR}/qa-runs/${RUN_ID}}"
mkdir -p "${ARTIFACT_DIR}" "${BACKUP_DIR}/qa-gocache" "${BACKUP_DIR}/qa-gomod"
chmod 700 "${BACKUP_DIR}" "${ARTIFACT_DIR}"

install_go_if_needed() {
  local want major minor rank=0
  want="$(awk '/^go [0-9]/ { print $2; exit }' "${ROOT_DIR}/go.mod")"
  [[ -n "${want}" ]] || {
    echo "could not read the go directive from go.mod" >&2
    return 1
  }
  major="${want%%.*}"
  minor="${want#*.}"
  minor="${minor%%.*}"
  if command -v go >/dev/null 2>&1; then
    local raw have_major have_minor
    raw="$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null || true)"
    raw="${raw#go}"
    have_major="${raw%%.*}"
    have_minor="${raw#*.}"
    have_minor="${have_minor%%.*}"
    if [[ "${have_major}" =~ ^[0-9]+$ && "${have_minor}" =~ ^[0-9]+$ ]]; then
      rank=$((have_major * 1000 + have_minor))
    fi
  fi
  if ((rank >= major * 1000 + minor)); then
    return 0
  fi
  local arch url
  case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *)
      echo "unsupported architecture $(uname -m) for Go" >&2
      return 1
      ;;
  esac
  url="https://go.dev/dl/go${want}.linux-${arch}.tar.gz"
  echo "[qa-vm] installing Go ${want}"
  curl -fsSL "${url}" -o "${ARTIFACT_DIR}/go.tar.gz"
  rm -rf /usr/local/go.new
  mkdir -p /usr/local/go.new
  tar -C /usr/local/go.new --strip-components=1 -xzf "${ARTIFACT_DIR}/go.tar.gz"
  rm -rf /usr/local/go
  mv /usr/local/go.new /usr/local/go
  rm -f "${ARTIFACT_DIR}/go.tar.gz"
  export PATH="/usr/local/go/bin:${PATH}"
}

install_kind_if_needed() {
  if command -v kind >/dev/null 2>&1; then
    return 0
  fi
  echo "[qa-vm] installing kind"
  curl --retry 5 --retry-delay 5 --retry-all-errors -fsSL \
    -o /usr/local/bin/kind \
    "https://github.com/kubernetes-sigs/kind/releases/download/v0.30.0/kind-linux-amd64"
  chmod +x /usr/local/bin/kind
}

install_kubectl_if_needed() {
  if command -v kubectl >/dev/null 2>&1; then
    return 0
  fi
  echo "[qa-vm] installing kubectl"
  curl --retry 5 --retry-delay 5 --retry-all-errors -fsSL \
    -o /usr/local/bin/kubectl \
    "https://dl.k8s.io/release/v1.34.1/bin/linux/amd64/kubectl"
  chmod +x /usr/local/bin/kubectl
}

command -v docker >/dev/null 2>&1 || {
  echo "docker is not installed on the disposable VM" >&2
  exit 1
}
docker info >/dev/null 2>&1 || {
  echo "the Docker daemon is not running" >&2
  exit 1
}
install_go_if_needed
install_kind_if_needed
install_kubectl_if_needed

# Do not inherit the VM's k3s kubeconfig. qa-e2e.sh writes its own Kind file.
unset KUBECONFIG

# Staging E2E shares this machine and logs the CLI in to platform.e2e.*.
# QA targets its own Kind cluster, so each run starts with an empty CLI
# profile and none of the public platform host overrides.
export MCP_RUNTIME_CONFIG_DIR="${ARTIFACT_DIR}/cli-config"
mkdir -p "${MCP_RUNTIME_CONFIG_DIR}"
chmod 700 "${MCP_RUNTIME_CONFIG_DIR}"
unset MCP_PLATFORM_DOMAIN MCP_PLATFORM_API_PROFILE MCP_PLATFORM_API_TOKEN \
  MCP_REGISTRY_HOST MCP_REGISTRY_INGRESS_HOST MCP_REGISTRY_ENDPOINT \
  MCP_AUTH_ISSUER_URL MCP_TRUST_DOMAIN

export E2E_CACHE_MODE=1
export E2E_KEEP_CLUSTER=1
export E2E_IMAGE_CACHE=local
export E2E_GHCR_PUSH=0
export MCP_SETUP_IMAGE_CACHE=0
export CLUSTER_NAME="${CLUSTER_NAME:-mcp-qa-vm}"
export LOCAL_REGISTRY_NAME="${LOCAL_REGISTRY_NAME:-mcp-qa-vm-mirror}"
export LOCAL_REGISTRY_PORT="${LOCAL_REGISTRY_PORT:-5001}"
export GOCACHE="${BACKUP_DIR}/qa-gocache"
export GOMODCACHE="${BACKUP_DIR}/qa-gomod"
export E2E_ARTIFACT_DIR="${ARTIFACT_DIR}"
export MCP_IMAGE_PLATFORM="${MCP_IMAGE_PLATFORM:-linux/amd64}"

started="$(date +%s)"
set +e
bash "${ROOT_DIR}/test/e2e/qa-e2e.sh"
status=$?
set -e
ended="$(date +%s)"
{
  echo "elapsed_seconds=$((ended - started))"
  echo "scenarios=${E2E_SCENARIOS:-}"
  echo "image_cache=local-latest"
  echo "cluster=${CLUSTER_NAME}"
  echo "status=${status}"
} >"${ARTIFACT_DIR}/timing.txt"
cat "${ARTIFACT_DIR}/timing.txt"
# Drop dangling image IDs left by the latest-tag replacement. Labeled images stay.
docker image prune -f >/dev/null 2>&1 || true
df -h / | awk 'NR==2 { print "disk_available=" $4 }' | tee -a "${ARTIFACT_DIR}/timing.txt"
exit "${status}"
