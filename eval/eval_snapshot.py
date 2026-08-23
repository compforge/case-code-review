"""Content-addressed snapshot helpers for CCR eval artifacts."""

from __future__ import annotations

import hashlib
import json
from pathlib import Path
from typing import Any, Iterable, Mapping


def canonical_sha256(value: Any) -> str:
    encoded = json.dumps(
        value, ensure_ascii=False, sort_keys=True, separators=(",", ":")
    ).encode()
    return hashlib.sha256(encoded).hexdigest()


def file_sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as stream:
        for chunk in iter(lambda: stream.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def dataset_artifacts(
    paths: Iterable[tuple[Path, int]],
) -> list[dict[str, object]]:
    return [
        {
            "name": path.name,
            "path": str(path.resolve()),
            "records": records,
            "sha256": file_sha256(path),
        }
        for path, records in paths
    ]


def artifacts_match(
    paths: Iterable[Path], expected: Iterable[Mapping[str, object]]
) -> bool:
    expected_by_name = {
        str(artifact.get("name") or ""): str(artifact.get("sha256") or "")
        for artifact in expected
    }
    actual = list(paths)
    if not expected_by_name or len(actual) != len(expected_by_name):
        return False
    return all(
        path.is_file()
        and expected_by_name.get(path.name) == file_sha256(path)
        for path in actual
    )
