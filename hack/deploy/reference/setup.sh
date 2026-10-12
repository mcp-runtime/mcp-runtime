#!/usr/bin/env bash
# Deliberate full setup; ordinary release updates use rollout.sh.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/deploy/reference/lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"
if [[ "${1:-}" == --help ]]; then
  echo 'Usage: setup.sh --yes'
  echo 'Requires a configured deployment profile and clean selected source ref.'
  echo 'Uses existing TLS Secrets. Restore is a separate, explicit recovery operation.'
  exit 0
fi
[[ $# == 1 && "$1" == --yes ]] || { echo 'Full setup requires --yes' >&2; exit 2; }
mcpruntime_org_load_env
mcpruntime_org_require_cluster
[[ "${MCP_SETUP_TEST_MODE:-0}" != 1 ]] || { echo 'Use the contributor workflow for test mode' >&2; exit 1; }
# Existing auth must be selected explicitly so setup cannot drop its operator configuration.
auth_deployment="$(mcpruntime_org_kubectl get deployment mcp-auth-server -n mcp-platform --ignore-not-found -o json)"
if [[ -n "$auth_deployment" ]]; then
  [[ "${MCP_SETUP_WITH_MCP_AUTH_SERVER:-0}" == 1 ]] || {
    echo 'Existing MCP Auth requires MCP_SETUP_WITH_MCP_AUTH_SERVER=1 and its current profile' >&2; exit 1;
  }
  live_image="$(printf '%s' "$auth_deployment" | jq -er '.spec.template.spec.containers[0].image')"
  [[ "${MCP_SETUP_MCP_AUTH_SERVER_IMAGE:-}" == "$live_image" ]] || {
    echo 'Setup must retain the existing MCP Auth image; update it separately' >&2; exit 1;
  }
fi
for target in registry/registry-tls mcp-platform/mcp-platform-tls; do
  ns="${target%%/*}"; name="${target#*/}"
  mcpruntime_org_kubectl get secret "$name" -n "$ns" -o json | jq -e \
    '.type == "kubernetes.io/tls" and .data["tls.crt"] != null and .data["tls.key"] != null' >/dev/null
done
if [[ "${MCP_SETUP_WITH_MCP_AUTH_SERVER:-0}" == 1 ]]; then
  mcpruntime_org_kubectl get secret "${MCP_SETUP_MCP_AUTH_TLS_SECRET:-mcp-auth-server-tls}" -n mcp-platform -o json | jq -e \
    '.type == "kubernetes.io/tls" and .data["tls.crt"] != null and .data["tls.key"] != null' >/dev/null
fi
mcpruntime_org_build_cli
args=(setup --env-file "$MCP_DEPLOY_ENV_FILE" --kubeconfig "$KUBECONFIG" --context "$MCP_KUBE_CONTEXT"
  --strict-prod --with-tls --provided-tls-secrets --skip-cert-manager-install
  --registry-mode "${MCP_SETUP_REGISTRY_MODE:-bundled-https}"
  --platform-mode "${MCP_SETUP_PLATFORM_MODE:-tenant}" --ingress "${MCP_SETUP_INGRESS:-none}")
if [[ -n "${MCP_SETUP_MTLS_CLUSTER_ISSUER:-}" ]]; then
  args+=(--mtls-cluster-issuer "$MCP_SETUP_MTLS_CLUSTER_ISSUER")
fi
# --env-file carries the selected issuer, connector, signing key, image and resource URLs.
unset MCP_SETUP_TLS_CLUSTER_ISSUER MCP_TLS_CLUSTER_ISSUER MCP_ACME_EMAIL
"$MCP_DEPLOY_CLI" "${args[@]}" --tls-cluster-issuer= --acme-email=
