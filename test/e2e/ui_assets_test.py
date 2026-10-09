"""Validate UI asset checks against the deployed bundle and prefixed routing."""
import ast
from pathlib import Path
import unittest
import re
import urllib.parse
from urllib.parse import urljoin

HERE = Path(__file__).resolve().parent
STATIC = HERE.parent.parent / "services/ui/static"


class UIAssetsTest(unittest.TestCase):
    def test_harnesses_accept_root_and_prefixed_runtime_config(self):
        html = (STATIC / "index.html").read_text()
        for harness in ("ui_auth_flows.py", "request_flow_routes.py"):
            module = ast.parse((HERE / harness).read_text())
            functions = [node for node in module.body if isinstance(node, ast.FunctionDef)
                         and node.name in ("vite_asset", "check_vite_assets")]
            for prefix in ("/", "/platform/"):
                with self.subTest(harness=harness, prefix=prefix):
                    base = "http://127.0.0.1:18080" + prefix
                    seen = []

                    def check(condition, success, failure):
                        self.assertTrue(condition, failure)

                    def expect_status(url, status, contains=None):
                        self.assertEqual(status, 200)
                        self.assertTrue(url.startswith(base + "assets/"), url)
                        body = (STATIC / "assets" / url.rsplit("/", 1)[1]).read_text()
                        self.assertIn(contains, body)
                        seen.append(url)
                        return body

                    namespace = {"re": re, "urllib": urllib,
                                 "check": check, "expect_status": expect_status}
                    exec(compile(ast.Module(body=functions, type_ignores=[]), harness, "exec"), namespace)
                    scripts, styles = namespace["check_vite_assets"](base, "ui", html)
                    self.assertEqual(seen, [urljoin(base, scripts), urljoin(base, styles)])
                    self.assertEqual(len(seen), 2)


if __name__ == "__main__":
    unittest.main()
