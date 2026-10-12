#!/usr/bin/env bash
# Release deployment through the supported component-aware CLI.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=hack/deploy/mcpruntime-org/lib/env.sh
source "$SCRIPT_DIR/lib/env.sh"
if [[ "${1:-}" == --help ]]; then
  echo 'Usage: rollout.sh --dry-run | --yes'
  echo 'Requires MCP_DEPLOY_ENV, MCP_DEPLOY_REF=vX.Y.Z, explicit context and cluster UID.'
  echo 'Builds all Runtime release components; preserves MCP Auth, certificates and scoped pull Secrets.'
  exit 0
fi
[[ $# == 1 && ( "$1" == --dry-run || "$1" == --yes ) ]] || { echo 'Use --dry-run or --yes' >&2; exit 2; }
mode="$1"
mcpruntime_org_load_env
[[ "${MCP_DEPLOY_REF:-}" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Select a stable release tag in MCP_DEPLOY_REF' >&2; exit 1; }
mcpruntime_org_require_cluster
mcpruntime_org_build_cli
"$MCP_DEPLOY_CLI" update --to "$MCP_DEPLOY_REF" --build \
  --source "$(mcpruntime_org_repo_root)" --image-platform "${MCP_IMAGE_PLATFORM:-linux/amd64}" \
  --kubeconfig "$KUBECONFIG" --context "$MCP_KUBE_CONTEXT" "$mode"
