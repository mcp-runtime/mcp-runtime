import unittest
from grafana_capture import compare_summary, measured_peak, volume_coverage


class GrafanaCaptureTests(unittest.TestCase):
    def test_zero_is_measured_and_missing_nan_are_not(self):
        self.assertEqual(measured_peak({"series": [{"values": [[1, "0"], [2, "NaN"]]}]}), 0)
        self.assertIsNone(measured_peak({"series": [{"values": [[1, "NaN"]]}]}))
        self.assertIsNone(measured_peak({}))

    def test_same_window_comparison_keeps_missing_values(self):
        report = compare_summary({"node_cpu_cores": {"series": [{"values": [[1, ".25"]]}]}},
                                 {"peak_node_cpu_cores": .5, "peak_node_memory_bytes": 1024})
        self.assertEqual(report["node_cpu_cores"]["difference"], -.25)
        self.assertEqual(report["node_cpu_cores"]["relative_difference"], -.5)
        self.assertEqual(report["node_working_set_bytes"]["coverage"], "missing_data")
        self.assertIsNone(report["node_working_set_bytes"]["prometheus_peak"])

    def test_pvc_capacity_and_du_never_fabricate_history(self):
        report = volume_coverage({}, {"pvc_reservations": [{"namespace": "team", "name": "data", "capacity": "10Gi"}],
                                      "local_du_volume_peak_used_bytes": {"team/data": 42}})["team/data"]
        self.assertEqual(report["coverage"], "missing_data")
        self.assertIsNone(report["prometheus_peak_used_bytes"])
        self.assertEqual(report["local_du_fallback_peak_used_bytes"], 42)
        self.assertEqual(report["provisioned_capacity"], "10Gi")


if __name__ == "__main__":
    unittest.main()
