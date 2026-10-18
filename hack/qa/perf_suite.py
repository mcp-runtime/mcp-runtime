"""Bounded Kind performance measurements; the shell owns cluster/URL guards."""
import concurrent.futures
import hashlib
import http.client
import json
import math
import os
import platform
import secrets
import ssl
import subprocess
import sys
import threading
import time
import urllib.parse
from pathlib import Path


def integer(name, default, maximum):
    value = int(os.environ.get(name, default))
    if not 1 <= value <= maximum:
        raise ValueError(f"{name} must be between 1 and {maximum}")
    return value


def digest(value):
    return hashlib.sha256(json.dumps(value, sort_keys=True, separators=(",", ":")).encode()).hexdigest()


def configuration():
    integer("PERF_SAMPLES", 100, 10000)
    integer("PERF_CONCURRENCY", 8, 1000)
    integer("PERF_MAX_CLIENTS", 128, 1000)
    integer("PERF_REQUESTS_PER_CLIENT", 5, 100)
    integer("PERF_MAX_SECONDS", 120, 300)
    integer("PERF_LAG_SAMPLES", 5, 20)
    integer("PERF_LAG_TIMEOUT", 60, 120)
    threshold = float(os.environ.get("PERF_REGRESSION_PCT", "25"))
    if not math.isfinite(threshold) or not 0 < threshold <= 100:
        raise ValueError("PERF_REGRESSION_PCT must be finite and in (0,100]")
    arguments = os.environ.get("PERF_TOOL_ARGS", "{}")
    if len(arguments) > 65536 or not isinstance(json.loads(arguments), dict):
        raise ValueError("PERF_TOOL_ARGS must be a JSON object of at most 64KiB")


def kubernetes(*args):
    return json.loads(subprocess.check_output(["kubectl", "--kubeconfig", os.environ["KUBECONFIG"], *args, "-o", "json"], text=True))


def guard_cluster():
    context = subprocess.check_output(["kubectl", "--kubeconfig", os.environ["KUBECONFIG"], "config", "current-context"], text=True).strip()
    if not context.startswith("kind-") and context != "test-mcp-runtime":
        raise ValueError("Live performance measurements require an explicit Kind context")
    nodes = kubernetes("get", "nodes")["items"]
    if not nodes or not all(n.get("spec", {}).get("providerID", "").startswith("kind://") for n in nodes):
        raise ValueError("Performance evidence requires actual Kind nodes")
    return nodes


