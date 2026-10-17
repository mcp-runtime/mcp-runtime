#!/usr/bin/env bash

# Bounded, redacted diagnostics for failed workload rollouts. This collector is
# intentionally read-only and only runs against the harness's disposable Kind
# context. It writes allowlisted summaries under WORKDIR for CI artifact upload.

e2e_diag_dir() {
  printf '%s/rollout-diagnostics' "${WORKDIR}"
}

e2e_diag_safe_name() {
  printf '%s' "$1" | tr -c 'A-Za-z0-9._-' '_'
}

e2e_diag_write_gap() {
  local file="$1"
  local label="$2"
  local reason="$3"
  printf 'collection_gap: %s unavailable (%s)\n' "${label}" "${reason}" >"${file}"
}

e2e_diag_redact_text() {
  local max_bytes="${1:-65536}"
  python3 -c '
import re, sys
limit = int(sys.argv[1])
text = sys.stdin.read()
text = re.sub(r"\x1b\[[0-?]*[ -/]*[@-~]", "", text)
text = re.sub(r"(?i)(https?://)[^/@\s]+:[^/@\s]+@", r"\1[REDACTED]@", text)
text = re.sub(r"(?i)\b(Bearer|Basic)\s+[A-Za-z0-9+/=._~-]+", r"\1 [REDACTED]", text)
text = re.sub(r"(?i)([\"\x27]?(?:authorization|proxy-authorization|access[_-]?token|refresh[_-]?token|id[_-]?token|password|passwd|secret|api[_-]?key|client[_-]?secret|private[_-]?key)[\"\x27]?\s*[:=]\s*[\"\x27]?)[^\s,\"\x27}]+", r"\1[REDACTED]", text)
text = re.sub(r"(?i)\beyJ[A-Za-z0-9_-]{16,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\b", "[REDACTED_JWT]", text)
encoded = text.encode("utf-8", "replace")[:limit]
sys.stdout.write(encoded.decode("utf-8", "ignore"))
' "${max_bytes}"
}

e2e_diag_capture() {
  local label="$1"
  local file="$2"
  shift 2
  local temp_dir raw error status context expected kubeconfig
  temp_dir="$(mktemp -d "${TMPDIR:-/tmp}/mcp-e2e-diag.XXXXXX")" || {
    e2e_diag_write_gap "${file}" "${label}" "temporary storage unavailable"
    return 0
  }
  raw="${temp_dir}/stdout"
  error="${temp_dir}/stderr"

  expected="kind-${CLUSTER_NAME:-mcp-e2e}"
  kubeconfig="${KUBECONFIG_FILE:-}"
  if [[ -z "${kubeconfig}" || ! -f "${kubeconfig}" ]]; then
    e2e_diag_write_gap "${file}" "${label}" "explicit E2E kubeconfig is unavailable"
    rm -rf "${temp_dir}"
    return 0
  fi
  if ! context="$(KUBECONFIG="${kubeconfig}" kubectl config current-context 2>"${error}")" || [[ "${context}" != "${expected}" ]]; then
    e2e_diag_write_gap "${file}" "${label}" "target context is not ${expected}"
    rm -rf "${temp_dir}"
    return 0
  fi

  if KUBECONFIG="${kubeconfig}" kubectl --request-timeout=8s "$@" >"${raw}" 2>"${error}"; then
    if ! e2e_diag_format "${label}" <"${raw}" | e2e_diag_redact_text >"${file}"; then
      e2e_diag_write_gap "${file}" "${label}" "formatter failed"
    fi
  else
    status=$?
    {
      printf 'collection_gap: %s unavailable (kubectl exit %s)\n' "${label}" "${status}"
      e2e_diag_redact_text <"${error}"
    } >"${file}"
  fi
  rm -rf "${temp_dir}"
  return 0
}

