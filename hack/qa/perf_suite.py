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


def kubernetes(*args):
    return json.loads(subprocess.check_output(["kubectl", "--kubeconfig", os.environ["KUBECONFIG"], *args, "-o", "json"], text=True))


def fingerprint(directory):
    nodes = kubernetes("get", "nodes")["items"]
    if not nodes or not all(n.get("spec", {}).get("providerID", "").startswith("kind://") for n in nodes):
        raise ValueError("Performance evidence requires actual Kind nodes")
    topology = sorted([{"capacity": n.get("status", {}).get("capacity", {}), "node_info": n.get("status", {}).get("nodeInfo", {})} for n in nodes], key=lambda n: json.dumps(n, sort_keys=True))
    # Versions/resources are compatibility inputs; ephemeral UIDs and code/image
    # revisions remain provenance so before/after builds can be compared.
    comparable = {"schema": 1, "host_arch": platform.machine(), "host_kernel": platform.release(), "host_cpus": os.cpu_count(),
                  "nodes": topology, "samples": os.environ.get("PERF_SAMPLES", "100"), "concurrency": os.environ.get("PERF_CONCURRENCY", "8"),
                  "levels": os.environ.get("PERF_CONCURRENCY_LEVELS", "1,10,100"), "requests_per_client": os.environ.get("PERF_REQUESTS_PER_CLIENT", "5"),
                  "protocol": os.environ.get("PERF_PROTOCOL", "2025-06-18"), "tool": os.environ.get("PERF_TOOL", "aaa-ping"),
                  "arguments_sha256": digest(json.loads(os.environ.get("PERF_TOOL_ARGS", "{}"))), "auth_label": os.environ.get("PERF_AUTH_LABEL", "unspecified"),
                  "mcp_header_names": sorted(json.loads(os.environ.get("PERF_MCP_HEADERS_JSON", "{}"))), "warmup": 5}
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
        matched = [item for item in messages if item.get("id") == request_id]
        if len(matched) != 1:
            raise ValueError("SSE must contain exactly the matching JSON-RPC response")
        value = matched[0]
    else:
        value = json.loads(raw)
    if value.get("jsonrpc") != "2.0" or value.get("id") != request_id or "error" in value or not isinstance(value.get("result"), dict) or value["result"].get("isError"):
        raise ValueError("MCP HTTP success did not contain a successful matching JSON-RPC result")
    return value["result"]


class MCPClient:
    def __init__(self, url):
        self.http, self.target = connection(url)
        self.headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "Mcp-Protocol-Version": os.environ.get("PERF_PROTOCOL", "2025-06-18")}
        self.headers.update(json.loads(os.environ.get("PERF_MCP_HEADERS_JSON", "{}")))
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
        self.http.request("POST", urllib.parse.urlunsplit(("", "", self.target.path or "/", self.target.query, "")), json.dumps(payload), headers)
        response = self.http.getresponse()
        raw = response.read(16 * 1024 * 1024 + 1)
        if len(raw) > 16 * 1024 * 1024:
            raise ValueError("MCP benchmark response exceeds bounded 16MiB")
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
        return self.rpc("tools/call", {"name": os.environ.get("PERF_TOOL", "aaa-ping"), "arguments": json.loads(os.environ.get("PERF_TOOL_ARGS", "{}"))}, trace=trace)

    def close(self):
        self.http.close()


def distribution(values):
    if not values or any(not isinstance(v, (int, float)) or not math.isfinite(v) or v < 0 for v in values):
        raise ValueError("Missing, negative or non-finite measurements")
    ordered = sorted(values)
    return {"n": len(values), **{"p" + str(q): ordered[int(q / 100 * (len(ordered) - 1))] for q in (50, 95, 99)}}


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


