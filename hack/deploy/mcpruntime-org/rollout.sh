#!/usr/bin/env bash
# Targeted rollout for mcpruntime.org k3s: build/push split platform APIs + UI and apply RBAC/config.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"
# shellcheck source=lib/registry.sh
source "$SCRIPT_DIR/lib/registry.sh"

mcpruntime_org_load_env 0

ROOT="$(mcpruntime_org_repo_root)"
TAG="${MCP_ROLLOUT_TAG:-verify-$(date +%m%d%H%M)}"
PLATFORM="${MCP_IMAGE_PLATFORM:-linux/amd64}"
REGISTRY_INTERNAL="$(mcpruntime_org_resolve_registry_internal)"
REGISTRY_HOST="$(mcpruntime_org_registry_host)"
PF_PORT="${MCP_REGISTRY_PF_PORT:-15000}"
PUSH_MODE="${MCP_REGISTRY_PUSH_MODE:-internal}"
UPDATE_MCP_AUTH="${MCP_UPDATE_MCP_AUTH:-0}"
MCP_AUTH_IMAGE_SOURCE="${MCP_AUTH_IMAGE_SOURCE:-published}"
MCP_AUTH_SOURCE="${MCP_AUTH_SOURCE:-$(dirname "$ROOT")/mcp-auth}"
MCP_AUTH_BUILD_REF="${MCP_AUTH_BUILD_REF:-}"
MCP_AUTH_TAG="${MCP_AUTH_IMAGE_TAG:-${TAG}-auth}"
MCP_AUTH_DOCKERHUB_IMAGE="${MCP_AUTH_DOCKERHUB_IMAGE:-docker.io/princekrroshan01/mcp-auth-server:latest}"
IMAGE_REGISTRY="$REGISTRY_INTERNAL"
DOCKER_CONFIG_TMP=""

case "$PUSH_MODE" in
  internal) ;;
  public) IMAGE_REGISTRY="$REGISTRY_HOST" ;;
  *) echo "MCP_REGISTRY_PUSH_MODE must be internal or public" >&2; exit 1 ;;
esac

if [[ "$UPDATE_MCP_AUTH" != "0" && "$UPDATE_MCP_AUTH" != "1" ]]; then
  echo "MCP_UPDATE_MCP_AUTH must be 0 or 1" >&2
  exit 1
fi
case "$MCP_AUTH_IMAGE_SOURCE" in
  published|local) ;;
  *) echo "MCP_AUTH_IMAGE_SOURCE must be published or local" >&2; exit 1 ;;
esac
if [[ -n "${MCP_AUTH_CLIENT_ID_METADATA_ENABLED:-}" &&
      ! "$MCP_AUTH_CLIENT_ID_METADATA_ENABLED" =~ ^(0|1|true|false)$ ]]; then
  echo "MCP_AUTH_CLIENT_ID_METADATA_ENABLED must be 0, 1, true, or false" >&2
  exit 1
fi

cleanup() {
  mcpruntime_org_registry_port_forward_stop "$MCP_REGISTRY_PF_PID"
  if [[ -n "$DOCKER_CONFIG_TMP" ]]; then
    rm -rf -- "$DOCKER_CONFIG_TMP"
  fi
}
trap cleanup EXIT

cd "$ROOT"
echo "kubeconfig: $KUBECONFIG"
echo "rollout tag: $TAG"
echo "registry internal: $REGISTRY_INTERNAL"
echo "registry host: $REGISTRY_HOST"
echo "registry push mode: $PUSH_MODE"
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  mcpruntime_org_kubectl get deployment mcp-auth-server -n mcp-platform >/dev/null
  if [[ "$MCP_AUTH_IMAGE_SOURCE" == "local" ]]; then
    : "${MCP_AUTH_BUILD_REF:?set MCP_AUTH_BUILD_REF to the selected mcp-auth branch, tag, or commit}"
    [[ -f "$MCP_AUTH_SOURCE/auth-server/Dockerfile" ]] || {
      echo "mcp-auth Dockerfile not found under $MCP_AUTH_SOURCE/auth-server" >&2
      exit 1
    }
    actual_auth_ref="$(git -C "$MCP_AUTH_SOURCE" rev-parse HEAD)"
    expected_auth_ref="$(git -C "$MCP_AUTH_SOURCE" rev-parse "${MCP_AUTH_BUILD_REF}^{commit}")"
    [[ "$actual_auth_ref" == "$expected_auth_ref" ]] || {
      echo "mcp-auth checkout HEAD ($actual_auth_ref) does not match selected ref ($expected_auth_ref)" >&2
      echo "check out the selected ref and rerun; the script will not switch branches" >&2
      exit 1
    }
    [[ -z "$(git -C "$MCP_AUTH_SOURCE" status --porcelain)" ]] || {
      echo "mcp-auth working tree is dirty; commit or move local changes before building" >&2
      exit 1
    }
    echo "mcp-auth image source: local $expected_auth_ref"
  else
    echo "mcp-auth image source: published $MCP_AUTH_DOCKERHUB_IMAGE"
  fi
  echo "mcp-auth image tag: $MCP_AUTH_TAG"
