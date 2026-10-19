"""Offline performance-contract tests; no cluster or real benchmark baseline."""
import copy
import importlib.util
import json
import os
import tempfile
import threading
import time
import unittest
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path
from unittest.mock import patch

ROOT = Path(__file__).resolve().parents[2]
SPEC = importlib.util.spec_from_file_location("perf", ROOT / "hack/qa/perf_suite.py")
perf = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(perf)


def measurement(values=(1., 2., 3.)):
    return {"scenario": "S1", "errors": 0, "compatibility": "same", "samples_ms": list(values), **perf.distribution(values)}


class Fixture(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    trace = ""

    def log_message(self, *_):
        pass

    def answer(self, value, status=200, headers=None):
        data = json.dumps(value).encode() if value is not None else b""
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        for key, val in (headers or {}).items():
            self.send_header(key, val)
        self.end_headers()
        self.wfile.write(data)

    def do_POST(self):
        body = json.loads(self.rfile.read(int(self.headers["Content-Length"])))
        if "id" not in body:
            self.answer(None, 202)
        elif body["method"] == "initialize":
            self.answer({"jsonrpc": "2.0", "id": body["id"], "result": {"protocolVersion": body["params"]["protocolVersion"]}}, headers={"Mcp-Session-Id": uuid.uuid4().hex})
        else:
            trace = self.headers.get("traceparent", "").split("-")
            if len(trace) == 4:
                Fixture.trace = trace[1]
            self.answer({"jsonrpc": "2.0", "id": body["id"], "result": {"content": [{"type": "text", "text": "pong"}]}}, headers={"X-Trace-ID": Fixture.trace})

    def do_GET(self):
        if "/events?" in self.path:
            self.answer({"events": [{"trace_id": Fixture.trace, "event_type": "mcp.request", "server": "server", "namespace": "team",
                                     "payload": {"server_uid": "current", "rpc_method": "tools/call", "status": 200}}]})
        else:
            self.answer({"totals": {}, "window_days": 7})


class PerfTests(unittest.TestCase):
    def setUp(self):
        self.server = ThreadingHTTPServer(("127.0.0.1", 0), Fixture)
        self.server.daemon_threads = True
        thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        thread.start()
        self.addCleanup(self.server.server_close)
        self.addCleanup(self.server.shutdown)
        self.url = "http://127.0.0.1:" + str(self.server.server_port)

    def test_rpc_http_success_must_be_real_matching_success(self):
        for value in ({}, [], {"jsonrpc": "2.0", "id": 9, "result": {}}, {"jsonrpc": "2.0", "id": True, "result": {}},
                      {"jsonrpc": "2.0", "id": 1, "error": {}}, {"jsonrpc": "2.0", "id": 1, "result": {"isError": True}}):
            with self.subTest(value=value), self.assertRaises(ValueError):
                perf.decode_rpc(json.dumps(value).encode(), "application/json", 1)
        data = b'data: {"jsonrpc":"2.0","method":"notification"}\n\ndata: {"jsonrpc":"2.0","id":1,"result":{"content":[]}}\n\n'
        self.assertEqual(perf.decode_rpc(data, "text/event-stream", 1), {"content": []})

    def test_loopback_and_real_kind_are_both_required(self):
        for url in ("https://platform.mcpruntime.org/mcp", "http://user@localhost/mcp", "file:///mcp", "http://localhost/#token"):
            with self.assertRaises(ValueError):
                perf.loopback(url)
        with patch.dict(os.environ, {"KUBECONFIG": "/private/test"}), patch.object(perf.subprocess, "check_output", return_value="production"), patch.object(perf, "kubernetes") as kube:
            with self.assertRaises(ValueError):
                perf.guard_cluster()
            kube.assert_not_called()
        with patch.dict(os.environ, {"KUBECONFIG": "/private/test"}), patch.object(perf.subprocess, "check_output", return_value="kind-renamed"), patch.object(perf, "kubernetes", return_value={"items": [{"spec": {"providerID": "aws:///foreign"}}]}):
            with self.assertRaises(ValueError):
                perf.guard_cluster()

    def test_bad_samples_and_fingerprints_fail_comparison(self):
        reference = measurement()
        for changes in ({"errors": 1}, {"errors": False}, {"p95": float("nan")}, {"p95": 99}, {"n": 100}, {"samples_ms": []}, {"compatibility": "other"}):
            with self.subTest(changes=changes), self.assertRaises(ValueError):
                perf.compare({**reference, **changes}, reference, 25)
        self.assertTrue(perf.compare(measurement((2., 4., 6.)), reference, 25)[0])
        self.assertFalse(perf.compare(reference, reference, 25)[0])
        with self.assertRaises(ValueError):
            perf.compare(reference, reference, float("nan"))

    def test_rpc_closes_response_before_reusing_connection(self):
        client = perf.MCPClient(self.url)
        self.addCleanup(client.close)
        _, _, response = client.rpc("initialize", {"protocolVersion": client.headers["Mcp-Protocol-Version"]})
        self.assertTrue(response.isclosed())
        client.rpc("notifications/initialized", notification=True)

    def test_real_local_http_measurements_have_exact_counts(self):
        result = perf.adapter_latency(self.url, 3)
        perf.validate_measurement(result)
        self.assertEqual(result["n"], 3)
        result = perf.overhead(self.url, self.url, 3)
        perf.validate_measurement(result)
        self.assertEqual(result["gateway"]["n"], result["direct"]["n"])
        row = perf.concurrent_level(self.url, [2, 2, 1], time.monotonic() + 10)
        perf.validate_measurement({"scenario": "S2", **row})
        self.assertEqual((row["n"], row["concurrency"], row["mcp_session_ids"]), (5, 3, 3))
        with patch.dict(os.environ, {"PERF_API_KEY": "synthetic"}):
            perf.validate_measurement(perf.analytics_latency(self.url, 2))

    def test_overhead_regression_is_not_hidden_by_faster_direct_upstream(self):
        def paired(gateway, direct):
            gateway_stats, direct_stats = perf.distribution([gateway] * 3), perf.distribution([direct] * 3)
            return {"scenario": "S5", "errors": 0, "compatibility": "same", **gateway_stats,
                    "gateway": gateway_stats, "direct": direct_stats,
                    "samples_ms": {"gateway": [gateway] * 3, "direct": [direct] * 3},
                    "added_latency_ms": {key: gateway - direct for key in ("p50", "p95", "p99")}}
        baseline = paired(2., 1.)
        failed, lines = perf.compare(paired(1.5, .1), baseline, 25)
        self.assertTrue(failed)
        self.assertTrue(any("added_latency_ms.p95" in line and "REGRESSION" in line for line in lines))
        self.assertFalse(perf.compare(paired(.5, 1.), baseline, 25)[0])
        for gateway in (1., .5):
            with self.assertRaisesRegex(ValueError, "zero/negative"):
                perf.compare(baseline, paired(gateway, 1.), 25)

    def test_trace_lag_requires_exact_trace_namespace_and_live_uid(self):
        target = {"name": "server", "namespace": "team", "uid": "current"}
        with patch.dict(os.environ, {"PERF_API_KEY": "synthetic", "PERF_LAG_SAMPLES": "1"}):
            result = perf.observability_lag(self.url, self.url, target)
        perf.validate_measurement(result)
        event = {"trace_id": "a" * 32, "event_type": "mcp.request", "server": "server", "namespace": "team", "payload": {"server_uid": "current", "rpc_method": "tools/call", "status": 200}}
        self.assertTrue(perf.matches_event(event, "a" * 32, target))
        for change in ({"namespace": "foreign"}, {"trace_id": "b" * 32}, {"payload": {"server_uid": "old", "rpc_method": "tools/call", "status": 200}}):
            self.assertFalse(perf.matches_event({**event, **change}, "a" * 32, target))

    def test_invalid_or_excessive_concurrency_never_sends_requests(self):
        for levels in ("0", "10,1", "1,1", "1001", "1,500,1000"):
            with patch.dict(os.environ, {"PERF_CONCURRENCY_LEVELS": levels}), patch.object(perf, "MCPClient") as client, self.assertRaises(ValueError):
                perf.concurrency(self.url)
            client.assert_not_called()

    def test_body_limit_and_total_deadline(self):
        class Response:
            fp = None
            def read1(self, _):
                return b"more-data"
            def close(self):
                pass
        with self.assertRaises(ValueError):
            perf.bounded_body(Response(), 3, time.monotonic() + 1)
        with self.assertRaises(TimeoutError):
            perf.bounded_body(Response(), 100, time.monotonic() - 1)

    def test_baseline_requires_explicit_creation_and_preserves_existing(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            comparable = {"schema": 1, "nodes": ["synthetic-offline-fixture"]}
            environment = {"comparable": comparable, "compatibility": perf.digest(comparable)}
            (path / "environment.json").write_text(json.dumps(environment))
            (path / "S1.json").write_text(json.dumps({**measurement(), "compatibility": environment["compatibility"]}))
            baseline = path / "baseline"
            argv = ["perf", "compare", str(path), str(baseline), "S1"]
            with patch.object(perf.sys, "argv", argv), patch.dict(os.environ, {"PERF_RECORD_BASELINE": "0"}), self.assertRaises(ValueError):
                perf.main()
            with patch.object(perf.sys, "argv", argv), patch.dict(os.environ, {"PERF_RECORD_BASELINE": "1"}):
                perf.main()
                original = (baseline / "S1.json").read_bytes()
                perf.main()
                self.assertEqual((baseline / "S1.json").read_bytes(), original)
            changed = json.loads((path / "S1.json").read_text())
            changed["compatibility"] = "other"
            (path / "S1.json").write_text(json.dumps(changed))
            with patch.object(perf.sys, "argv", argv), self.assertRaises(ValueError):
                perf.main()
            self.assertEqual((baseline / "S1.json").read_bytes(), original)

    def test_fingerprint_ignores_ephemeral_node_ids_but_tracks_resources(self):
        node = {"metadata": {"uid": "first"}, "status": {"nodeInfo": {"bootID": "first", "architecture": "amd64"}, "capacity": {"cpu": "4", "memory": "8Gi", "pods": "110"}}}
        deployment = {"metadata": {"namespace": "test", "name": "api"}, "spec": {"template": {"spec": {"containers": [{"name": "api", "image": "image:before", "resources": {"requests": {"cpu": "1"}}}]}}}}
        with tempfile.TemporaryDirectory() as directory, patch.object(perf, "guard_cluster", side_effect=lambda: [node]), patch.object(perf, "kubernetes", return_value={"items": [deployment]}), patch.object(perf.subprocess, "check_output", return_value="commit"), patch.dict(os.environ, {"PERF_MCP_HEADERS_JSON": '{"Authorization":"private-value"}'}):
            perf.fingerprint(directory)
            first = json.loads(Path(directory, "environment.json").read_text())
            node["metadata"]["uid"], node["status"]["nodeInfo"]["bootID"] = "second", "second"
            deployment["spec"]["template"]["spec"]["containers"][0]["image"] = "image:after"
            perf.fingerprint(directory)
            second = json.loads(Path(directory, "environment.json").read_text())
            self.assertEqual(first["compatibility"], second["compatibility"])
            self.assertNotIn("private-value", Path(directory, "environment.json").read_text())
            node["status"]["capacity"]["cpu"] = "8"
            perf.fingerprint(directory)
            self.assertNotEqual(second["compatibility"], json.loads(Path(directory, "environment.json").read_text())["compatibility"])


if __name__ == "__main__":
    unittest.main()
