#!/usr/bin/env bash
# Build and Trivy-scan MCP Runtime platform images (stdlib + OS CVEs in shipped binaries).
# Mirrors .github/workflows/security-trivy.yaml image job flags.
#
# Usage:
#   bash hack/trivy-platform-images.sh              # all platform images
#   bash hack/trivy-platform-images.sh platform-api analytics-api
#   bash hack/trivy-platform-images.sh --scan-only platform-api
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

TRIVY_ACTION_REF="aquasecurity/trivy-action@ed142fd0673e97e23eac54620cfb913e5ce36c25" # 0.36.0
SCAN_ONLY=0
BUILD_TAG="${BUILD_TAG:-audit}"

ALL_IMAGE_KEYS="operator platform-api analytics-api runtime-api ui ingest processor mcp-gateway doctor-smoke"

image_dockerfile() {
  case "$1" in
    operator) echo "Dockerfile.operator" ;;
    platform-api) echo "services/platform-api/Dockerfile" ;;
    analytics-api) echo "services/analytics-api/Dockerfile" ;;
    runtime-api) echo "services/runtime-api/Dockerfile" ;;
    ui) echo "services/ui/Dockerfile" ;;
    ingest) echo "services/ingest/Dockerfile" ;;
    processor) echo "services/processor/Dockerfile" ;;
    mcp-gateway) echo "services/mcp-gateway/Dockerfile" ;;
    doctor-smoke) echo "services/doctor-smoke/Dockerfile" ;;
    *) return 1 ;;
  esac
}

image_repo() {
  case "$1" in
    operator) echo "mcp-runtime-operator" ;;
    platform-api) echo "mcp-platform-api" ;;
    analytics-api) echo "mcp-analytics-api" ;;
    runtime-api) echo "mcp-runtime-api" ;;
    ui) echo "mcp-ui" ;;
    ingest) echo "mcp-ingest" ;;
    processor) echo "mcp-processor" ;;
    mcp-gateway) echo "mcp-gateway" ;;
    doctor-smoke) echo "mcp-runtime-doctor-smoke" ;;
    *) return 1 ;;
  esac
}

usage() {
  echo "usage: $0 [--scan-only] [image-key ...]" >&2
  echo "image keys: $ALL_IMAGE_KEYS" >&2
}

selected=()
while [ $# -gt 0 ]; do
  case "$1" in
    --scan-only)
      SCAN_ONLY=1
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      selected+=("$1")
      shift
      ;;
  esac
done

if [ "${#selected[@]}" -eq 0 ]; then
  # shellcheck disable=SC2206
  selected=($ALL_IMAGE_KEYS)
fi

if ! command -v trivy >/dev/null 2>&1; then
  echo "trivy not found; install from https://trivy.dev/latest/getting-started/installation/" >&2
  exit 1
fi

if ! command -v docker >/dev/null 2>&1; then
  echo "docker not found" >&2
  exit 1
fi

pass=0
fail=0

build_image() {
  local dockerfile="$1" repo_tag="$2"
  if [ "$SCAN_ONLY" = "1" ]; then
    if ! docker image inspect "$repo_tag" >/dev/null 2>&1; then
      echo "FAIL $repo_tag image missing (drop --scan-only or build first)" >&2
      return 1
    fi
    return 0
  fi
  echo "==> docker build --pull -f $dockerfile -t $repo_tag ."
  docker build --pull -f "$dockerfile" -t "$repo_tag" .
}

scan_image() {
  local name="$1" repo_tag="$2"
  echo "==> trivy image $repo_tag"
  if trivy image \
    --exit-code 1 \
    --severity CRITICAL,HIGH \
    --ignore-unfixed \
    --vuln-type os,library \
    --format table \
    --skip-version-check \
    "$repo_tag"; then
    echo "PASS trivy-$name"
    pass=$((pass + 1))
    return 0
  fi
  echo "FAIL trivy-$name"
  fail=$((fail + 1))
  return 1
}

for key in "${selected[@]}"; do
  dockerfile="$(image_dockerfile "$key")" || { echo "unknown image key: $key" >&2; usage; exit 1; }
  repo_name="$(image_repo "$key")"
  repo_tag="${repo_name}:${BUILD_TAG}"
  build_image "$dockerfile" "$repo_tag"
  scan_image "$key" "$repo_tag" || true
done

echo "=== TRIVY SUMMARY pass=$pass fail=$fail (action pin: $TRIVY_ACTION_REF) ==="
[ "$fail" -eq 0 ]
