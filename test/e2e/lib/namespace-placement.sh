# shellcheck shell=bash
# Shared clean-install placement contract for QA E2E and Staging E2E.

namespace_placement_workloads() {
  cat <<'EOF'
deployment|mcp-runtime|mcp-runtime-operator-controller-manager
deployment|mcp-platform|mcp-platform-api
deployment|mcp-platform|mcp-runtime-api
deployment|mcp-platform|mcp-ui
deployment|mcp-platform|mcp-platform-gateway
statefulset|mcp-platform|mcp-postgres
deployment|mcp-observability|mcp-analytics-api
deployment|mcp-observability|mcp-ingest
deployment|mcp-observability|mcp-processor
statefulset|mcp-observability|clickhouse
statefulset|mcp-observability|kafka
daemonset|mcp-log-collector|promtail
EOF
}

namespace_placement_secrets() {
  cat <<'EOF'
mcp-platform|mcp-platform-api-credentials
mcp-platform|mcp-runtime-api-credentials
mcp-platform|mcp-ui-credentials
mcp-platform|mcp-postgres-credentials
mcp-observability|mcp-analytics-api-credentials
mcp-observability|mcp-ingest-credentials
mcp-observability|mcp-grafana-credentials
EOF
}

namespace_placement_configmaps() {
  cat <<'EOF'
mcp-platform|mcp-shared-config
mcp-observability|mcp-shared-config
EOF
}

# kind|namespace|name|env|value
namespace_placement_env() {
  cat <<'EOF'
deployment|mcp-platform|mcp-ui|API_UPSTREAM|http://mcp-platform-api.mcp-platform.svc.cluster.local:8080
deployment|mcp-platform|mcp-ui|RUNTIME_UPSTREAM|http://mcp-runtime-api.mcp-platform.svc.cluster.local:8084
deployment|mcp-platform|mcp-ui|ANALYTICS_UPSTREAM|http://mcp-analytics-api.mcp-observability.svc.cluster.local:8085
deployment|mcp-platform|mcp-platform-api|RUNTIME_CONTROL_URL|http://mcp-runtime-api.mcp-platform.svc.cluster.local:8084
deployment|mcp-platform|mcp-runtime-api|PLATFORM_API_URL|http://mcp-platform-api.mcp-platform.svc.cluster.local:8080
deployment|mcp-platform|mcp-runtime-api|CLICKHOUSE_ADDR|clickhouse.mcp-observability.svc.cluster.local:9000
deployment|mcp-observability|mcp-analytics-api|PLATFORM_API_URL|http://mcp-platform-api.mcp-platform.svc.cluster.local:8080
EOF
}

namespace_placement_fail() {
  printf '%s\n' "$*" >&2
}

namespace_placement_verify() {
  local failed=0 kind ns name key want got args traefik_ns
  while IFS='|' read -r kind ns name; do
    [[ -z "${kind}" ]] && continue
    if ! kubectl get "${kind}" "${name}" -n "${ns}" >/dev/null 2>&1; then
      namespace_placement_fail "missing ${kind} ${ns}/${name}"
      failed=1
    fi
  done < <(namespace_placement_workloads)

  while IFS='|' read -r ns name; do
    [[ -z "${ns}" ]] && continue
    if ! kubectl get secret "${name}" -n "${ns}" >/dev/null 2>&1; then
      namespace_placement_fail "missing secret ${ns}/${name}"
      failed=1
    fi
  done < <(namespace_placement_secrets)

  while IFS='|' read -r ns name; do
    [[ -z "${ns}" ]] && continue
    if ! kubectl get configmap "${name}" -n "${ns}" >/dev/null 2>&1; then
      namespace_placement_fail "missing configmap ${ns}/${name}"
      failed=1
    fi
  done < <(namespace_placement_configmaps)


  if kubectl get secret mcp-platform-tls -n mcp-observability >/dev/null 2>&1; then
    namespace_placement_fail "mcp-platform-tls must stay in mcp-platform, not mcp-observability"
    failed=1
  fi

  while IFS='|' read -r kind ns name key want; do
    [[ -z "${kind}" ]] && continue
    got="$(kubectl get "${kind}" "${name}" -n "${ns}" -o jsonpath="{.spec.template.spec.containers[0].env[?(@.name==\"${key}\")].value}" 2>/dev/null || true)"
    if [[ "${got}" != "${want}" ]]; then
      namespace_placement_fail "env ${ns}/${name} ${key} is '${got}', want '${want}'"
      failed=1
    fi
  done < <(namespace_placement_env)

  args=""
  for traefik_ns in traefik kube-system; do
    if args="$(kubectl get deployment traefik -n "${traefik_ns}" -o jsonpath='{.spec.template.spec.containers[0].args}' 2>/dev/null)" && [[ -n "${args}" ]]; then
      break
    fi
    args=""
  done
  if [[ -n "${args}" ]]; then
    # Only a namespace-scoped Traefik needs these names. k3s's bundled Traefik
    # in kube-system has no --providers.*.namespaces flag and watches all
    # namespaces, so there is no list to check.
    if [[ "${args}" == *".namespaces="* ]]; then
      for name in mcp-platform mcp-observability mcp-servers; do
        if [[ "${args}" != *"${name}"* ]]; then
          namespace_placement_fail "Traefik watch list is missing ${name}: ${args}"
          failed=1
        fi
      done
    fi
  fi

  [[ "${failed}" -eq 0 ]]
}
