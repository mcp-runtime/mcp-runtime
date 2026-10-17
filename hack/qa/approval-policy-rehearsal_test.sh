#!/usr/bin/env bash
# Exercise ownership failures without contacting Docker or Kubernetes.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
# shellcheck source=hack/qa/approvalpolicy/outcomes.sh
source "$ROOT/hack/qa/approvalpolicy/outcomes.sh"
for outcome in Ready Approved,Ready Ready,Approved; do
  approval_outcome_matches Ready "$outcome" || { echo "rejected legitimate Ready condition: $outcome" >&2; exit 1; }
done
for outcome in 'NotReady(never approved)' 'Pending(never approved, no certificate issued)' Denied Failed Ready,Denied Approved; do
  if approval_outcome_matches Ready "$outcome"; then
    echo "false-positive issuance acceptance: $outcome" >&2
    exit 1
  fi
done
for outcome in 'submit-failed: Pending' 'read-failed' 'Pending,Ready'; do
  if approval_outcome_matches Pending "$outcome"; then
    echo "failed or issued request counted as pending: $outcome" >&2
    exit 1
  fi
done
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
mkdir -p "$WORK/bin"
export REHEARSAL_TEST_LOG="$WORK/calls" CLUSTER_NAME=mcp-approval-ownership-test KEEP_CLUSTER=0
cat > "$WORK/bin/kind" <<'EOF'
#!/usr/bin/env bash
echo "kind $*" >> "$REHEARSAL_TEST_LOG"
case "$1 $2" in
  'get clusters')
    [[ "$REHEARSAL_TEST_CASE" != existing ]] || echo "$CLUSTER_NAME"
    exit 0 ;;
  'create cluster') [[ "$REHEARSAL_TEST_CASE" != create-failed ]] ;;
  'delete cluster') exit 0 ;;
  *) exit 1 ;;
esac
EOF
cat > "$WORK/bin/kubectl" <<'EOF'
#!/usr/bin/env bash
echo "kubectl $*" >> "$REHEARSAL_TEST_LOG"
if [[ "$1 $2" == 'config current-context' ]]; then
  echo "kind-$CLUSTER_NAME"
  exit 0
fi
exit 1
EOF
for tool in docker helm jq go; do
  cat > "$WORK/bin/$tool" <<'EOF'
#!/usr/bin/env bash
exit 1
EOF
done
chmod +x "$WORK/bin/"*
export PATH="$WORK/bin:$PATH"
for REHEARSAL_TEST_CASE in existing create-failed setup-failed; do
  export REHEARSAL_TEST_CASE
  : > "$REHEARSAL_TEST_LOG"
  if bash "$ROOT/hack/qa/approval-policy-rehearsal.sh" > "$WORK/output" 2>&1; then
    echo "unexpected success: $REHEARSAL_TEST_CASE" >&2
    exit 1
  fi
  calls="$(cat "$REHEARSAL_TEST_LOG")"
  if [[ "$REHEARSAL_TEST_CASE" == existing && "$calls" == *'kind create'* ]]; then
    echo 'attempted to create over an existing cluster' >&2
    exit 1
  fi
  if [[ "$REHEARSAL_TEST_CASE" != setup-failed && "$calls" == *'kind delete'* ]]; then
    echo 'attempted to delete a cluster this run did not create' >&2
    exit 1
  fi
  if [[ "$REHEARSAL_TEST_CASE" == setup-failed && "$calls" != *'kind delete cluster'* ]]; then
    echo 'failed to clean up the cluster this run created' >&2
    exit 1
  fi
done
echo 'PASS: approval rehearsal preserves existing clusters and cleans up only its own'
