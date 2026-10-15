"""Offline coverage for the complete one-second observation budget."""

import subprocess
import unittest
from unittest.mock import patch

from session_revocation import wait_for_revocation


class SessionRevocationTest(unittest.TestCase):
    def test_http_observation_uses_remaining_budget(self):
        def request(command, **kwargs):
            timeout = float(command[command.index("--max-time") + 1])
            if timeout < 0.3:
                return subprocess.CompletedProcess(command, 28, "", "timeout")
            return subprocess.CompletedProcess(command, 0, '{"error":"session_revoked"}\n401', "")

        with patch("session_revocation.subprocess.run", side_effect=request) as run, patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 0.45]):
            elapsed = wait_for_revocation("https://kind/mcp", "/tmp/cert", "kind", "2025-06-18", oauth_token="fixture-token")
        self.assertEqual(elapsed, 0.45)
        command = run.call_args.args[0]
        self.assertEqual(float(command[command.index("--max-time") + 1]), 0.9)
        self.assertIn("Authorization: Bearer fixture-token", command)
        self.assertFalse(any(header.startswith("Mcp-Session-Id:") for header in command))

    def test_late_revocation_does_not_pass_the_one_second_bound(self):
        response = subprocess.CompletedProcess([], 0, '{"error":"session_revoked"}\n401', "")
        with patch("session_revocation.subprocess.run", return_value=response), patch("session_revocation.time.sleep"), patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 1.1, 1.2]):
            with self.assertRaisesRegex(AssertionError, "one second"):
                wait_for_revocation("https://kind/mcp", "/tmp/cert", "kind", "2025-06-18")

    def test_transport_failure_reports_metadata(self):
        response = subprocess.CompletedProcess([], 28, "", "private transport details")
        with patch("session_revocation.subprocess.run", return_value=response), patch("session_revocation.time.sleep"), patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 0.3, 1.1]):
            with self.assertRaisesRegex(AssertionError, "curl exit 28, HTTP unknown") as failure:
                wait_for_revocation("https://kind/mcp", "/tmp/cert", "kind", "2025-06-18", oauth_token="do-not-log")
        self.assertNotIn("do-not-log", str(failure.exception))
        self.assertNotIn("private transport", str(failure.exception))

    def test_other_policy_denial_is_not_revocation(self):
        response = subprocess.CompletedProcess([], 0, '{"error":"tool_not_granted"}\n403', "")
        with patch("session_revocation.subprocess.run", return_value=response), patch("session_revocation.time.sleep"), patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 0.2, 1.1]):
            with self.assertRaisesRegex(AssertionError, "HTTP 403"):
                wait_for_revocation("https://kind/mcp", "/tmp/cert", "kind", "2025-06-18")


if __name__ == "__main__":
    unittest.main()