def fingerprint(directory):
    configuration()
    nodes = guard_cluster()
    topology = sorted([{"capacity": {k: n.get("status", {}).get("capacity", {}).get(k) for k in ("cpu", "memory", "pods")},
                        "node_info": {k: n.get("status", {}).get("nodeInfo", {}).get(k) for k in ("kernelVersion", "osImage", "containerRuntimeVersion", "kubeletVersion", "operatingSystem", "architecture")}}
                       for n in nodes], key=lambda n: json.dumps(n, sort_keys=True))
    # Versions/resources are compatibility inputs; ephemeral UIDs and code/image
    # revisions remain provenance so before/after builds can be compared.
    comparable = {"schema": 1, "host_arch": platform.machine(), "host_kernel": platform.release(), "host_cpus": os.cpu_count(),
                  "nodes": topology, "samples": os.environ.get("PERF_SAMPLES", "100"), "concurrency": os.environ.get("PERF_CONCURRENCY", "8"),
                  "levels": os.environ.get("PERF_CONCURRENCY_LEVELS", "1,10,100"), "requests_per_client": os.environ.get("PERF_REQUESTS_PER_CLIENT", "5"),
                  "protocol": os.environ.get("PERF_PROTOCOL", "2025-06-18"), "tool": os.environ.get("PERF_TOOL", "aaa-ping"),
                  "arguments_sha256": digest(json.loads(os.environ.get("PERF_TOOL_ARGS", "{}"))), "auth_label": os.environ.get("PERF_AUTH_LABEL", "unspecified"),
                  "mcp_header_names": sorted(json.loads(os.environ.get("PERF_MCP_HEADERS_JSON", "{}"))), "warmup": {"sequential": 5, "per_concurrent_client": 1},
                  "lag_samples": os.environ.get("PERF_LAG_SAMPLES", "5"), "lag_timeout": os.environ.get("PERF_LAG_TIMEOUT", "60"), "poll_ms": 500,
                  "affinity_cpus": len(os.sched_getaffinity(0)) if hasattr(os, "sched_getaffinity") else os.cpu_count()}
    comparable["transports"] = {key: {"scheme": loopback(os.environ[key]).scheme, "path_sha256": digest((loopback(os.environ[key]).path, loopback(os.environ[key]).query))}
                                for key in ("BASE_URL", "ADAPTER_URL", "PERF_GATEWAY_URL", "PERF_DIRECT_URL") if os.environ.get(key)}
    comparable["client_certificate"] = bool(os.environ.get("PERF_CLIENT_CERT"))
    server = None
    if os.environ.get("PERF_SERVER_NAME"):
        obj = kubernetes("get", "mcpserver", os.environ["PERF_SERVER_NAME"], "-n", os.environ.get("PERF_SERVER_NAMESPACE", "mcp-servers"))
        server = {key: obj["metadata"][key] for key in ("name", "namespace", "uid")}
        comparable["upstream_image"] = obj["spec"].get("image")
        comparable["server_policy"] = obj["spec"].get("policy")
    provenance = {"commit": subprocess.check_output(["git", "rev-parse", "HEAD"], text=True).strip(), "recorded_unix": time.time(), "server": server,
                  "node_uids": [n.get("metadata", {}).get("uid") for n in nodes], "implementation": os.environ.get("PERF_IMPLEMENTATION", "current-gateway")}
    deployments = kubernetes("get", "deployments", "-A").get("items", [])
    provenance["images"] = [{"namespace": d["metadata"]["namespace"], "name": d["metadata"]["name"],
                             "containers": [{"name": c["name"], "image": c["image"], "resources": c.get("resources", {})} for c in d["spec"]["template"]["spec"].get("containers", [])]}
                            for d in deployments]
    comparable["workload_resources"] = sorted([{**d, "containers": [{k: c[k] for k in ("name", "resources")} for c in d["containers"]]} for d in provenance["images"]], key=lambda d: (d["namespace"], d["name"]))
    result = {"compatibility": digest(comparable), "comparable": comparable, "provenance": provenance}
    Path(directory, "environment.json").write_text(json.dumps(result, indent=2) + "\n")


def loopback(url):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme not in ("http", "https") or parsed.hostname not in ("localhost", "127.0.0.1", "::1") or parsed.username or parsed.password or parsed.fragment:
        raise ValueError("Performance requests require explicit loopback endpoints without userinfo")
    return parsed


def connection(url):
    target = loopback(url)
    if target.scheme == "https":
        context = ssl.create_default_context(cafile=os.environ.get("PERF_CA_FILE") or None)
        if os.environ.get("PERF_CLIENT_CERT"):
            context.load_cert_chain(os.environ["PERF_CLIENT_CERT"], os.environ["PERF_CLIENT_KEY"])
        return http.client.HTTPSConnection(target.hostname, target.port, timeout=10, context=context), target
    return http.client.HTTPConnection(target.hostname, target.port, timeout=10), target


def decode_rpc(raw, content_type, request_id):
    if "text/event-stream" in content_type:
        messages = [json.loads(line[5:].strip()) for line in raw.decode().splitlines() if line.startswith("data:") and line[5:].strip()]
        matched = [item for item in messages if isinstance(item, dict) and item.get("id") == request_id]
        if len(matched) != 1:
            raise ValueError("SSE must contain exactly the matching JSON-RPC response")
        value = matched[0]
    else:
        value = json.loads(raw)
    if not isinstance(value, dict) or value.get("jsonrpc") != "2.0" or type(value.get("id")) is not type(request_id) or value.get("id") != request_id or "error" in value or not isinstance(value.get("result"), dict) or value["result"].get("isError"):
        raise ValueError("MCP HTTP success did not contain a successful matching JSON-RPC result")
    return value["result"]


