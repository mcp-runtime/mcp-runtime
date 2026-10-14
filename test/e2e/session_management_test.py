"""Offline checks for the platform-backed session editing used by QA E2E."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


class SessionManagementTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        source = Path(__file__).with_name("qa-e2e.sh").read_text()
        cls.helpers = source[source.index("qa_session_update() {"):source.index("# emit_cert_session_deny_reasons exercises")]

    def edit(self, patch):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            session = {
                "name": "adapter-test", "namespace": "team-a",
                "serverRef": {"name": "demo", "namespace": "team-a"},
                "subject": {"humanID": "member", "agentID": "agt_test", "teamID": "team-a"},
                "consentedTrust": "low", "revoked": False,
                "expiresAt": "2099-01-01T00:00:00Z", "policyVersion": "v1",
                "serverUID": "server-uid", "grantName": "delegated", "grantNamespace": "team-b",
            }
            (root / "current.json").write_text(json.dumps(session))
            script = """set -euo pipefail
qa_platform_cli() {
  if [[ "$1 $2 $3" == "access session get" ]]; then
    cat "$WORKDIR/current.json"
  elif [[ "$1 $2 $3" == "access session apply" ]]; then
    cp "$5" "$WORKDIR/applied.json"
  else
    return 1
  fi
}
""" + self.helpers + '\nqa_session_update adapter-test "$PATCH" team-a\n'
            result = subprocess.run(["bash", "-c", script], env={**os.environ, "WORKDIR": directory, "PATCH": json.dumps(patch)}, capture_output=True, text=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            return json.loads((root / "applied.json").read_text())

    def test_trust_update_retains_identity_and_grant_binding(self):
        manifest = self.edit({"consentedTrust": "medium"})
        self.assertEqual(manifest["spec"]["consentedTrust"], "medium")
        self.assertEqual(manifest["spec"]["subject"]["agentID"], "agt_test")
        self.assertFalse(manifest["spec"]["revoked"])
        self.assertEqual(manifest["metadata"]["annotations"], {
            "mcpruntime.org/access-grant-name": "delegated",
            "mcpruntime.org/access-grant-namespace": "team-b",
            "mcpruntime.org/server-uid": "server-uid",
        })

    def test_expiry_update_retains_other_session_fields(self):
        manifest = self.edit({"expiresAt": "2000-01-01T00:00:00Z"})
        self.assertEqual(manifest["spec"]["expiresAt"], "2000-01-01T00:00:00Z")
        self.assertEqual(manifest["spec"]["policyVersion"], "v1")
        self.assertEqual(manifest["spec"]["serverRef"]["namespace"], "team-a")

    def test_expiry_can_be_cleared_without_losing_binding(self):
        manifest = self.edit({"expiresAt": None})
        self.assertIsNone(manifest["spec"]["expiresAt"])
        self.assertEqual(manifest["metadata"]["annotations"]["mcpruntime.org/access-grant-name"], "delegated")


if __name__ == "__main__":
    unittest.main()
