"""Capture read-only Grafana/Prometheus range queries from an explicitly named Kind cluster."""
import argparse
import base64
import json
import math
import socket
import subprocess
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

QUERIES = {
    "target_health": "up",
    "required_targets_missing": "mcp:scrape_required_target:info unless on(job) up",
    "uninstrumented_workloads": "mcp:scrape_uninstrumented_workload:info",
    "process_cpu_cores": "sum by(job)(rate(process_cpu_seconds_total[2m]))",
    "process_resident_memory_bytes": "sum by(job)(process_resident_memory_bytes)",
    "go_goroutines": "sum by(job)(go_goroutines)",
    "gateway_requests_per_second": "sum by(namespace,server)(rate(mcp_gateway_requests_total[2m]))",
    "gateway_p95_seconds": "histogram_quantile(0.95,sum by(le,namespace,server)(rate(mcp_gateway_request_duration_seconds_bucket[2m])))",
    "resource_collection_success": "mcp_resource_collection_success",
    "node_collection_success": "mcp_resource_node_collection_success",
    "node_cpu_cores": "sum(mcp_resource_node_cpu_cores)",
    "node_working_set_bytes": "sum(mcp_resource_node_working_set_bytes)",
    "container_cpu_cores": 'sum by(namespace,pod)(mcp_resource_container_cpu_cores)',
    "container_working_set_bytes": 'sum by(namespace,pod)(mcp_resource_container_working_set_bytes)',
    "container_rootfs_used_bytes": 'sum by(namespace,pod)(mcp_resource_container_rootfs_used_bytes)',
    "container_logs_used_bytes": 'sum by(namespace,pod)(mcp_resource_container_logs_used_bytes)',
    "container_network_receive_bytes_per_second": 'sum by(namespace,pod)(rate(mcp_resource_pod_network_receive_bytes_total[2m]))',
    "node_network_receive_bytes_per_second": 'rate(mcp_resource_node_network_receive_bytes_total[2m])',
    "pod_restarts": "mcp_resource_container_restarts_total",
    "cpu_requests_cores": "sum(mcp_resource_pod_cpu_requests_cores)",
    "memory_requests_bytes": "sum(mcp_resource_pod_memory_requests_bytes)",
    "volume_used_bytes": "mcp_resource_volume_used_bytes",
    "pvc_requested_bytes": "mcp_resource_pvc_requested_bytes",
    "pvc_capacity_bytes": "mcp_resource_pvc_capacity_bytes",
    "node_filesystem_used_bytes": "max(mcp_resource_node_filesystem_used_bytes)",
    "node_filesystem_available_bytes": "mcp_resource_node_filesystem_available_bytes",
    "resource_collection_duration_seconds": "mcp_resource_collection_duration_seconds",
}


def measured_peak(result):
    values = []
    for series in result.get("series", []):
        for _, value in series.get("values", []):
            try:
                number = float(value)
                if math.isfinite(number):
                    values.append(number)
            except (TypeError, ValueError):
                pass
    return max(values) if values else None


def compare_summary(queries, summary):
    comparisons = {}
    for name, reference in {
        "node_cpu_cores": "peak_node_cpu_cores",
        "node_working_set_bytes": "peak_node_memory_bytes",
        "node_filesystem_used_bytes": "peak_node_filesystem_used_bytes",
        "cpu_requests_cores": "peak_cpu_requests_cores",
        "memory_requests_bytes": "peak_memory_requests_bytes",
    }.items():
        measured, sampled = measured_peak(queries.get(name, {})), summary.get(reference)
        row = {"prometheus_peak": measured, "sampler_peak": sampled,
               "coverage": "compared" if measured is not None and sampled is not None else "missing_data"}
        if row["coverage"] == "compared":
            row["difference"] = measured - sampled
            row["relative_difference"] = (measured - sampled) / sampled if sampled else None
        comparisons[name] = row
    return comparisons


def volume_coverage(queries, summary):
    measured = {}
    for series in queries.get("volume_used_bytes", {}).get("series", []):
        labels = series.get("metric", {})
        key = labels.get("namespace", "") + "/" + labels.get("persistentvolumeclaim", "")
        value = measured_peak({"series": [series]})
        if value is not None:
            measured[key] = max(value, measured.get(key, 0))
    reports = {}
    for claim in summary.get("pvc_reservations", []):
        key = claim["namespace"] + "/" + claim["name"]
        reports[key] = {"coverage": "series_present" if key in measured else "missing_data",
                        "prometheus_peak_used_bytes": measured.get(key),
                        "kubelet_sampler_peak_used_bytes": summary.get("volume_peak_used_bytes", {}).get(key),
                        "local_du_fallback_peak_used_bytes": summary.get("local_du_volume_peak_used_bytes", {}).get(key),
                        "requested_reservation": claim.get("requested"),
                        "provisioned_capacity": claim.get("capacity")}
    return reports


