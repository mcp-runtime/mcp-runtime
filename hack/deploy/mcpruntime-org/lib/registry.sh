#!/usr/bin/env bash
# Registry pull-secret and local push helpers for mcpruntime.org k3s rollouts.

# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

MCP_REGISTRY_PF_PID=""

mcpruntime_org_ensure_platform_pull_secret() {
  local registry_host
  registry_host="$(mcpruntime_org_registry_host)"
  local api_key
  api_key="$(mcpruntime_org_kubectl get secret mcp-ui-credentials -n mcp-platform -o jsonpath='{.data.UI_API_KEY}' | base64 -d)"
  if [[ -z "$api_key" ]]; then
    echo "failed to read UI_API_KEY from mcp-platform/mcp-ui-credentials for registry pull secret" >&2
    exit 1
  fi
  local ns
  for ns in mcp-platform mcp-observability; do
    mcpruntime_org_kubectl create secret docker-registry mcp-runtime-registry-pull \
      -n "$ns" \
      --docker-server="$registry_host" \
      --docker-username=platform-service \
      --docker-password="$api_key" \
      --dry-run=client -o yaml | mcpruntime_org_kubectl apply -f -
  done
  mcpruntime_org_kubectl patch deployment/mcp-platform-api deployment/mcp-runtime-api deployment/mcp-ui \
    -n mcp-platform \
    -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]}}}}'
  mcpruntime_org_kubectl patch deployment/mcp-analytics-api \
    -n mcp-observability \
    -p '{"spec":{"template":{"spec":{"imagePullSecrets":[{"name":"mcp-runtime-registry-pull"}]}}}}'
}

mcpruntime_org_resolve_registry_internal() {
  if [[ -n "${MCP_REGISTRY_INTERNAL:-}" ]]; then
    printf '%s' "$MCP_REGISTRY_INTERNAL"
    return 0
  fi
  local cluster_ip port
  cluster_ip="$(mcpruntime_org_kubectl get svc -n registry registry -o jsonpath='{.spec.clusterIP}')"
  port="$(mcpruntime_org_kubectl get svc -n registry registry -o jsonpath='{.spec.ports[0].port}')"
  if [[ -z "$cluster_ip" || -z "$port" ]]; then
    echo "failed to resolve registry ClusterIP from registry/registry service" >&2
    exit 1
  fi
  printf '%s:%s' "$cluster_ip" "$port"
}

mcpruntime_org_registry_local_reachable() {
  local port="$1"
  curl -k -sf "https://127.0.0.1:${port}/v2/" >/dev/null 2>&1 \
    || curl -sf "http://127.0.0.1:${port}/v2/" >/dev/null 2>&1
}

mcpruntime_org_registry_port_forward_start() {
  local port="$1"
  mcpruntime_org_kubectl port-forward -n registry svc/registry "${port}:5000" >/dev/null 2>&1 &
  printf '%s' "$!"
}

mcpruntime_org_registry_port_forward_stop() {
  local pid="$1"
  if [[ -n "$pid" ]]; then
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true
  fi
}

mcpruntime_org_registry_ensure_port_forward() {
  local port="${1:-15000}"
  local pf_pid="${2:-}"

  if mcpruntime_org_registry_local_reachable "$port"; then
    printf '%s' "$pf_pid"
    return 0
  fi

  mcpruntime_org_registry_port_forward_stop "$pf_pid"
  echo "Starting registry port-forward on 127.0.0.1:${port}..." >&2
  pf_pid="$(mcpruntime_org_registry_port_forward_start "$port")"
  MCP_REGISTRY_PF_PID="$pf_pid"
  local i
  for i in $(seq 1 30); do
    if mcpruntime_org_registry_local_reachable "$port"; then
      printf '%s' "$pf_pid"
      return 0
    fi
    sleep 1
  done
  mcpruntime_org_registry_port_forward_stop "$pf_pid"
  echo "registry port-forward on 127.0.0.1:${port} is not reachable" >&2
  echo "free the port or set MCP_REGISTRY_PF_PORT to an unused value" >&2
  exit 1
}

mcpruntime_org_registry_push_via_port_forward() {
  local registry_internal="$1"
  local port="$2"
  local name="$3"
  local tag="$4"
  local attempt
  for attempt in 1 2 3; do
    if docker run --rm -v /var/run/docker.sock:/var/run/docker.sock --add-host=host.docker.internal:host-gateway \
      quay.io/skopeo/stable:v1.14 copy \
      "docker-daemon:${registry_internal}/${name}:${tag}" \
      "docker://host.docker.internal:${port}/${name}:${tag}" \
      --dest-tls-verify=false; then
      return 0
    fi
    if [[ "$attempt" -lt 3 ]]; then
      echo "push ${name}:${tag} failed (attempt ${attempt}); restarting registry port-forward..." >&2
      MCP_REGISTRY_PF_PID="$(mcpruntime_org_registry_ensure_port_forward "$port" "$MCP_REGISTRY_PF_PID")"
    fi
  done
  return 1
}