fi

if [[ "$PUSH_MODE" == "public" ]]; then
  DOCKER_CONFIG_TMP="$(mktemp -d)"
  export DOCKER_CONFIG="$DOCKER_CONFIG_TMP"
  mcpruntime_org_kubectl get secret mcp-ui-credentials -n mcp-platform \
    -o jsonpath='{.data.UI_API_KEY}' | base64 -d \
    | docker login "$REGISTRY_HOST" --username platform-service --password-stdin
fi

echo "building ./bin/mcp-runtime ..."
make build

declare -a API_SERVICES=(
  "platform-api:mcp-platform-api:platform-api"
  "runtime-api:mcp-runtime-api:runtime-api"
  "analytics-api:mcp-analytics-api:analytics-api"
)

for entry in "${API_SERVICES[@]}"; do
  IFS=: read -r dir image container <<<"$entry"
  echo "Building ${IMAGE_REGISTRY}/${image}:${TAG} (${PLATFORM})..."
  docker build --platform "$PLATFORM" --label "org.opencontainers.image.revision=$(git rev-parse HEAD)" \
    -f "services/${dir}/Dockerfile" -t "${IMAGE_REGISTRY}/${image}:${TAG}" .
done

echo "Building ${IMAGE_REGISTRY}/mcp-ui:${TAG} (${PLATFORM})..."
docker build --platform "$PLATFORM" --label "org.opencontainers.image.revision=$(git rev-parse HEAD)" \
  -f services/ui/Dockerfile -t "${IMAGE_REGISTRY}/mcp-ui:${TAG}" .
echo "Building ${IMAGE_REGISTRY}/mcp-runtime-doctor-smoke:${TAG} (${PLATFORM})..."
docker build --platform "$PLATFORM" --label "org.opencontainers.image.revision=$(git rev-parse HEAD)" \
  -f services/doctor-smoke/Dockerfile -t "${IMAGE_REGISTRY}/mcp-runtime-doctor-smoke:${TAG}" .

if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  if [[ "$MCP_AUTH_IMAGE_SOURCE" == "local" ]]; then
    echo "Building ${IMAGE_REGISTRY}/mcp-auth-server:${MCP_AUTH_TAG} (${PLATFORM})..."
    docker build --platform "$PLATFORM" \
      --label "org.opencontainers.image.revision=$actual_auth_ref" \
      -f "$MCP_AUTH_SOURCE/auth-server/Dockerfile" \
      -t "${IMAGE_REGISTRY}/mcp-auth-server:${MCP_AUTH_TAG}" \
      "$MCP_AUTH_SOURCE/auth-server"
  else
    echo "Pulling published mcp-auth image $MCP_AUTH_DOCKERHUB_IMAGE..."
    docker pull --platform "$PLATFORM" "$MCP_AUTH_DOCKERHUB_IMAGE"
    docker tag "$MCP_AUTH_DOCKERHUB_IMAGE" "${IMAGE_REGISTRY}/mcp-auth-server:${MCP_AUTH_TAG}"
  fi
fi

for entry in "${API_SERVICES[@]}"; do
  IFS=: read -r _ image _ <<<"$entry"
  if [[ "$PUSH_MODE" == "public" ]]; then
    docker push "${REGISTRY_HOST}/${image}:${TAG}"
  elif ! mcpruntime_org_registry_push_via_port_forward "$REGISTRY_INTERNAL" "$PF_PORT" "$image" "$TAG"; then
    echo "failed to push ${image}:${TAG}" >&2; exit 1
  fi
done
for image in mcp-ui mcp-runtime-doctor-smoke; do
  if [[ "$PUSH_MODE" == "public" ]]; then
    docker push "${REGISTRY_HOST}/${image}:${TAG}"
  elif ! mcpruntime_org_registry_push_via_port_forward "$REGISTRY_INTERNAL" "$PF_PORT" "$image" "$TAG"; then
    echo "failed to push ${image}:${TAG}" >&2; exit 1
  fi
done
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  if [[ "$PUSH_MODE" == "public" ]]; then
    docker push "${REGISTRY_HOST}/mcp-auth-server:${MCP_AUTH_TAG}"
  elif ! mcpruntime_org_registry_push_via_port_forward "$REGISTRY_INTERNAL" "$PF_PORT" "mcp-auth-server" "$MCP_AUTH_TAG"; then
    echo "failed to push mcp-auth-server:${MCP_AUTH_TAG}" >&2; exit 1
  fi
