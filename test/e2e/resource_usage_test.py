import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

from resource_usage import Sampler, pod_requests, quantity, summarize, write_summary


class ResourceUsageTests(unittest.TestCase):
    def test_quantities(self):
        self.assertEqual(quantity("250m"), .25)
        self.assertEqual(quantity("500000000n"), .5)
        self.assertEqual(quantity("2Gi"), 2 * 1024**3)

    def test_init_sidecars_and_pod_overhead(self):
        def container(cpu, **extra):
            return {"resources": {"requests": {"cpu": cpu}}, **extra}
        pod = {"spec": {"containers": [container("100m")],
                        "initContainers": [container("200m", restartPolicy="Always"), container("500m")],
                        "overhead": {"cpu": "50m"}}}
        self.assertAlmostEqual(pod_requests(pod, "cpu"), .75)

    def test_missing_samples_are_not_zero_usage(self):
        report = summarize([{"time": 1, "errors": [{"source": "kubernetes"}]}], "single", "smoke-auth", 1)
        self.assertIsNone(report["peak_node_cpu_cores"])
        self.assertEqual(report["e2e_exit_status"], 1)
        self.assertEqual(report["error_samples"], 1)

    def test_host_and_vm_peaks_before_cluster_exists(self):
        samples = [{"time": 1, "host": {"cpu_used_cores": 7, "swap_used_bytes": 100},
                    "colima_memory": "Mem: 6000 5000 1000", "errors": []},
                   {"time": 2, "host": {"cpu_used_cores": 1},
                    "nodes": [{"usage": {}, "pods": []}], "errors": []}]
        report = summarize(samples, "single", "smoke-auth", 0)
        self.assertEqual(report["peak_host_cpu_cores"], 7)
        self.assertEqual(report["peak_host_swap_bytes"], 100)
        self.assertEqual(report["peak_colima_memory_used_bytes"], 5000)

    def test_ambient_production_context_is_never_queried(self):
        calls = []
        def command(argv, **_):
            calls.append(argv)
            if argv[-2:] == ["config", "current-context"]:
                return "prod-mcp-runtime"
            return ""
        with patch("resource_usage.host_sample", return_value={}), patch("resource_usage.run", side_effect=command):
            sample = Sampler(Path("/explicit/kind-config"), "test").sample("build")
        self.assertNotIn("nodes", sample)
        self.assertFalse(any("get" in call for call in calls))

    def test_truncated_final_sample_preserves_report(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory)
            (path / "samples.jsonl").write_text('{"time":1,"errors":[]}\n{"time":')
            write_summary(path, "single", "smoke-auth", 1)
            self.assertTrue((path / "summary.md").exists())
            self.assertTrue((path / "samples.csv").exists())


if __name__ == "__main__":
    unittest.main()
