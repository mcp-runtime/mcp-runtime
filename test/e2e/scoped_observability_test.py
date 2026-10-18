import json
import os
import subprocess
import unittest
from pathlib import Path

import http.client
import urllib.error
from unittest import mock

import scoped_observability


class ScopedObservabilityTests(unittest.TestCase):
    def test_gateway_positive_control_requires_the_real_authentication_response(self):
        scoped_observability.validate_gateway_positive_control(
            subprocess.CompletedProcess([], 1, "", "  HTTP/1.1 401 Unauthorized\n"))
        for code, output in ((1, "wget: download timed out"), (1, "HTTP/1.1 404 Not Found\n"),
                             (1, "HTTP/1.1 403 Forbidden\n"), (1, "HTTP/1.1 503 Unavailable\n"),
                             (0, "HTTP/1.1 200 OK\n")):
            with self.subTest(code=code, output=output), self.assertRaises(AssertionError):
                scoped_observability.validate_gateway_positive_control(subprocess.CompletedProcess([], code, "", output))

    def test_network_probe_requires_an_uncredentialed_managed_workload(self):
        def probe(labels=None, token=False, phase="Running"):
            return {"metadata": {"labels": labels or {}},
                    "spec": {"automountServiceAccountToken": token}, "status": {"phase": phase}}
        managed = {"app.kubernetes.io/managed-by": "mcp-runtime"}
        scoped_observability.validate_network_probe(probe(managed))
        for value in (probe(), probe({"app.kubernetes.io/managed-by": "other"}),
                      probe(managed, token=True), probe(managed, token=None),
                      probe(managed, phase="Pending")):
            with self.subTest(value=value), self.assertRaises(AssertionError):
                scoped_observability.validate_network_probe(value)

    def test_missing_and_non_numeric_frames_do_not_pass_acceptance(self):
        def frame(kind, values):
            return json.dumps({"results": {"A": {"frames": [{"schema": {"fields": [{"type": kind}]}, "data": {"values": [values]}}]}}})
        for value in (None, "0", True, float("nan"), float("inf")):
            self.assertFalse(scoped_observability.frames_present(frame("number", [value])))
        self.assertFalse(scoped_observability.frames_present(frame("time", [123])))
        self.assertTrue(scoped_observability.frames_present(frame("number", [None, 0])))
        self.assertEqual(scoped_observability.frame_numbers(frame("number", [1, None, 0.5])), [1, 0.5])

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


class ManifestPolicyTests(unittest.TestCase):
    def test_kubectl_stream_and_list_select_the_exact_named_policy(self):
        target = {"kind": "NetworkPolicy", "metadata": {"name": "boundary", "namespace": "observability"}, "spec": {"policyTypes": ["Ingress"]}}
        other = {"kind": "NetworkPolicy", "metadata": {"name": "egress", "namespace": "platform"}, "spec": {"policyTypes": ["Egress"]}}
        stream = json.dumps(other) + "\n" + json.dumps(target) + "\n"
        listing = json.dumps({"kind": "List", "items": [other, target]})
        for output in (stream, listing, json.dumps(target)):
            self.assertEqual(scoped_observability.manifest_policy(output, "observability", "boundary"), target)
        for items in ([], [other], [target, target]):
            with self.assertRaises(AssertionError):
                scoped_observability.manifest_policy(json.dumps({"kind": "List", "items": items}), "observability", "boundary")
        with self.assertRaises(ValueError):
            scoped_observability.manifest_policy(stream + "not-json", "observability", "boundary")


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