def bounded_body(response, limit, deadline):
    try:
        chunks, size = [], 0
        while True:
            remaining = deadline - time.monotonic()
            if remaining <= 0:
                raise TimeoutError("Benchmark response exceeded its total deadline")
            raw = getattr(response.fp, "raw", None)
            sock = getattr(raw, "_sock", None)
            if sock is not None:
                sock.settimeout(remaining)
            chunk = response.read1(min(65536, limit - size + 1))
            if not chunk:
                return b"".join(chunks)
            chunks.append(chunk)
            size += len(chunk)
            if size > limit:
                raise ValueError("Benchmark response exceeds its bounded body limit")
    finally:
        # read1 reaching EOF does not close persistent responses on every
        # supported Python. Finish them explicitly before connection reuse.
        response.close()


class MCPClient:
    def __init__(self, url):
        self.http, self.target = connection(url)
        self.headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": os.environ.get("PERF_PROTOCOL", "2025-06-18")}
        extra = json.loads(os.environ.get("PERF_MCP_HEADERS_JSON", "{}"))
        if not isinstance(extra, dict) or any(not isinstance(k, str) or not isinstance(v, str) for k, v in extra.items()):
            raise ValueError("PERF_MCP_HEADERS_JSON must be an object of string headers")
        self.headers.update(extra)
        self.next_id = 0

    def rpc(self, method, params=None, trace=None, notification=False):
        self.next_id += 1
        payload = {"jsonrpc": "2.0", "method": method}
        if params is not None:
            payload["params"] = params
        if not notification:
            payload["id"] = self.next_id
        headers = dict(self.headers)
        if trace:
            headers["traceparent"] = "00-" + trace + "-" + secrets.token_hex(8) + "-01"
        started = time.perf_counter()
        deadline = time.monotonic() + 10
        if self.http.sock:
            self.http.sock.settimeout(10)
        self.http.request("POST", urllib.parse.urlunsplit(("", "", self.target.path or "/", self.target.query, "")), json.dumps(payload), headers)
        response = self.http.getresponse()
        raw = bounded_body(response, 16 * 1024 * 1024, deadline)
        if notification:
            if response.status not in (200, 202, 204):
                raise ValueError("MCP initialized notification failed")
            result = None
        else:
            if response.status != 200:
                raise ValueError(f"MCP request failed with HTTP {response.status}")
            result = decode_rpc(raw, response.getheader("Content-Type", ""), self.next_id)
        return result, (time.perf_counter() - started) * 1000, response

    def initialize(self):
        result, _, response = self.rpc("initialize", {"protocolVersion": self.headers["Mcp-Protocol-Version"], "capabilities": {}, "clientInfo": {"name": "kind-perf-suite", "version": "1"}})
        if result.get("protocolVersion") != self.headers["Mcp-Protocol-Version"]:
            raise ValueError("MCP protocol negotiation differs from measured configuration")
        if response.getheader("Mcp-Session-Id"):
            self.headers["Mcp-Session-Id"] = response.getheader("Mcp-Session-Id")
        self.rpc("notifications/initialized", notification=True)

    def call(self, trace=None):
        result = self.rpc("tools/call", {"name": os.environ.get("PERF_TOOL", "aaa-ping"), "arguments": json.loads(os.environ.get("PERF_TOOL_ARGS", "{}"))}, trace=trace)
        if not isinstance(result[0].get("content"), list) and not isinstance(result[0].get("structuredContent"), dict):
            raise ValueError("Successful tools/call has neither content nor structuredContent")
        return result

    def close(self):
        self.http.close()


def distribution(values):
    if not values or any(not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0 for v in values):
        raise ValueError("Missing, negative or non-finite measurements")
    ordered = sorted(values)
    return {"n": len(values), **{"p" + str(q): ordered[int(q / 100 * (len(ordered) - 1))] for q in (50, 95, 99)}}


def adapter_latency(url, samples):
    client = MCPClient(url)
    try:
        client.initialize()
        for _ in range(5):
            client.call()
        values = [client.call()[1] for _ in range(samples)]
        return {"scenario": "S1", "errors": 0, **distribution(values), "samples_ms": values}
    finally:
        client.close()


