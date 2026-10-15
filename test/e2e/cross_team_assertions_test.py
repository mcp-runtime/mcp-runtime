"""Offline coverage for live cross-team E2E evidence and revocation checks."""

import subprocess
import unittest
from unittest.mock import patch

from cross_team_assertions import assert_attribution, wait_for_revocation


def event(tool, decision):
    return {"server": "delegated", "session_id": "session-b", "tool_name": tool, "decision": decision, "subject_team_id": "team-b", "resource_team_id": "team-a"}


class CrossTeamAssertionsTest(unittest.TestCase):
    def test_requires_both_allow_and_ungranted_deny_with_distinct_teams(self):
        assert_attribution({"events": [event("aaa-ping", "allow"), event("echo", "deny")]}, "delegated", "session-b", "team-b", "team-a")

    def test_authority_team_cannot_stand_in_for_subject_team(self):
        wrong = event("aaa-ping", "allow")
        wrong["subject_team_id"] = "team-a"
        with self.assertRaisesRegex(AssertionError, "attribution"):
            assert_attribution({"events": [wrong, event("echo", "deny")]}, "delegated", "session-b", "team-b", "team-a")

    def test_other_server_or_session_does_not_satisfy_the_journey(self):
        wrong = event("aaa-ping", "allow")
        wrong["session_id"] = "unrelated-session"
        with self.assertRaisesRegex(AssertionError, "decisions"):
            assert_attribution({"events": [wrong, event("echo", "deny")]}, "delegated", "session-b", "team-b", "team-a")

    def test_revocation_requires_http_401_and_session_revoked(self):
        response = subprocess.CompletedProcess([], 0, '{"error":"session_revoked"}\n401', "")
        with patch("session_revocation.subprocess.run", return_value=response) as run, patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 0.2]):
            elapsed = wait_for_revocation("https://kind/mcp", "/tmp/test-credentials", "kind", "2025-06-18", "mcp-session")
        self.assertEqual(elapsed, 0.2)
        command = run.call_args.args[0]
        self.assertIn("--cert", command)
        self.assertIn("Mcp-Session-Id: mcp-session", command)
        self.assertNotIn("Authorization", " ".join(command))

    def test_ungranted_tool_denial_is_not_revocation(self):
        response = subprocess.CompletedProcess([], 0, '{"error":"tool_not_granted"}\n403', "")
        with patch("session_revocation.subprocess.run", return_value=response), patch("session_revocation.time.sleep"), patch("session_revocation.time.monotonic", side_effect=[0, 0.1, 0.2, 1.1]):
            with self.assertRaisesRegex(AssertionError, "one second"):
                wait_for_revocation("https://kind/mcp", "/tmp/test-credentials", "kind", "2025-06-18", "mcp-session")


if __name__ == "__main__":
    unittest.main()
