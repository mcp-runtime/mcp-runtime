#!/usr/bin/env bash
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT
mkdir -p "${TMP}/bin" "${TMP}/work"
export WORKDIR="${TMP}/work"
export CLUSTER_NAME=test
export KUBECONFIG="/dev/null"
export KUBECONFIG_FILE="${TMP}/kind.kubeconfig"
export FAKE_KUBECTL_LOG="${TMP}/kubectl.log"
export FAKE_RBAC_GAP=0
export PATH="${TMP}/bin:${PATH}"
touch "${KUBECONFIG_FILE}"

cat >"${TMP}/bin/sleep" <<'SH'
#!/usr/bin/env bash
exit 0
SH
cat >"${TMP}/bin/kubectl" <<'PY'
#!/usr/bin/env python3
import json, os, sys
args = sys.argv[1:]
if (args[:2] == ["config", "current-context"] or any(arg.startswith("--request-timeout=") for arg in args)) and os.environ.get("KUBECONFIG") != os.environ.get("KUBECONFIG_FILE"):
    print("diagnostic request did not use explicit E2E kubeconfig", file=sys.stderr)
    raise SystemExit(3)
args = [arg for arg in args if not arg.startswith("--request-timeout=")]
with open(os.environ["FAKE_KUBECTL_LOG"], "a", encoding="utf-8") as stream:
    stream.write(json.dumps(args) + "\n")
if args[:2] == ["config", "current-context"]:
    print("kind-test")
elif args[:2] == ["get", "mcpserver"] and "jsonpath={.status." in " ".join(args):
    print("false" if ".status.phase" not in " ".join(args) else "Failed")
elif args[:2] == ["get", "mcpserver"]:
    print(json.dumps({"kind":"MCPServer", "metadata":{"name":"demo","namespace":"mcp-servers","generation":4},
      "status":{"phase":"Failed","observedGeneration":4,"deploymentReady":False,
      "conditions":[{"type":"DeploymentReady","status":"False","reason":"ReconcileError",
      "message":"forbidden: cannot list EndpointSlices", "observedGeneration":4}]}}))
elif args[:1] == ["get"] and args[1] in ("deployment,replicaset,pod", "deployment,statefulset,daemonset,replicaset,pod"):
    print(json.dumps({"kind":"List","items":[
      {"kind":"Deployment","metadata":{"name":"demo","namespace":"mcp-servers","generation":4},
       "spec":{"replicas":1,"template":{"spec":{"containers":[{"env":[{"name":"PASSWORD","value":"never-upload-this-secret"}]}]}}},
       "status":{"readyReplicas":0,"updatedReplicas":0,"conditions":[{"type":"Available","status":"False","reason":"MinimumReplicasUnavailable"}]}},
      {"kind":"Pod","metadata":{"name":"demo-abc","namespace":"mcp-servers"},
       "status":{"phase":"Pending","containerStatuses":[{"name":"app","ready":False,"restartCount":3,
       "state":{"waiting":{"reason":"ImagePullBackOff","message":"retrying image pull"}}}]}}
    ]}))
elif args[:2] == ["get", "events"]:
    if os.environ.get("FAKE_RBAC_GAP") == "1":
        print("Error from server (Forbidden): cannot list events", file=sys.stderr)
        raise SystemExit(1)
    print(json.dumps({"items":[{"type":"Warning","reason":"FailedScheduling","message":"insufficient memory",
      "count":2,"involvedObject":{"kind":"Pod","name":"demo-abc"}}]}))
elif args[:1] == ["logs"]:
    print('{"level":"error","msg":"reconcile failed","authorization":"Bearer never-upload-token","error":"forbidden to list MCPServers","tool_payload":{"argument":"never-upload-payload"}}')
else:
    print("unexpected fake kubectl invocation: " + json.dumps(args), file=sys.stderr)
    raise SystemExit(2)
PY
chmod +x "${TMP}/bin/sleep" "${TMP}/bin/kubectl"

source "${ROOT}/test/e2e/lib/rollout-diagnostics.sh"

