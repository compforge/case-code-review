from pathlib import Path
import json
import subprocess
import tempfile
import unittest
from unittest.mock import patch

from eval.trajectory.measurement_views import source_digests, write_measurements
from eval.trajectory.atif import ATIFTrajectoryLoader, CCRToolUsageMeasurer


class MeasurementViewsTest(unittest.TestCase):
    def test_snapshot_exports_catalog_and_detects_concurrent_recording_changes(self):
        raw = {'schema_version': 'ATIF-v1.8', 'trajectory_id': 'lane', 'session_id': 'lane', 'agent': {'name': 'ccr', 'version': 'test'}, 'steps': [{'step_id': 1, 'source': 'agent', 'message': '', 'llm_call_count': 1, 'extra': {'duration_ms': 0}}]}
        with tempfile.TemporaryDirectory() as directory:
            review = Path(directory) / 'review.jsonl'
            review.write_text('review\n')
            with patch('eval.trajectory.measurement_views.subprocess.run', return_value=subprocess.CompletedProcess([], 0, json.dumps(raw))):
                destination = write_measurements(review)
            result = json.loads(destination.read_text())
            self.assertEqual(result['sources'], source_digests(review))
            self.assertEqual(result['views']['sessions'][0]['session']['id'], 'lane')
            values = {item['name']: item for item in result['views']['global']['values'] if item['measurer_id'] == 'model_usage'}
            self.assertEqual(values['model_call_count']['value'], 1)
            self.assertEqual(values['model_duration_ms']['value'], 0)
            before = destination.read_bytes()
            def mutate(*args, **kwargs):
                review.write_text('changed\n')
                return subprocess.CompletedProcess([], 0, json.dumps(raw))
            with patch('eval.trajectory.measurement_views.subprocess.run', side_effect=mutate):
                with self.assertRaisesRegex(RuntimeError, 'Recording changed'):
                    write_measurements(review)
            self.assertEqual(destination.read_bytes(), before)

    def test_tool_duration_does_not_reuse_parent_model_duration(self):
        raw = {'schema_version': 'ATIF-v1.8', 'trajectory_id': 'lane', 'agent': {'name': 'ccr', 'version': 'test'}, 'steps': [{'step_id': 1, 'source': 'agent', 'message': '', 'extra': {'duration_ms': 1000}, 'tool_calls': [{'tool_call_id': 'a', 'function_name': 'read', 'arguments': {}}, {'tool_call_id': 'b', 'function_name': 'read', 'arguments': {}}], 'observation': {'results': [{'source_call_id': 'a', 'content': '', 'extra': {'duration_ms': 0}}, {'source_call_id': 'b', 'content': ''}]}}]}
        trajectory = ATIFTrajectoryLoader().loads(json.dumps(raw))[0]
        result = CCRToolUsageMeasurer().measure(trajectory).measurements
        self.assertEqual(result['tool_duration_ms'], 0)
        self.assertEqual(result['duration_reported_call_count'], 1)