def analytics_latency(base, samples):
    api, target = connection(base)
    values = []
    try:
        for index in range(samples + 5):
            started = time.perf_counter()
            api.request("GET", target.path.rstrip("/") + "/api/v1/analytics/usage?limit=50", headers={"X-API-Key": os.environ["PERF_API_KEY"]})
            answer = api.getresponse()
            if answer.status != 200:
                raise ValueError(f"Analytics request failed with HTTP {answer.status}")
            body = json.loads(bounded_body(answer, 4 * 1024 * 1024, time.monotonic() + 10))
            if not isinstance(body, dict) or "error" in body or not isinstance(body.get("totals"), dict) or type(body.get("window_days")) is not int or body["window_days"] <= 0:
                raise ValueError("Analytics success has no valid usage totals/window")
            if index >= 5:
                values.append((time.perf_counter() - started) * 1000)
        return {"scenario": "S3", "errors": 0, **distribution(values), "samples_ms": values}
    finally:
        api.close()


def overhead(gateway_url, direct_url, samples):
    gateway, direct = MCPClient(gateway_url), MCPClient(direct_url)
    try:
        gateway.initialize()
        direct.initialize()
        for _ in range(5):
            gateway.call()
            direct.call()
        measurements = {"gateway": [], "direct": []}
        for index in range(samples):
            outputs = {}
            # Alternate order to reduce warm-up/drift bias; keep both raw sets.
            for name, client in (("gateway", gateway), ("direct", direct)) if index % 2 == 0 else (("direct", direct), ("gateway", gateway)):
                result, latency, _ = client.call()
                measurements[name].append(latency)
                outputs[name] = result
            if outputs["gateway"] != outputs["direct"]:
                raise ValueError("Gateway and direct fixture tools returned different results; no comparable overhead")
        result = {"scenario": "S5", "errors": 0, **distribution(measurements["gateway"]), "gateway": distribution(measurements["gateway"]), "direct": distribution(measurements["direct"])}
        result["added_latency_ms"] = {key: result["gateway"][key] - result["direct"][key] for key in ("p50", "p95", "p99")}
        result["samples_ms"] = measurements
        return result
    finally:
        gateway.close()
        direct.close()


def concurrent_level(gateway_url, counts, deadline):
    level = len(counts)
    barrier = threading.Barrier(level)

    def worker(count):
        client = MCPClient(gateway_url)
        try:
            client.initialize()
            client.call()
            barrier.wait(timeout=20)
            start = time.perf_counter()
            latencies = []
            for _ in range(count):
                if time.monotonic() >= deadline:
                    raise ValueError("Concurrency measurement exceeded its bounded wall budget")
                latencies.append(client.call()[1])
            return latencies, start, time.perf_counter(), client.headers.get("Mcp-Session-Id")
        except Exception:
            barrier.abort()
            raise
        finally:
            client.close()

    with concurrent.futures.ThreadPoolExecutor(max_workers=level) as pool:
        rows = list(pool.map(worker, counts))
    elapsed = max(row[2] for row in rows) - min(row[1] for row in rows)
    sessions = [row[3] for row in rows if row[3]]
    if sessions and len(set(sessions)) != level:
        raise ValueError("MCP server did not issue a distinct session to every concurrent client")
    latencies = [latency for row in rows for latency in row[0]]
    return {"concurrency": level, "errors": 0, "elapsed_s": elapsed, "throughput_rps": sum(counts) / elapsed,
            "mcp_session_ids": len(sessions), **distribution(latencies), "samples_ms": latencies}


def concurrency(gateway_url):
    levels = [int(value) for value in os.environ.get("PERF_CONCURRENCY_LEVELS", "1,10,100").split(",")]
    maximum = integer("PERF_MAX_CLIENTS", 128, 1000)
    count = integer("PERF_REQUESTS_PER_CLIENT", 5, 100)
    if not levels or len(levels) > 16 or sorted(set(levels)) != levels or min(levels) < 1 or max(levels) > maximum or sum(levels) * count > 10000 or sum(levels) > 2000:
        raise ValueError("Concurrency levels must increase, respect PERF_MAX_CLIENTS and at most 10000 timed requests/2000 initialized clients")
    deadline = time.monotonic() + integer("PERF_MAX_SECONDS", 120, 300)
    results = [concurrent_level(gateway_url, [count] * level, deadline) for level in levels]
    # An observed plateau is a relative signal, not a claim of absolute capacity.
    gains = [results[i]["throughput_rps"] / results[i - 1]["throughput_rps"] - 1 for i in range(1, len(results))]
    plateau = next((results[i + 1]["concurrency"] for i in range(1, len(gains)) if gains[i] < .10 and gains[i - 1] < .10), None)
    return {"scenario": "S6", "errors": 0, "levels": results, "saturation": {"two_consecutive_throughput_gains_below_pct": 10, "observed_at_concurrency": plateau,
            "status": "plateau-observed" if plateau else "not-observed-within-bounds"}}


