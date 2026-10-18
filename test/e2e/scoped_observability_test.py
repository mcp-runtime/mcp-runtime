import json
import os
import subprocess
import unittest
from pathlib import Path

import http.client
import urllib.error
from unittest import mock

import scoped_observability
from scoped_observability import frame_numbers, frames_present


class ScopedObservabilityTests(unittest.TestCase):
    def test_missing_and_non_numeric_frames_do_not_pass_acceptance(self):
        def frame(kind, values):
            return json.dumps({"results": {"A": {"frames": [{"schema": {"fields": [{"type": kind}]}, "data": {"values": [values]}}]}}})
        for value in (None, "0", True, float("nan"), float("inf")):
            self.assertFalse(frames_present(frame("number", [value])))
        self.assertFalse(frames_present(frame("time", [123])))
        self.assertTrue(frames_present(frame("number", [None, 0])))
        self.assertEqual(frame_numbers(frame("number", [1, None, 0.5])), [1, 0.5])

    def test_fresh_cluster_and_registry_guard_precedes_all_mutation(self):
        script = Path(__file__).parent / "lib/cilium.sh"
        body = '''
set -eu
source "$SCOPED_SCRIPT"
helm() { :; }
kind() { [[ "$*" == 'get clusters' ]] || exit 99; printf '%s\\n' "$EXISTING_CLUSTERS"; }
docker() { [[ "$*" == 'ps -a --format {{.Names}}' ]] || exit 99; printf '%s\\n' "$EXISTING_CONTAINERS"; }
scoped_cilium_guard
'''
        baseline = {**os.environ, "SCOPED_SCRIPT": str(script), "CLUSTER_NAME": "mcp-scope-e2e-unit", "E2E_CACHE_MODE": "0",
                    "LOCAL_REGISTRY_NAME": "mcp-scope-e2e-unit-mirror", "EXISTING_CLUSTERS": "other-cluster", "EXISTING_CONTAINERS": "unrelated"}
        cases = [({}, True), ({"CLUSTER_NAME": "production"}, False), ({"E2E_CACHE_MODE": "1"}, False),
                 ({"LOCAL_REGISTRY_NAME": "foreign-registry"}, False), ({"EXISTING_CLUSTERS": "mcp-scope-e2e-unit"}, False),
                 ({"EXISTING_CONTAINERS": "mcp-scope-e2e-unit-mirror"}, False)]
        for overrides, expected in cases:
            with self.subTest(overrides=overrides):
                result = subprocess.run(["bash", "-c", body], env={**baseline, **overrides}, capture_output=True, text=True)
                self.assertEqual(result.returncode == 0, expected, result.stderr)


class GrafanaQueryTests(unittest.TestCase):
    def test_public_origin_is_independent_of_loopback_transport(self):
        with mock.patch.dict(os.environ, {"SCOPED_UI_BASE": "http://127.0.0.1:18080", "SCOPED_UI_ORIGIN": "http://localhost:18080"}):
            client = mock.MagicMock()
            response = client.open.return_value.__enter__.return_value
            response.status, response.headers = 200, {}
            response.read.return_value = b'{"results":{"A":{"frames":[]}}}'
            scoped_observability.query(client, 'rate(mcp_gateway_requests_total[5m])')
            sent = client.open.call_args.args[0]
            self.assertEqual(sent.full_url, "http://127.0.0.1:18080/grafana/api/ds/query")
            self.assertEqual(sent.get_header("Origin"), "http://localhost:18080")

    def test_query_errors_fail_immediately_instead_of_waiting_for_series(self):
        for status, body in ((403, b'{"error":"csrf_failed"}'), (200, b'{"results":{"A":{"error":"backend failed"}}}')):
            with self.subTest(status=status), self.assertRaises(AssertionError):
                scoped_observability.query_has_series(status, body)
        self.assertFalse(scoped_observability.query_has_series(200, b'{"results":{"A":{"frames":[]}}}'))


class MCPOriginTests(unittest.TestCase):
    def test_gateway_mcp_uses_the_tls_origin_when_ui_stays_on_http(self):
        with mock.patch.dict(os.environ, {"SCOPED_UI_BASE": "http://127.0.0.1:18080", "SCOPED_MCP_BASE": "https://127.0.0.1:18443"}):
            self.assertEqual(scoped_observability.mcp_origin(), "https://127.0.0.1:18443")

    def test_gateway_mcp_follows_the_ui_origin_without_a_tls_override(self):
        env = os.environ.copy()
        env.pop("SCOPED_MCP_BASE", None)
        env["SCOPED_UI_BASE"] = "http://127.0.0.1:18080"
        with mock.patch.dict(os.environ, env, clear=True):
            self.assertEqual(scoped_observability.mcp_origin(), "http://127.0.0.1:18080")


class RequestRetryTests(unittest.TestCase):
    class Response:
        status = 200
        headers = {}

        def __enter__(self):
            return self

        def __exit__(self, *exc):
            return False

        def read(self):
            return b"{}"

    def client(self, failures, error):
        calls = {"count": 0}

        def open_(req, timeout=None):
            calls["count"] += 1
            if calls["count"] <= failures:
                raise error
            return self.Response()

        return mock.Mock(open=open_), calls

    def run_request(self, client, method, **kwargs):
        with mock.patch.object(scoped_observability.time, "sleep"):
            return scoped_observability.request(client, "http://example.test/x", method, **kwargs)

    def test_safe_requests_retry_a_dropped_connection(self):
        client, calls = self.client(2, http.client.RemoteDisconnected("closed"))
        self.assertEqual(self.run_request(client, "GET")[0], 200)
        self.assertEqual(calls["count"], 3)

    def test_unsafe_requests_do_not_retry_unless_asked(self):
        client, calls = self.client(1, http.client.RemoteDisconnected("closed"))
        with self.assertRaises(http.client.RemoteDisconnected):
            self.run_request(client, "POST", body={"a": 1})
        self.assertEqual(calls["count"], 1)
        client, calls = self.client(1, http.client.RemoteDisconnected("closed"))
        self.assertEqual(self.run_request(client, "POST", body={"a": 1}, retry_connection=True)[0], 200)
        self.assertEqual(calls["count"], 2)

    def test_urllib_wrapped_connection_errors_are_retried(self):
        # urllib raises URLError(reason=ConnectionRefusedError) for a refused
        # connection, not the bare exception.
        client, calls = self.client(2, urllib.error.URLError(ConnectionRefusedError(111, "refused")))
        self.assertEqual(self.run_request(client, "GET")[0], 200)
        self.assertEqual(calls["count"], 3)

    def test_urllib_errors_that_are_not_connection_drops_are_not_retried(self):
        client, calls = self.client(1, urllib.error.URLError(OSError("name resolution failed")))
        with self.assertRaises(urllib.error.URLError):
            self.run_request(client, "GET")
        self.assertEqual(calls["count"], 1)

    def test_persistent_connection_failure_still_fails(self):
        client, calls = self.client(99, http.client.RemoteDisconnected("closed"))
        with self.assertRaises(http.client.RemoteDisconnected):
            self.run_request(client, "GET")
        self.assertEqual(calls["count"], scoped_observability.CONNECTION_ATTEMPTS)

    def test_http_errors_are_not_retried_as_connection_failures(self):
        error = urllib.error.HTTPError("http://example.test/x", 503, "unavailable", {}, None)
        client, calls = self.client(1, error)
        with mock.patch.object(error, "read", return_value=b"x"):
            status = self.run_request(client, "GET")[0]
        self.assertEqual(status, 503)
        self.assertEqual(calls["count"], 1)


if __name__ == "__main__":
    unittest.main()