e2e_diag_format() {
  local label="$1"
  python3 -c '
import json, re, sys
label = sys.argv[1]
if label == "runtime-init-logs":
    rows = []
    for line in sys.stdin:
        message = re.sub(r"^\[pod/[^]]+\]\s*", "", line)
        message = re.sub(r"^\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}\s+", "", message)
        if message.startswith(("[WARN] Kubernetes client initialization failed:", "workload network policy reconciliation failed:")):
            rows.append(message.rstrip())
    print("\n".join(rows) if rows else "collection_gap: no runtime initialization failure was logged")
    raise SystemExit(0)
if label in ("operator-logs", "gateway-logs"):
    allowed = ("ts", "time", "timestamp", "level", "logger", "msg", "error", "name", "namespace", "controller", "reconcileID", "generation", "port")
    rows, skipped = [], 0
    for line in sys.stdin:
        try:
            record = json.loads(line)
        except Exception:
            skipped += 1
            continue
        if isinstance(record, dict):
            row = {key: record[key] for key in allowed if key in record}
            if row:
                rows.append(json.dumps(row, sort_keys=True))
    if rows:
        sys.stdout.write("\n".join(rows) + "\n")
    else:
        print("collection_gap: operator logs omitted because no structured entries were available")
    raise SystemExit(0)
try:
    doc = json.load(sys.stdin)
except Exception as exc:
    print(f"collection_gap: {label} returned invalid JSON ({type(exc).__name__})")
    raise SystemExit(0)

def condition(item):
    allowed = ("type", "status", "reason", "message", "observedGeneration", "lastTransitionTime")
    return {key: item[key] for key in allowed if key in item}

def pod_status(item):
    status = item.get("status", {})
    result = {key: status[key] for key in ("phase", "reason", "message", "startTime") if key in status}
    if status.get("conditions"):
        result["conditions"] = [condition(value) for value in status["conditions"]]
    containers = []
    for container in status.get("containerStatuses", []):
        entry = {key: container[key] for key in ("name", "ready", "restartCount") if key in container}
        state = container.get("state", {})
        for kind in ("waiting", "terminated"):
            if kind in state:
                entry[kind] = {key: state[kind][key] for key in ("reason", "message", "exitCode") if key in state[kind]}
        containers.append(entry)
    if containers:
        result["containers"] = containers
    return result

if label == "mcpserver":
    metadata = doc.get("metadata", {})
    status = doc.get("status", {})
    out = {"kind": "MCPServer", "namespace": metadata.get("namespace"), "name": metadata.get("name"), "generation": metadata.get("generation")}
    for key in ("observedGeneration", "phase", "deploymentReady", "serviceReady", "ingressReady", "message"):
        if key in status:
            out[key] = status[key]
    if status.get("conditions"):
        out["conditions"] = [condition(value) for value in status["conditions"]]
    print(json.dumps(out, indent=2, sort_keys=True))
elif label == "workloads":
    items = doc.get("items", [])[:40]
    out = []
    for item in items:
        metadata, status = item.get("metadata", {}), item.get("status", {})
        kind = item.get("kind", "Resource")
        row = {"kind": kind, "namespace": metadata.get("namespace"), "name": metadata.get("name"), "generation": metadata.get("generation")}
        if kind in ("Deployment", "ReplicaSet", "StatefulSet", "DaemonSet"):
            spec = item.get("spec", {})
            for key in ("replicas", "updatedReplicas", "readyReplicas", "availableReplicas", "unavailableReplicas", "observedGeneration"):
                if key in status:
                    row[key] = status[key]
            if status.get("conditions"):
                row["conditions"] = [condition(value) for value in status["conditions"]]
            if kind == "Deployment" and "replicas" in spec:
                row["desiredReplicas"] = spec["replicas"]
        elif kind == "Pod":
            row["status"] = pod_status(item)
            containers = []
            for container in item.get("spec", {}).get("containers", []):
                ports = [{key: value for key, value in port.items() if key in ("name", "containerPort", "protocol")}
                         for port in container.get("ports", [])]
                containers.append({"name": container.get("name"), "ports": ports})
            if containers:
                row["containers"] = containers
        out.append(row)
    print(json.dumps({"kind": "WorkloadSummary", "items": out}, indent=2, sort_keys=True))
elif label == "warning-events":
    events = doc.get("items", [])[-40:]
    out = []
    for item in events:
        metadata, involved = item.get("metadata", {}), item.get("involvedObject", {})
        out.append({key: value for key, value in {
            "lastTimestamp": item.get("lastTimestamp") or metadata.get("creationTimestamp"),
            "type": item.get("type"), "reason": item.get("reason"),
            "message": item.get("message"), "count": item.get("count"),
            "objectKind": involved.get("kind"), "objectName": involved.get("name"),
            "source": (item.get("source") or {}).get("component"),
        }.items() if value is not None})
    print(json.dumps({"kind": "WarningEvents", "items": out}, indent=2, sort_keys=True))
else:
    print(json.dumps(doc, indent=2, sort_keys=True))
' "${label}"
}

