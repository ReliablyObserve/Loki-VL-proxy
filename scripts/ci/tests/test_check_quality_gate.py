import unittest

from scripts.ci.check_quality_gate import check_component_breakdown


class CheckQualityGateComponentTests(unittest.TestCase):
    def test_missing_head_component_breakdown_fails(self):
        failures = []
        check_component_breakdown(
            failures,
            "loki",
            "Loki compatibility",
            {"components": {"labels": {"passed": 2, "total": 2, "pct": 100.0}}},
            {"components": {}},
        )
        self.assertTrue(any("component breakdown missing" in failure for failure in failures))

    def test_required_component_missing_fails(self):
        failures = []
        check_component_breakdown(
            failures,
            "loki",
            "Loki compatibility",
            {"components": {"labels": {"passed": 2, "total": 2, "pct": 100.0}}},
            {"components": {"labels": {"passed": 2, "total": 2, "pct": 100.0}}},
        )
        self.assertTrue(any("required component missing" in failure for failure in failures))

    def test_component_regression_fails(self):
        failures = []
        check_component_breakdown(
            failures,
            "vl",
            "VictoriaLogs compatibility",
            {
                "components": {
                    "stream_translation": {"passed": 3, "total": 3, "pct": 100.0},
                    "detected_fields": {"passed": 3, "total": 3, "pct": 100.0},
                    "synthetic_labels": {"passed": 3, "total": 3, "pct": 100.0},
                    "index_stats": {"passed": 3, "total": 3, "pct": 100.0},
                    "volume_range": {"passed": 3, "total": 3, "pct": 100.0},
                    "field_values": {"passed": 3, "total": 3, "pct": 100.0},
                }
            },
            {
                "components": {
                    "stream_translation": {"passed": 3, "total": 3, "pct": 100.0},
                    "detected_fields": {"passed": 3, "total": 3, "pct": 100.0},
                    "synthetic_labels": {"passed": 3, "total": 3, "pct": 100.0},
                    "index_stats": {"passed": 3, "total": 3, "pct": 100.0},
                    "volume_range": {"passed": 2, "total": 3, "pct": 66.7},
                    "field_values": {"passed": 3, "total": 3, "pct": 100.0},
                }
            },
        )
        self.assertTrue(any("component volume_range" in failure for failure in failures))


if __name__ == "__main__":
    unittest.main()
