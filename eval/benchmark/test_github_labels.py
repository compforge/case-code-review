from __future__ import annotations

import argparse
import json
import subprocess
import tempfile
import unittest
from datetime import datetime
from pathlib import Path
from unittest.mock import patch

import eval.benchmark.github_labels as github_labels


class GitHubLabelsTest(unittest.TestCase):
    @patch("eval.benchmark.github_labels.subprocess.run")
    def test_discovers_and_deduplicates_merged_pull_requests(self, run) -> None:
        run.side_effect = [
            subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout=json.dumps(
                    [
                        {
                            "repository": {"nameWithOwner": "example/one"},
                            "number": 7,
                            "url": "https://example.test/one/7",
                        }
                    ]
                ),
                stderr="",
            ),
            subprocess.CompletedProcess(
                args=[],
                returncode=0,
                stdout=json.dumps(
                    [
                        {
                            "repository": {"nameWithOwner": "example/one"},
                            "number": 7,
                            "url": "https://example.test/one/7",
                        },
                        {
                            "repository": {"nameWithOwner": "other/two"},
                            "number": 3,
                            "url": "https://example.test/two/3",
                        },
                    ]
                ),
                stderr="",
            ),
        ]

        pull_requests = github_labels.discover_pull_requests(
            ["example", "other"],
            "@me",
            datetime.fromisoformat("2026-08-17T00:00:00+08:00"),
            datetime.fromisoformat("2026-08-24T00:00:00+08:00"),
            100,
        )

        self.assertEqual(
            [(item.repository, item.number) for item in pull_requests],
            [("example/one", 7), ("other/two", 3)],
        )
        command = run.call_args_list[0].args[0]
        self.assertIn(
            "2026-08-17T00:00:00+08:00..2026-08-23T23:59:59+08:00",
            command,
        )

    def test_timestamp_requires_timezone(self) -> None:
        with self.assertRaisesRegex(argparse.ArgumentTypeError, "timezone"):
            github_labels._timestamp("2026-08-17T00:00:00")

    def test_harvests_each_pr_and_upserts_by_repository(self) -> None:
        pull_requests = [
            github_labels.PullRequest("example/one", 7, "https://example.test/one/7"),
            github_labels.PullRequest("example/one", 8, "https://example.test/one/8"),
            github_labels.PullRequest("other/two", 3, "https://example.test/two/3"),
        ]

        def harvest(repository: str, number: int) -> list[dict]:
            return [
                {
                    "source": f"github:{repository}#{number}",
                    "reply_id": number,
                    "label": "minor" if number == 7 else "important",
                    "at": f"2026-08-{number + 10:02d}T00:00:00Z",
                }
            ]

        with tempfile.TemporaryDirectory() as directory:
            summary = github_labels.harvest_pull_requests(
                pull_requests, Path(directory), 1, harvest
            )
            first_repo = Path(directory) / "example-one.jsonl"
            second_repo = Path(directory) / "other-two.jsonl"

            self.assertEqual(summary["pull_requests_discovered"], 3)
            self.assertEqual(summary["pull_requests_harvested"], 3)
            self.assertEqual(summary["labels"], 3)
            self.assertEqual(len(summary["records_sha256"]), 64)
            self.assertEqual(summary["new"], 3)
            self.assertEqual(summary["by_label"], {"important": 2, "minor": 1})
            self.assertEqual(len(first_repo.read_text().splitlines()), 2)
            self.assertEqual(len(second_repo.read_text().splitlines()), 1)

    def test_keeps_successful_labels_when_one_pr_fails(self) -> None:
        pull_requests = [
            github_labels.PullRequest("example/one", 7, ""),
            github_labels.PullRequest("example/one", 8, ""),
        ]

        def harvest(repository: str, number: int) -> list[dict]:
            if number == 8:
                raise RuntimeError("review comments unavailable")
            return [
                {
                    "source": f"github:{repository}#{number}",
                    "reply_id": number,
                    "label": "minor",
                    "at": "2026-08-20T00:00:00Z",
                }
            ]

        with tempfile.TemporaryDirectory() as directory:
            summary = github_labels.harvest_pull_requests(
                pull_requests, Path(directory), 1, harvest
            )

        self.assertEqual(summary["pull_requests_harvested"], 1)
        self.assertEqual(summary["pull_requests_failed"], 1)
        self.assertEqual(summary["errors"][0]["number"], 8)


if __name__ == "__main__":
    unittest.main()
