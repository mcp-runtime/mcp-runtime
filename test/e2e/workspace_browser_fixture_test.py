"""Offline checks for the private disposable browser input boundary."""
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("workspace_browser_fixture.py")


class BrowserFixtureTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.directory = Path(self.temp.name)
        self.source = self.directory / "demo-credentials-test.json"
        self.source.write_text(json.dumps({"acme": "private-owner-password", "globex": "private-member-password"}))
        self.environment = {
            "PATH": os.environ.get("PATH", ""),
            "STAGING_MT_RUN_ID": "test",
            "STAGING_MT_CONFIG_DIR": str(self.directory),
            "PLATFORM_URL": "https://platform.e2e.mcpruntime.org",
            "GITHUB_SHA": "workflow-ref-sha",
            "WORKSPACE_BROWSER_CANDIDATE": "candidate-sha",
            "MCP_PLATFORM_ADMIN_EMAIL": "admin@example.test",
            "MCP_PLATFORM_ADMIN_PASSWORD": "private-admin-password",
        }

    def run_fixture(self):
        return subprocess.run([sys.executable, str(SCRIPT)], env=self.environment, text=True, capture_output=True, check=False)

    def assert_private_failure(self, response):
        self.assertNotEqual(response.returncode, 0)
        self.assertEqual(response.stdout, "")
        self.assertEqual(response.stderr.strip(), "could not load complete disposable browser fixtures")

    def test_streams_cli_created_roles_without_artifact_files(self):
        response = self.run_fixture()
        self.assertEqual(response.returncode, 0)
        self.assertEqual(response.stderr, "")
        value = json.loads(response.stdout)
        self.assertEqual(value["candidate"], "candidate-sha")
        self.assertEqual(value["roles"]["owner"], {"email": "acme-owner-test@example.com", "password": "private-owner-password", "teamSlug": "acme-test"})
        self.assertEqual(value["roles"]["member"]["teamSlug"], "globex-test")
        self.assertEqual(value["roles"]["admin"]["password"], "private-admin-password")
        self.assertEqual(list(self.directory.iterdir()), [self.source])

    def test_honors_supported_fixture_identity_overrides(self):
        self.environment.update(ACME_EMAIL="custom-owner@example.test", ACME_SLUG="custom-team")
        value = json.loads(self.run_fixture().stdout)
        self.assertEqual(value["roles"]["owner"]["email"], "custom-owner@example.test")
        self.assertEqual(value["roles"]["owner"]["teamSlug"], "custom-team")

    def test_forwards_public_ca_bundle(self):
        certificate = self.directory / "public-ca.pem"
        certificate.write_text("public disposable certificate bundle")
        self.environment["SSL_CERT_FILE"] = str(certificate)
        self.assertEqual(json.loads(self.run_fixture().stdout)["caPEM"], certificate.read_text())

    def test_malformed_private_file_never_echoes_its_contents(self):
        self.source.write_text("private-owner-password invalid JSON")
        self.assert_private_failure(self.run_fixture())

    def test_missing_private_file_reports_only_generic_failure(self):
        self.source.unlink()
        self.assert_private_failure(self.run_fixture())

    def test_incomplete_role_never_emits_partial_json(self):
        self.environment["MCP_PLATFORM_ADMIN_PASSWORD"] = ""
        self.assert_private_failure(self.run_fixture())


if __name__ == "__main__":
    unittest.main()
