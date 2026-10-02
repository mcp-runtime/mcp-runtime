#!/usr/bin/env bash
# Namespace selection and MCP Runtime wipe helpers for mcpruntime.org k3s clean.

# shellcheck source=env.sh
source "$(dirname "${BASH_SOURCE[0]}")/env.sh"

MCP_RUNTIME_PRESERVED_NAMESPACES=(
  kube-system
  kube-public
  kube-node-lease
  default
  cert-manager
)

MCP_RUNTIME_PLATFORM_NAMESPACES=(
  mcp-runtime
  mcp-platform
  mcp-observability
  mcp-log-collector
  registry
  traefik
)

mcpruntime_org_is_preserved_namespace() {
  local ns="$1"
  local preserved
  for preserved in "${MCP_RUNTIME_PRESERVED_NAMESPACES[@]}"; do
    if [[ "$ns" == "$preserved" ]]; then
      return 0
    fi
  done
  return 1
}

mcpruntime_org_namespace_name_is_mcp_runtime() {
  local ns="$1"
  local known
  for known in "${MCP_RUNTIME_PLATFORM_NAMESPACES[@]}"; do
    if [[ "$ns" == "$known" ]]; then
      return 0
    fi
  done
  case "$ns" in
    mcp-servers|mcp-servers-*|mcp-team-*)
      return 0
      ;;
  esac
  return 1
}

mcpruntime_org_namespace_has_mcp_runtime_label() {
  local ns="$1"
  local managed_by platform_managed
  managed_by="$(mcpruntime_org_kubectl get namespace "$ns" -o jsonpath='{.metadata.labels.app\.kubernetes\.io/managed-by}' 2>/dev/null || true)"
  platform_managed="$(mcpruntime_org_kubectl get namespace "$ns" -o jsonpath='{.metadata.labels.platform\.mcpruntime\.org/managed}' 2>/dev/null || true)"
  [[ "$managed_by" == "mcp-runtime" || "$platform_managed" == "true" ]]
}

mcpruntime_org_should_delete_namespace() {
  local ns="$1"
  if mcpruntime_org_is_preserved_namespace "$ns"; then
    return 1
  fi
  if mcpruntime_org_namespace_name_is_mcp_runtime "$ns"; then
    return 0
  fi
  mcpruntime_org_namespace_has_mcp_runtime_label "$ns"
}

mcpruntime_org_list_namespaces_to_delete() {
  local ns
  while IFS= read -r ns; do
    [[ -z "$ns" ]] && continue
    if mcpruntime_org_should_delete_namespace "$ns"; then
      printf '%s\n' "$ns"
    fi
  done < <(mcpruntime_org_kubectl get ns --no-headers 2>/dev/null | awk '{print $1}')
}

mcpruntime_org_clean_cluster_scoped_mcp() {
  local dry_run="${1:-0}"
  echo "Deleting cluster-scoped MCP access objects (tenant/user workload metadata)..."
  if [[ "$dry_run" == "1" ]]; then
    echo "[dry-run] would delete mcpserver,mcpaccessgrant,mcpagentsession --all -A"
    echo "[dry-run] would delete clusterrole,clusterrolebinding -l app.kubernetes.io/managed-by=mcp-runtime"
    return 0
  fi
  if ! mcpruntime_org_kubectl delete mcpserver,mcpaccessgrant,mcpagentsession --all -A --ignore-not-found --wait=false; then
    echo "error: failed to delete MCP access CRs" >&2
    exit 1
  fi
  if ! mcpruntime_org_kubectl delete clusterrole,clusterrolebinding \
    -l app.kubernetes.io/managed-by=mcp-runtime --ignore-not-found; then
    echo "error: failed to delete MCP Runtime cluster RBAC" >&2
    exit 1
  fi
}

mcpruntime_org_clean_app_namespaces() {
  local dry_run="${1:-0}"
  local wait_ns="${2:-0}"
  local to_delete=()
  local ns
  while IFS= read -r ns; do
    [[ -z "$ns" ]] && continue
    to_delete+=("$ns")
  done < <(mcpruntime_org_list_namespaces_to_delete)

  if [[ ${#to_delete[@]} -eq 0 ]]; then
    echo "no MCP Runtime namespaces to delete"
    return 0
  fi
  echo "Deleting MCP Runtime namespaces (tenant/user data in these namespaces will be lost):"
  printf '  %s\n' "${to_delete[@]}"
  if [[ "$dry_run" == "1" ]]; then
    echo "[dry-run] would delete namespaces above"
    return 0
  fi
  local wait_flag=(--wait=false)
  if [[ "$wait_ns" == "1" ]]; then
    wait_flag=(--wait=true --timeout=15m)
  fi
  mcpruntime_org_kubectl delete ns "${to_delete[@]}" --ignore-not-found "${wait_flag[@]}"
  if [[ "$wait_ns" == "1" ]]; then
    echo "namespace deletion finished"
  else
    echo "namespace deletion started (may still be Terminating; use --wait or poll before setup)"
  fi
}