def query(base, authorization, expression, start, end):
    parameters = urllib.parse.urlencode({"query": expression, "start": start, "end": end, "step": "15s", "timeout": "5s"})
    url = base + "/api/datasources/proxy/uid/prometheus/api/v1/query_range?" + parameters
    request = urllib.request.Request(url, headers={"Authorization": authorization})
    with urllib.request.urlopen(request, timeout=8) as response:
        result = json.load(response)
    series = result.get("data", {}).get("result", [])
    captured = {"expression": expression, "status": result.get("status"), "series": series}
    captured["coverage"] = "series_present" if measured_peak(captured) is not None else "no_series"
    return captured


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True, type=Path)
    parser.add_argument("--cluster", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    report = {"queries": {}, "notes": ["No series is not zero usage. Process metrics omit uninstrumented services.",
                                        "Capture uses Grafana's admin Prometheus datasource proxy, without modifying dashboards or credentials.",
                                        "Resource history is collected every 30 seconds from authenticated, TLS-verified kubelet summaries. Peaks can miss shorter bursts and pre-collector startup.",
                                        "PVC capacity/requested bytes are reservations, never actual consumption. Missing driver volume stats stay missing; local du samples remain a separate fallback.",
                                        "Collector wall time is reported separately; runtime process CPU/memory include other API work and are not attributed entirely to collection.",
                                        "Prometheus and local sampler peaks use the same test window but different sample times; their difference is diagnostic, not a sizing pass threshold."]}
    forward = None
    try:
        kube = ["kubectl", "--kubeconfig", str(args.kubeconfig), "--request-timeout=5s"]
        context = subprocess.check_output(kube + ["config", "current-context"], text=True, stderr=subprocess.DEVNULL, timeout=6).strip()
        if context != "kind-" + args.cluster:
            raise ValueError("Grafana capture requires the named Kind context")
        secret = json.loads(subprocess.check_output(kube + ["get", "secret", "mcp-grafana-credentials", "-n", "mcp-observability", "-o", "json"],
                                                    text=True, stderr=subprocess.DEVNULL, timeout=6))["data"]
        user = base64.b64decode(secret["GRAFANA_ADMIN_USER"]).decode()
        password = base64.b64decode(secret["GRAFANA_ADMIN_PASSWORD"]).decode()
        authorization = "Basic " + base64.b64encode((user + ":" + password).encode()).decode()
        # Bind only loopback. Port selection can race; a failed forward is reported.
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", 0))
            port = listener.getsockname()[1]
        forward = subprocess.Popen(kube + ["port-forward", "-n", "mcp-observability", "svc/grafana", f"{port}:3000", "--address=127.0.0.1"],
                                   stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        base = f"http://127.0.0.1:{port}/grafana"
        for _ in range(20):
            try:
                with urllib.request.urlopen(base + "/api/health", timeout=1) as response:
                    report["grafana"] = json.load(response)
                break
            except (OSError, urllib.error.URLError):
                if forward.poll() is not None:
                    raise RuntimeError("Grafana port-forward exited")
                time.sleep(.5)
        else:
            raise RuntimeError("Grafana did not become reachable")
        now = time.time()
        summary_path = args.output / "summary.json"
        summary = json.loads(summary_path.read_text()) if summary_path.exists() else {}
        start = summary.get("start") or now - 600
        end = summary.get("end") or now
        report.update(start=start, end=end, step_seconds=15)
        for name, expression in QUERIES.items():
            try:
                report["queries"][name] = query(base, authorization, expression, start, end)
            except (OSError, ValueError, urllib.error.URLError) as error:
                report["queries"][name] = {"expression": expression, "coverage": "query_failed", "error": type(error).__name__}
        report["kubelet_comparison"] = compare_summary(report["queries"], summary)
        report["pvc_volume_coverage"] = volume_coverage(report["queries"], summary)
    except (OSError, ValueError, KeyError, RuntimeError, subprocess.SubprocessError) as error:
        # Do not write exceptions containing HTTP headers, credentials or Secret bodies.
        report["capture_error"] = type(error).__name__
    finally:
        if forward:
            forward.terminate()
            try:
                forward.wait(timeout=3)
            except subprocess.TimeoutExpired:
                forward.kill()
                forward.wait()
    (args.output / "grafana.json").write_text(json.dumps(report, indent=2) + "\n")
    lines = ["# Grafana capture coverage", "", "| Query | Coverage |", "|---|---|"]
    lines += [f"| {name} | {value['coverage']} |" for name, value in report["queries"].items()]
    lines += ["", "| Same-window quantity | Prometheus sampled peak | Local sampler peak | Coverage |", "|---|---|---|---|"]
    lines += [f"| {name} | {row['prometheus_peak']} | {row['sampler_peak']} | {row['coverage']} |"
              for name, row in report.get("kubelet_comparison", {}).items()]
    lines += ["", "| PVC | Historical consumed bytes | Local du fallback | Coverage |", "|---|---|---|---|"]
    lines += [f"| {name} | {row['prometheus_peak_used_bytes']} | {row['local_du_fallback_peak_used_bytes']} | {row['coverage']} |"
              for name, row in report.get("pvc_volume_coverage", {}).items()]
    if report.get("capture_error"):
        lines += ["", "Capture unavailable: " + report["capture_error"]]
    lines += ["", *report["notes"]]
    (args.output / "grafana.md").write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