e2e_collect_rollout_diagnostics() {
  local namespace="$1"
  local kind="$2"
  local name="$3"
  local server_name="${4:-${name}}"
  local directory safe_server safe_namespace
  directory="$(e2e_diag_dir)"
  safe_server="$(e2e_diag_safe_name "${server_name}")"
  safe_namespace="$(e2e_diag_safe_name "${namespace}")"
  mkdir -p "${directory}"
  echo "[debug] collecting bounded rollout diagnostics for ${safe_namespace}/${safe_server}"

  e2e_diag_capture "mcpserver" "${directory}/${safe_server}-mcpserver.json" \
    get mcpserver "${server_name}" -n "${namespace}" -o json
  e2e_diag_capture "workloads" "${directory}/${safe_server}-workloads.json" \
    get deployment,replicaset,pod -n "${namespace}" -l "app=${server_name}" -o json
  e2e_diag_capture "warning-events" "${directory}/${safe_server}-warning-events.json" \
    get events -n "${namespace}" --field-selector "type=Warning,involvedObject.name=${server_name}" -o json
  e2e_diag_capture "operator-logs" "${directory}/${safe_server}-operator.log" \
    logs -n mcp-runtime deployment/mcp-runtime-operator-controller-manager --all-containers=true --since=10m --tail=100

  printf '[debug] rollout diagnostics saved under %s\n' "${directory}"
}

# Setup failures happen before the named-server waits. Capture status/events
# for the platform namespaces before cleanup; never serialize manifests/env.
e2e_collect_setup_diagnostics() {
  local directory namespace
  directory="$(e2e_diag_dir)"
  mkdir -p "${directory}"
  for namespace in mcp-platform mcp-observability mcp-runtime; do
    e2e_diag_capture "workloads" "${directory}/setup-${namespace}-workloads.json" \
      get deployment,statefulset,daemonset,replicaset,pod -n "${namespace}" -o json
    e2e_diag_capture "warning-events" "${directory}/setup-${namespace}-warning-events.json" \
      get events -n "${namespace}" --field-selector "type=Warning" -o json
  done
  e2e_diag_capture "runtime-init-logs" "${directory}/setup-runtime-init.log" \
    logs -n mcp-platform -l app=mcp-runtime-api --all-containers=true --prefix=true --since=10m --tail=80
}

rollout_status_with_logs() {
  local namespace="$1"
  local kind="$2"
  local name="$3"
  local timeout="$4"
  local server_name="${5:-${name}}"
  local status

  if kubectl rollout status "${kind}/${name}" -n "${namespace}" --timeout="${timeout}"; then
    return 0
  else
    status=$?
  fi

  echo "[debug] rollout failed for ${kind}/${name}; collecting diagnostics" >&2
  e2e_collect_rollout_diagnostics "${namespace}" "${kind}" "${name}" "${server_name}" || true
  return "${status}"
}

wait_for_named_server_ready() {
  local server_name="$1"
  local namespace="${2:-mcp-servers}"
  local tries="${3:-60}"
  local i deployment_ready service_ready ingress_ready phase
  for ((i = 0; i < tries; i++)); do
    deployment_ready="$(kubectl get mcpserver "${server_name}" -n "${namespace}" -o jsonpath='{.status.deploymentReady}' 2>/dev/null || true)"
    service_ready="$(kubectl get mcpserver "${server_name}" -n "${namespace}" -o jsonpath='{.status.serviceReady}' 2>/dev/null || true)"
    ingress_ready="$(kubectl get mcpserver "${server_name}" -n "${namespace}" -o jsonpath='{.status.ingressReady}' 2>/dev/null || true)"
    phase="$(kubectl get mcpserver "${server_name}" -n "${namespace}" -o jsonpath='{.status.phase}' 2>/dev/null || true)"
    if [[ "${deployment_ready}" == "true" && "${service_ready}" == "true" && "${ingress_ready}" == "true" && "${phase}" == "Ready" ]]; then
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for MCPServer ${server_name} to report core readiness and phase Ready" >&2
  e2e_collect_rollout_diagnostics "${namespace}" mcpserver "${server_name}" "${server_name}" || true
  return 1
}

wait_for_deployment_exists() {
  local namespace="$1"
  local name="$2"
  local tries="${3:-60}"
  local i
  for ((i = 0; i < tries; i++)); do
    if kubectl get deployment "${name}" -n "${namespace}" >/dev/null 2>&1; then
      return 0
    fi
    sleep 2
  done
  echo "timed out waiting for deployment ${name} in namespace ${namespace}" >&2
  e2e_collect_rollout_diagnostics "${namespace}" deployment "${name}" "${name}" || true
  return 1
}