def concurrency(gateway_url):
    levels = [int(value) for value in os.environ.get("PERF_CONCURRENCY_LEVELS", "1,10,100").split(",")]
    maximum = integer("PERF_MAX_CLIENTS", 128, 1000)
    count = integer("PERF_REQUESTS_PER_CLIENT", 5, 100)
    if not levels or sorted(set(levels)) != levels or min(levels) < 1 or max(levels) > maximum or sum(levels) * count > 10000:
        raise ValueError("Concurrency levels must increase, respect PERF_MAX_CLIENTS and at most 10000 timed requests")
    deadline = time.monotonic() + integer("PERF_MAX_SECONDS", 120, 300)
    results = []
    for level in levels:
        barrier = threading.Barrier(level)

        def worker(_):
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
                return latencies, start, time.perf_counter(), "Mcp-Session-Id" in client.headers
            except Exception:
                barrier.abort()
                raise
            finally:
                client.close()

        with concurrent.futures.ThreadPoolExecutor(max_workers=level) as pool:
            rows = list(pool.map(worker, range(level)))
        elapsed = max(row[2] for row in rows) - min(row[1] for row in rows)
        result = {"concurrency": level, "errors": 0, "elapsed_s": elapsed, "throughput_rps": level * count / elapsed,
                  "mcp_session_ids": sum(row[3] for row in rows), **distribution([latency for row in rows for latency in row[0]])}
        results.append(result)
    # An observed plateau is a relative signal, not a claim of absolute capacity.
    gains = [results[i]["throughput_rps"] / results[i - 1]["throughput_rps"] - 1 for i in range(1, len(results))]
    plateau = next((results[i + 1]["concurrency"] for i in range(1, len(gains)) if gains[i] < .10 and gains[i - 1] < .10), None)
    return {"scenario": "S6", "errors": 0, "levels": results, "saturation": {"two_consecutive_throughput_gains_below_pct": 10, "observed_at_concurrency": plateau,
            "status": "plateau-observed" if plateau else "not-observed-within-bounds"}}


def matches_event(event, trace, server):
    payload = event.get("payload", {})
    if isinstance(payload, str):
        payload = json.loads(payload)
    return (event.get("trace_id") == trace and event.get("server") == server["name"] and event.get("namespace") == server["namespace"]
            and payload.get("server_uid") == server["uid"] and payload.get("rpc_method") == "tools/call" and payload.get("status") == 200)


def observability_lag(gateway_url, analytics_url, server):
    count = integer("PERF_LAG_SAMPLES", 5, 20)
    maximum = integer("PERF_LAG_TIMEOUT", 60, 120)
    api, target = connection(analytics_url)
    client = MCPClient(gateway_url)
    lags, traces = [], []
    try:
        client.initialize()
        for _ in range(count):
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
                body = json.loads(answer.read(4 * 1024 * 1024))
                if not isinstance(body.get("events"), list):
                    raise ValueError("Analytics success has no events array")
                if any(matches_event(event, trace, server) for event in body["events"]):
                    lags.append((time.monotonic() - started) * 1000)
                    traces.append(trace)
                    break
                if time.monotonic() - started >= maximum:
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


def compare(current, baseline, threshold):
    validate(current)
    validate(baseline)
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
    return failed, comparisons


def main():
    command, directory = sys.argv[1:3]
    if command == "fingerprint":
        fingerprint(directory)
        return
    environment = json.loads(Path(directory, "environment.json").read_text())
    if command == "run":
        scenario = sys.argv[3]
        server = environment["provenance"]["server"]
        if not server:
            raise ValueError("S5/S6/S7 require PERF_SERVER_NAME and its actual Kind MCPServer identity")
        gateway = os.environ["PERF_GATEWAY_URL"]
        if scenario == "S5":
            result = overhead(gateway, os.environ["PERF_DIRECT_URL"], integer("PERF_SAMPLES", 100, 10000))
        elif scenario == "S6":
            result = concurrency(gateway)
        elif scenario == "S7":
            result = observability_lag(gateway, os.environ["BASE_URL"], server)
        else:
            raise ValueError("Unknown extended scenario")
        validate(result)
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
    validate(current)
    current["compatibility"] = environment["compatibility"]
    current_path.write_text(json.dumps(current, indent=2, allow_nan=False) + "\n")
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
    threshold = float(os.environ.get("PERF_REGRESSION_PCT", "25"))
    if not math.isfinite(threshold) or not 0 < threshold <= 100:
        raise ValueError("PERF_REGRESSION_PCT must be finite and in (0,100]")
    failed, lines = compare(current, json.loads(baseline.read_text()), threshold)
    print("\n".join(lines))
    if failed:
        raise SystemExit(1)


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print("FAIL performance measurement: " + str(error), file=sys.stderr)
        raise SystemExit(1)