def matches_event(event, trace, server):
    payload = event.get("payload", {})
    if isinstance(payload, str):
        payload = json.loads(payload)
    return (isinstance(payload, dict) and event.get("trace_id") == trace and event.get("event_type") == "mcp.request" and event.get("server") == server["name"] and event.get("namespace") == server["namespace"]
            and payload.get("server_uid") == server["uid"] and payload.get("rpc_method") == "tools/call" and payload.get("status") == 200)


def observability_lag(gateway_url, analytics_url, server):
    count = integer("PERF_LAG_SAMPLES", 5, 20)
    maximum = integer("PERF_LAG_TIMEOUT", 60, 120)
    api, target = connection(analytics_url)
    client = MCPClient(gateway_url)
    lags, traces = [], []
    deadline = time.monotonic() + integer("PERF_MAX_SECONDS", 120, 300)
    try:
        client.initialize()
        for _ in range(count):
            if time.monotonic() >= deadline:
                raise TimeoutError("Observability measurement exceeded its batch wall budget")
            trace = secrets.token_hex(16)
            started = time.monotonic()
            _, _, response = client.call(trace)
            if response.getheader("X-Trace-ID") != trace:
                raise ValueError("Gateway did not return the exact submitted trace ID")
            query = urllib.parse.urlencode({"trace_id": trace, "server": server["name"], "namespace": server["namespace"], "limit": 100})
            while True:
                api.request("GET", target.path.rstrip("/") + "/api/v1/events?" + query, headers={"X-API-Key": os.environ["PERF_API_KEY"]})
                answer = api.getresponse()
                if answer.status != 200:
                    raise ValueError(f"Analytics poll failed with HTTP {answer.status}")
                body = json.loads(bounded_body(answer, 4 * 1024 * 1024, min(deadline, time.monotonic() + 10)))
                if not isinstance(body.get("events"), list):
                    raise ValueError("Analytics success has no events array")
                if any(matches_event(event, trace, server) for event in body["events"]):
                    lags.append((time.monotonic() - started) * 1000)
                    traces.append(trace)
                    break
                if time.monotonic() - started >= maximum or time.monotonic() >= deadline:
                    raise ValueError("Exact trace/current-server audit event did not traverse the pipeline within its deadline")
                time.sleep(.5)
        return {"scenario": "S7", "errors": 0, **distribution(lags), "lag_ms": lags, "trace_ids": traces, "poll_interval_ms": 500}
    finally:
        api.close()
        client.close()


def validate(value):
    if isinstance(value, dict):
        if value.get("errors", 0) != 0:
            raise ValueError("Failed requests cannot be a performance baseline")
        for child in value.values():
            validate(child)
    elif isinstance(value, list):
        for child in value:
            validate(child)
    elif isinstance(value, float) and not math.isfinite(value):
        raise ValueError("Non-finite performance metric")