fi

mcpruntime_org_ensure_platform_pull_secret
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  mcpruntime_org_kubectl patch deployment/mcp-auth-server -n mcp-platform \
    -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]}}}}'
fi

mcpruntime_org_kubectl patch configmap mcp-shared-config -n mcp-platform --type merge -p "$(cat <<PATCH
{
  "data": {
    "PLATFORM_TEAM_TRAEFIK_WATCH": "${PLATFORM_TEAM_TRAEFIK_WATCH:-disabled}",
    "PLATFORM_TRAEFIK_NAMESPACE": "${PLATFORM_TRAEFIK_NAMESPACE:-kube-system}",
    "MCP_REGISTRY_ENDPOINT": "${REGISTRY_HOST}",
    "MCP_REGISTRY_INGRESS_HOST": "${REGISTRY_HOST}",
    "MCP_DOCTOR_SMOKE_IMAGE": "${REGISTRY_HOST}/mcp-runtime-doctor-smoke:${TAG}"
  }
}
PATCH
)"

for entry in "${API_SERVICES[@]}"; do
  IFS=: read -r _ image container <<<"$entry"
  ns=mcp-platform
  if [[ "$image" == "mcp-analytics-api" ]]; then
    ns=mcp-observability
  fi
  mcpruntime_org_kubectl set image "deployment/${image}" -n "$ns" \
    "${container}=${REGISTRY_HOST}/${image}:${TAG}"
done
mcpruntime_org_kubectl set image deployment/mcp-ui -n mcp-platform \
  "ui=${REGISTRY_HOST}/mcp-ui:${TAG}"
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  mcpruntime_org_kubectl set image deployment/mcp-auth-server -n mcp-platform \
    "auth-server=${REGISTRY_HOST}/mcp-auth-server:${MCP_AUTH_TAG}"
  auth_env=()
  if [[ -n "${MCP_AUTH_CLIENT_ID_METADATA_ENABLED:-}" ]]; then
    auth_env+=("MCP_AUTH_CLIENT_ID_METADATA_ENABLED=${MCP_AUTH_CLIENT_ID_METADATA_ENABLED}")
  fi
  if [[ -n "${MCP_AUTH_CLIENT_ID_METADATA_HOSTS:-}" ]]; then
    auth_env+=("MCP_AUTH_CLIENT_ID_METADATA_HOSTS=${MCP_AUTH_CLIENT_ID_METADATA_HOSTS}")
  fi
  if ((${#auth_env[@]} > 0)); then
    mcpruntime_org_kubectl set env deployment/mcp-auth-server -n mcp-platform "${auth_env[@]}"
  fi
fi

for entry in "${API_SERVICES[@]}"; do
  IFS=: read -r _ image _ <<<"$entry"
  ns=mcp-platform
  if [[ "$image" == "mcp-analytics-api" ]]; then
    ns=mcp-observability
  fi
  mcpruntime_org_kubectl rollout status "deployment/${image}" -n "$ns" --timeout=180s
done
mcpruntime_org_kubectl rollout status deployment/mcp-ui -n mcp-platform --timeout=180s
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  mcpruntime_org_kubectl rollout status deployment/mcp-auth-server -n mcp-platform --timeout=180s
fi

echo "Patching team namespace NetworkPolicies for ingress controller (${PLATFORM_TRAEFIK_NAMESPACE:-kube-system})..."
TRAEFIK_NS="${PLATFORM_TRAEFIK_NAMESPACE:-kube-system}"
for ns in $(mcpruntime_org_kubectl get ns -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | grep '^mcp-team-' || true); do
  if ! mcpruntime_org_kubectl get networkpolicy platform-default-deny -n "$ns" >/dev/null 2>&1; then
    continue
  fi
  if mcpruntime_org_kubectl get networkpolicy platform-default-deny -n "$ns" -o json \
    | jq -e --arg traefik_ns "$TRAEFIK_NS" '
        (.spec.ingress // []) | any(.from[]?;
          .namespaceSelector.matchLabels["kubernetes.io/metadata.name"] == $traefik_ns)
      ' >/dev/null; then
    continue
  fi
  mcpruntime_org_kubectl patch networkpolicy platform-default-deny -n "$ns" --type='json' -p="[
    {\"op\":\"add\",\"path\":\"/spec/ingress/-\",\"value\":{\"from\":[{\"namespaceSelector\":{\"matchLabels\":{\"kubernetes.io/metadata.name\":\"${TRAEFIK_NS}\"}}}]}}
  ]"
done

echo "Rollout complete: platform-api/runtime-api/analytics-api/ui tag ${TAG}"
if [[ "$UPDATE_MCP_AUTH" == "1" ]]; then
  echo "mcp-auth-server tag ${MCP_AUTH_TAG}"
fi
