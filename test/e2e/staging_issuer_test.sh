#!/usr/bin/env bash
# Offline regression: staged issuer provisioning must fail closed and keep
# the workload signing key separate from the bundled registry root.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
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
echo 'staging issuer offline regressions passed'