def validate_measurement(value):
    validate(value)
    if value.get("scenario") not in ("S1", "S2", "S3", "S5", "S6", "S7") or type(value.get("errors")) is not int or value["errors"] != 0:
        raise ValueError("Missing scenario or successful-request evidence")

    def stats(row, samples):
        if not isinstance(samples, list) or row.get("n") != len(samples) or type(row.get("n")) is not int:
            raise ValueError("Missing or inconsistent sample count")
        expected = distribution(samples)
        if any(type(v) is bool for v in samples) or any(type(row.get(key)) not in (int, float) or row.get(key) != expected[key] for key in ("p50", "p95", "p99")):
            raise ValueError("Percentiles do not match the actual samples")
        if row.get("concurrency") is not None:
            concurrency = row["concurrency"]
            if type(concurrency) is not int or not 1 <= concurrency <= 1000 or row["n"] < concurrency:
                raise ValueError("Invalid concurrency coverage")
            elapsed = row.get("elapsed_s", 0)
            throughput = row.get("throughput_rps", 0)
            if type(elapsed) not in (int, float) or elapsed <= 0 or type(throughput) not in (int, float) or throughput <= 0 or not math.isclose(throughput, row["n"] / elapsed, rel_tol=1e-9):
                raise ValueError("Throughput does not match completed requests/wall time")
            if row.get("mcp_session_ids") not in (0, concurrency):
                raise ValueError("Incomplete MCP session coverage")

    if value["scenario"] == "S6":
        levels = value.get("levels")
        if not isinstance(levels, list) or not levels or len(levels) > 16:
            raise ValueError("Missing concurrency levels")
        previous = 0
        for row in levels:
            if type(row.get("errors")) is not int or row["errors"] != 0:
                raise ValueError("Missing successful-request evidence for a concurrency level")
            stats(row, row.get("samples_ms"))
            if row["concurrency"] <= previous:
                raise ValueError("Concurrency levels must increase")
            previous = row["concurrency"]
    elif value["scenario"] == "S5":
        for name in ("gateway", "direct"):
            stats(value[name], value.get("samples_ms", {}).get(name))
        if value["gateway"]["n"] != value["direct"]["n"] or any(value.get(k) != value["gateway"][k] for k in ("n", "p50", "p95", "p99")):
            raise ValueError("Unpaired gateway/direct measurements")
        if any(value.get("added_latency_ms", {}).get(k) != value["gateway"][k] - value["direct"][k] for k in ("p50", "p95", "p99")):
            raise ValueError("Incorrect gateway overhead")
    elif value["scenario"] == "S7":
        stats(value, value.get("lag_ms"))
        traces = value.get("trace_ids", [])
        if len(traces) != value["n"] or len(set(traces)) != len(traces) or any(not isinstance(t, str) or len(t) != 32 or any(c not in "0123456789abcdef" for c in t) or t == "0" * 32 for t in traces):
            raise ValueError("Missing unique trace correlation evidence")
    else:
        stats(value, value.get("samples_ms"))


def compare(current, baseline, threshold):
    if not math.isfinite(threshold) or not 0 < threshold <= 100:
        raise ValueError("Comparison threshold must be finite and in (0,100]")
    validate_measurement(current)
    validate_measurement(baseline)
    if current.get("scenario") != baseline.get("scenario") or not current.get("compatibility") or current["compatibility"] != baseline.get("compatibility"):
        raise ValueError("Baseline environment/traffic fingerprint differs or is missing; record a deliberate matching baseline")
    current_rows = current.get("levels", [current])
    baseline_rows = baseline.get("levels", [baseline])
    if len(current_rows) != len(baseline_rows):
        raise ValueError("Baseline concurrency coverage differs")
    failed, comparisons = False, []
    for candidate, reference in zip(current_rows, baseline_rows):
        if candidate.get("concurrency") != reference.get("concurrency"):
            raise ValueError("Baseline concurrency levels differ")
        if candidate["n"] != reference["n"]:
            raise ValueError("Baseline sample coverage differs")
        for name in ("p95", "p99", "throughput_rps"):
            if (name in candidate) != (name in reference):
                raise ValueError("Missing baseline/candidate metric " + name)
            if name not in candidate:
                continue
            before, after = reference[name], candidate[name]
            if not isinstance(before, (int, float)) or not isinstance(after, (int, float)) or before <= 0 or after < 0:
                raise ValueError("Invalid comparison metric " + name)
            delta = (after / before - 1) * 100
            worse = delta < -threshold if name == "throughput_rps" else delta > threshold
            failed |= worse
            comparisons.append(f"{name} concurrency={candidate.get('concurrency', 1)}: {before:.3f} -> {after:.3f} ({delta:+.1f}%)" + (" REGRESSION" if worse else ""))
    if not comparisons:
        raise ValueError("No comparable performance metrics")
    if current["scenario"] == "S5":
        for name in ("p95", "p99"):
            before = baseline["added_latency_ms"][name]
            after = current["added_latency_ms"][name]
            if before <= 0:
                raise ValueError("S5 baseline " + name + " overhead is zero/negative; timing noise has no relative denominator. Retain raw evidence and deliberately recapture a stable positive-overhead baseline")
            delta = (after / before - 1) * 100
            worse = delta > threshold
            failed |= worse
            comparisons.append(f"added_latency_ms.{name}: {before:.3f} -> {after:.3f} ({delta:+.1f}%)" + (" REGRESSION" if worse else ""))
    return failed, comparisons