set +e
wait_for_named_server_ready demo mcp-servers 1 2>"${TMP}/wait.stderr"
wait_status=$?
set -e
[[ "${wait_status}" -eq 1 ]]
grep -F "timed out waiting for MCPServer demo" "${TMP}/wait.stderr" >/dev/null
diag_dir="${WORKDIR}/rollout-diagnostics"
for file in demo-mcpserver.json demo-workloads.json demo-warning-events.json demo-operator.log; do
  [[ -s "${diag_dir}/${file}" ]]
done
grep -F '"generation": 4' "${diag_dir}/demo-mcpserver.json" >/dev/null
grep -F 'EndpointSlices' "${diag_dir}/demo-mcpserver.json" >/dev/null
grep -F 'ImagePullBackOff' "${diag_dir}/demo-workloads.json" >/dev/null
grep -F 'FailedScheduling' "${diag_dir}/demo-warning-events.json" >/dev/null
grep -F 'reconcile failed' "${diag_dir}/demo-operator.log" >/dev/null
if grep -R -E 'never-upload|Bearer never-upload' "${diag_dir}"; then
  echo '[fail] rollout diagnostics leaked a fake credential or Secret value' >&2
  exit 1
fi

# The failure collector must finish synchronously before the caller begins
# cleanup so the artifact survives even if cleanup removes cluster resources.
printf '%s\n' CLEANUP >>"${FAKE_KUBECTL_LOG}"
python3 - "${FAKE_KUBECTL_LOG}" <<'PY'
import json, pathlib, sys
lines = pathlib.Path(sys.argv[1]).read_text().splitlines()
cleanup = lines.index("CLEANUP")
calls = [json.loads(line) for line in lines[:cleanup]]
assert any(call[:2] == ["get", "mcpserver"] and "-o" in call and "json" in call for call in calls), calls
assert any(call[:2] == ["logs", "-n"] for call in calls), calls
PY
python3 - "${diag_dir}" <<'PY'
from pathlib import Path
import sys
for path in Path(sys.argv[1]).iterdir():
    assert path.stat().st_size <= 65536, f"unbounded diagnostic: {path.name} ({path.stat().st_size} bytes)"
PY

# API/RBAC outages are represented as explicit collection gaps and do not
# replace the original wait failure.
export FAKE_RBAC_GAP=1
e2e_collect_rollout_diagnostics mcp-servers deployment demo demo
grep -F 'collection_gap: warning-events unavailable (kubectl exit 1)' "${diag_dir}/demo-warning-events.json" >/dev/null


# Setup can fail before any named MCPServer wait. Preserve platform rollout
# state and namespace events without serializing pod env/Secret data.
export FAKE_RBAC_GAP=0
e2e_collect_setup_diagnostics
for namespace in mcp-platform mcp-observability mcp-runtime; do
  [[ -s "${diag_dir}/setup-${namespace}-workloads.json" ]]
  [[ -s "${diag_dir}/setup-${namespace}-warning-events.json" ]]
done
grep -F 'ImagePullBackOff' "${diag_dir}/setup-mcp-platform-workloads.json" >/dev/null
if grep -R -E 'never-upload|Bearer never-upload' "${diag_dir}"; then
  echo '[fail] setup diagnostics leaked credentials or pod environment' >&2
  exit 1
fi
python3 - "${ROOT}" <<'PY'
import pathlib, sys
root = pathlib.Path(sys.argv[1])
script = (root / 'test/e2e/qa-e2e.sh').read_text()
cleanup = script.split('cleanup() {', 1)[1].split('trap cleanup EXIT', 1)[0]
assert cleanup.index('e2e_collect_setup_diagnostics') < cleanup.index('cp -R') < cleanup.index('kind delete cluster')
workflow = (root / '.github/workflows/scoped-observability-e2e.yaml').read_text()
assert '.e2e-artifacts/scoped-observability/rollout-diagnostics/' in workflow
PY

echo '[pass] rollout failure diagnostics are bounded, sanitized, and collected before cleanup'
