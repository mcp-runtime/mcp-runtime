import json
import os
import subprocess
import unittest
from pathlib import Path

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


if __name__ == "__main__":
    unittest.main()
