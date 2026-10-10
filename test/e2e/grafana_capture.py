"""Capture read-only Grafana/Prometheus range queries from an explicitly named Kind cluster."""
import argparse
import base64
import json
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
    "container_cpu_cores": 'sum by(namespace,pod)(rate(container_cpu_usage_seconds_total{container!="",container!="POD"}[2m]))',
    "container_working_set_bytes": 'sum by(namespace,pod)(container_memory_working_set_bytes{container!="",container!="POD"})',
    "container_network_receive_bytes_per_second": 'sum by(namespace,pod)(rate(container_network_receive_bytes_total[2m]))',
    "pod_restarts": "kube_pod_container_status_restarts_total",
    "volume_used_bytes": "kubelet_volume_stats_used_bytes",
    "node_filesystem_available_bytes": 'node_filesystem_avail_bytes{fstype!="tmpfs"}',
}


def query(base, authorization, expression, start, end):
    parameters = urllib.parse.urlencode({"query": expression, "start": start, "end": end, "step": "15s", "timeout": "5s"})
    url = base + "/api/datasources/proxy/uid/prometheus/api/v1/query_range?" + parameters
    request = urllib.request.Request(url, headers={"Authorization": authorization})
    with urllib.request.urlopen(request, timeout=8) as response:
        result = json.load(response)
    series = result.get("data", {}).get("result", [])
    return {"expression": expression, "status": result.get("status"),
            "coverage": "series_present" if series else "no_series", "series": series}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--kubeconfig", required=True, type=Path)
    parser.add_argument("--cluster", required=True)
    parser.add_argument("--output", required=True, type=Path)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    report = {"queries": {}, "notes": ["No series is not zero usage. Process metrics omit uninstrumented services.",
                                        "Capture uses Grafana's Prometheus datasource proxy, without modifying dashboards or credentials."]}
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
        report.update(start=start, end=now, step_seconds=15)
        for name, expression in QUERIES.items():
            try:
                report["queries"][name] = query(base, authorization, expression, start, now)
            except (OSError, ValueError, urllib.error.URLError) as error:
                report["queries"][name] = {"expression": expression, "coverage": "query_failed", "error": type(error).__name__}
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
    if report.get("capture_error"):
        lines += ["", "Capture unavailable: " + report["capture_error"]]
    lines += ["", *report["notes"]]
    (args.output / "grafana.md").write_text("\n".join(lines) + "\n")


if __name__ == "__main__":
    main()
