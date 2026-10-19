#!/usr/bin/env bash
# Offline regression: staged issuer provisioning must fail closed and keep
# the workload signing key separate from the bundled registry root.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source "$ROOT/test/e2e/lib/staging.sh"
source "$ROOT/test/e2e/lib/staging-issuer.sh"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT
ROOT_DIR="$ROOT" WORK_DIR="$TMP" E2E_MTLS_CLUSTER_ISSUER=mcp-e2e-workload
MCP_TRUST_DOMAIN=e2e.mcpruntime.org
staging_issuer_policies >"$TMP/policies.yaml"
grep -q 'name: mcp-e2e-workload' "$TMP/policies.yaml"
grep -q 'spiffe://e2e.mcpruntime.org/ns/\*/session/\*' "$TMP/policies.yaml"
grep -q 'name: letsencrypt-staging' "$TMP/policies.yaml"
# No automatic operator acknowledgement before live approval probes succeed.
MCP_WORKLOAD_ISSUER_APPROVAL_ACK=true
kubectl() { return 17; }
if staging_prepare_workload_issuer; then
  echo 'fixture ignored a failed prerequisite' >&2; exit 1
fi
[[ -z "${MCP_WORKLOAD_ISSUER_APPROVAL_ACK:-}" ]]
E2E_MTLS_CLUSTER_ISSUER=mcp-runtime-ca
if staging_issuer_policies >"$TMP/invalid"; then
  echo 'fixture accepted the registry issuer as workload issuer' >&2; exit 1
fi
[[ ! -s "$TMP/invalid" ]]
# Exercise the actual stage boundary: dependency PATH exports disappear in the
# subshell, and the verified acknowledgement must survive via private state.
E2E_MTLS_CLUSTER_ISSUER=mcp-e2e-workload
mkdir -p "$WORK_DIR/linux-amd64"
printf '#!/usr/bin/env bash\nexit 0\n' >"$WORK_DIR/linux-amd64/helm"
chmod +x "$WORK_DIR/linux-amd64/helm"
kubectl() { if [[ "$*" == *'apply -f -'* ]]; then cat >/dev/null; fi; return 0; }
staging_verify_workload_approval() { return 0; }
staging_stages_init "$TMP/artifacts" "$TMP/private-state"
unset MCP_WORKLOAD_ISSUER_APPROVAL_ACK
staging_run_stage issuer critical fixture staging_prepare_workload_issuer
[[ "$(staging_stage_status issuer)" == passed ]]
[[ "${MCP_WORKLOAD_ISSUER_APPROVAL_ACK:-}" == true ]]
# Failed init jobs must retain a bounded useful error, with credentials removed.
unset E2E_PLATFORM_API_TOKEN
staging_platform_namespaces() { printf '%s\n' mcp-observability; }
kubectl() {
  case "$*" in
    *'logs job/kafka-topic-init'*) printf '%s\n' 'Permission denied: /private/password; password=fixture-password Bearer fixture-token';;
    *'get secret '*) return 1;;
  esac
  return 0
}
staging_collect_diagnostics "$TMP/diagnostics"
grep -q 'Permission denied' "$TMP/diagnostics/mcp-observability-kafka-topic-init.log"
if grep -q 'fixture-password\|fixture-token' "$TMP/diagnostics/mcp-observability-kafka-topic-init.log"; then
  echo 'initialization diagnostics leaked credentials' >&2; exit 1
fi
echo 'staging issuer offline regressions passed'
