import unittest

from build_hypothesis_dataset import build_case


class BuildHypothesisDatasetTest(unittest.TestCase):
    def test_freezes_generator_output_and_human_delivery_verdict(self):
        record = {
            "id": "example-1",
            "label": "wrong",
            "rationale": "caller validates the input",
            "source": "github:example/repo#1",
            "engine": {
                "session_id": "session-1",
                "tool_version": "v1.13.2",
                "model": "model-a",
                "features": {"hypothesis_review": True},
                "git_head": "deadbeef",
                "hypothesis": {"id": "h-1", "path": "a.go"},
                "assessment": {"support": "supported"},
                "trial": {"passed_trial": False},
            },
        }

        case = build_case(record)

        self.assertFalse(case["expected_delivery"])
        self.assertEqual(case["hypothesis"]["id"], "h-1")
        self.assertEqual(case["previous_assessment"]["support"], "supported")
        self.assertFalse(case["previous_trial"]["passed_trial"])
        self.assertEqual(case["engine"]["tool_version"], "v1.13.2")

    def test_skips_legacy_record_without_hypothesis_artifact(self):
        self.assertIsNone(build_case({"id": "legacy", "label": "wrong"}))


if __name__ == "__main__":
    unittest.main()
