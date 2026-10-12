#!/usr/bin/env bash
# Shared env and kubectl helpers for mcpruntime.org k3s hack scripts.

mcpruntime_org_repo_root() {
  if [[ -n "${MCPRUNTIME_ORG_ROOT:-}" ]]; then
    printf '%s' "$MCPRUNTIME_ORG_ROOT"
    return 0
  fi
  MCPRUNTIME_ORG_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../.." && pwd)"
  printf '%s' "$MCPRUNTIME_ORG_ROOT"
}

# Require a reviewed private deployment profile; never source ambient .env/examples.
mcpruntime_org_load_env() {
  local caller_kubeconfig="${MCP_SETUP_KUBECONFIG:-${KUBECONFIG:-}}"

  MCP_DEPLOY_ENV_FILE="${MCP_DEPLOY_ENV:-config/deployments/mcpruntime-org.env}"
  local root
  root="$(mcpruntime_org_repo_root)"
  cd "$root" || return 1

  if [[ -f "$MCP_DEPLOY_ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    set -a && source "$MCP_DEPLOY_ENV_FILE" && set +a

  else
    echo "error: $MCP_DEPLOY_ENV_FILE is required for production cluster operations" >&2
    echo "       copy $MCP_DEPLOY_ENV_EXAMPLE and customize" >&2
    exit 1
  fi

  # The production profile supplies a default kubeconfig, while an explicit
  # shell override must win (for example, a shared named-context kubeconfig).
  if [[ -n "$caller_kubeconfig" ]]; then
    KUBECONFIG="$caller_kubeconfig"
    MCP_SETUP_KUBECONFIG="$caller_kubeconfig"
  else
    KUBECONFIG="${MCP_SETUP_KUBECONFIG:-${KUBECONFIG:-}}"
  fi
  : "${KUBECONFIG:?set KUBECONFIG or MCP_SETUP_KUBECONFIG in $MCP_DEPLOY_ENV_FILE}"
  export KUBECONFIG MCP_DEPLOY_ENV_FILE
}

mcpruntime_org_kubectl() {
  local kube_args=(--kubeconfig "$KUBECONFIG")
  if [[ -n "${MCP_KUBE_CONTEXT:-}" ]]; then
    kube_args+=(--context "$MCP_KUBE_CONTEXT")
  fi
  kubectl "${kube_args[@]}" "$@"
}

mcpruntime_org_require_cluster() {
  : "${MCP_KUBE_CONTEXT:?Set the explicit target context in the deployment profile}"
  : "${MCP_EXPECTED_CLUSTER_UID:?Set the target kube-system namespace UID in the deployment profile}"
  local actual
  actual="$(mcpruntime_org_kubectl get namespace kube-system -o jsonpath='{.metadata.uid}')"
  [[ -n "$actual" && "$actual" == "$MCP_EXPECTED_CLUSTER_UID" ]] || {
    echo "error: cluster identity does not match the deployment profile" >&2
    return 1
  }
  echo "Verified context $MCP_KUBE_CONTEXT, cluster $actual"
}

mcpruntime_org_build_cli() {
  : "${MCP_DEPLOY_REF:?Set the exact selected Runtime source ref}"
  local root expected actual
  root="$(mcpruntime_org_repo_root)"
  expected="$(git -C "$root" rev-parse "${MCP_DEPLOY_REF}^{commit}")"
  actual="$(git -C "$root" rev-parse HEAD)"
  [[ "$actual" == "$expected" && -z "$(git -C "$root" status --porcelain)" ]] || {
    echo "error: use a clean checkout at MCP_DEPLOY_REF=$MCP_DEPLOY_REF" >&2
    return 1
  }
  make -C "$root" build VERSION="$MCP_DEPLOY_REF" COMMIT="$expected"
  export MCP_DEPLOY_CLI="$root/bin/mcp-runtime"
}