def main():
    command, directory = sys.argv[1:3]
    if command == "fingerprint":
        fingerprint(directory)
        return
    environment = json.loads(Path(directory, "environment.json").read_text())
    if environment.get("compatibility") != digest(environment.get("comparable")):
        raise ValueError("Environment fingerprint does not match its recorded configuration")
    if command == "run":
        configuration()
        guard_cluster()
        scenario = sys.argv[3]
        server = environment["provenance"]["server"]
        if scenario in ("S5", "S6", "S7") and not server:
            raise ValueError("S5/S6/S7 require PERF_SERVER_NAME and its actual Kind MCPServer identity")
        if scenario in ("S5", "S6", "S7") and not os.environ.get("PERF_AUTH_LABEL", "").strip():
            raise ValueError("S5/S6/S7 require a non-secret PERF_AUTH_LABEL describing the same authentication fixture")
        gateway = os.environ["PERF_GATEWAY_URL"]
        samples = integer("PERF_SAMPLES", 100, 10000)
        if scenario == "S1":
            result = adapter_latency(os.environ["ADAPTER_URL"], samples)
        elif scenario == "S2":
            count = integer("PERF_CONCURRENCY", 8, integer("PERF_MAX_CLIENTS", 128, 1000))
            if samples < count:
                raise ValueError("PERF_SAMPLES must cover every concurrent client")
            counts = [samples // count + (index < samples % count) for index in range(count)]
            result = {"scenario": "S2", **concurrent_level(os.environ["ADAPTER_URL"], counts, time.monotonic() + integer("PERF_MAX_SECONDS", 120, 300))}
        elif scenario == "S3":
            result = analytics_latency(os.environ["BASE_URL"], samples)
        elif scenario == "S5":
            result = overhead(gateway, os.environ["PERF_DIRECT_URL"], integer("PERF_SAMPLES", 100, 10000))
        elif scenario == "S6":
            result = concurrency(gateway)
        elif scenario == "S7":
            result = observability_lag(gateway, os.environ["BASE_URL"], server)
        else:
            raise ValueError("Unknown extended scenario")
        validate_measurement(result)
        result["compatibility"] = environment["compatibility"]
        Path(directory, scenario + ".json").write_text(json.dumps(result, indent=2, allow_nan=False) + "\n")
        print(json.dumps(result, allow_nan=False))
        return
    if command != "compare":
        raise ValueError("Unknown performance command")
    baseline_dir, scenario = Path(sys.argv[3]), sys.argv[4]
    current_path = Path(directory, scenario + ".json")
    current = json.loads(current_path.read_text())
    if current.get("scenario") != scenario:
        raise ValueError("Measurement has the wrong scenario")
    validate_measurement(current)
    if current.get("compatibility") != environment["compatibility"]:
        raise ValueError("Measurement belongs to another environment/traffic fingerprint")
    threshold = float(os.environ.get("PERF_REGRESSION_PCT", "25"))
    if not math.isfinite(threshold) or not 0 < threshold <= 100:
        raise ValueError("PERF_REGRESSION_PCT must be finite and in (0,100]")
    baseline = baseline_dir / (scenario + ".json")
    if not baseline.exists():
        if os.environ.get("PERF_RECORD_BASELINE") != "1":
            raise ValueError("Missing baseline; use PERF_RECORD_BASELINE=1 deliberately")
        baseline_dir.mkdir(parents=True, exist_ok=True)
        with baseline.open("x") as output:
            output.write(current_path.read_text())
        # Provenance is explicit per scenario and never silently replaced.
        with (baseline_dir / (scenario + "-environment.json")).open("x") as output:
            json.dump(environment, output, indent=2)
        print(scenario + ": recorded new baseline, not regression acceptance")
        return
    recorded_environment = json.loads((baseline_dir / (scenario + "-environment.json")).read_text())
    recorded = json.loads(baseline.read_text())
    if recorded_environment.get("compatibility") != digest(recorded_environment.get("comparable")) or recorded_environment["compatibility"] != recorded.get("compatibility"):
        raise ValueError("Baseline has no matching environment evidence")
    failed, lines = compare(current, recorded, threshold)
    print("\n".join(lines))
    if failed:
        raise SystemExit(1)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL performance measurement: " + str(error), file=sys.stderr)
        raise SystemExit(1)
