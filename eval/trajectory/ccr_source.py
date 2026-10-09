"""CCR session implementation of the trajectory_harness recording source."""

from __future__ import annotations

import os
import subprocess
from datetime import UTC, datetime
from pathlib import Path
from typing import Callable, Iterable
from urllib.parse import unquote, urlparse

from trajectory_harness import Recording, RecordingQuery, RecordingRef
from eval.session_recording import Recording as SessionRecording

Exporter = Callable[[Path], str]


class CCRSessionSource:
    """Discover intact evidence from closed and unfinished local CCR sessions and fetch their ATIF export."""

    def __init__(
        self,
        sessions_root: str | Path = Path.home() / ".casecodereview" / "sessions",
        *,
        repositories: Iterable[str | Path] = (),
        ccr_command: str = "ccr",
        export_timeout_seconds: float = 120,
        exporter: Exporter | None = None,
    ) -> None:
        self.sessions_root = Path(sessions_root).expanduser().resolve()
        self.repositories = tuple(
            Path(repo).expanduser().resolve() for repo in repositories
        )
        self.ccr_command = ccr_command
        self.export_timeout_seconds = export_timeout_seconds
        self.exporter = exporter

    def select(self, query: RecordingQuery | None = None) -> list[RecordingRef]:
        query = query or RecordingQuery()
        refs: list[RecordingRef] = []
        if not self.sessions_root.is_dir():
            return refs

        for path in self.sessions_root.rglob("*.jsonl"):
            inspected = _inspect_session(path)
            if inspected is None:
                continue
            manifest, closed, gaps = inspected
            if not self._matches_repository(
                str(manifest.get("cwd") or "")
            ):
                continue
            started_at = _timestamp(manifest.get("timestamp"))
            if started_at is None:
                started_at = datetime.fromtimestamp(path.stat().st_mtime, tz=UTC)
            attributes = {
                "cwd": str(manifest.get("cwd") or ""),
                "model": str(manifest.get("model") or ""),
                "tool_version": str(manifest.get("tool_version") or ""),
                "biz_id": str(manifest.get("biz_id") or ""),
                "git_head": str(manifest.get("git_head") or ""),
                "closed": closed,
                "recording_incomplete": not closed or bool(gaps),
                "recording_gaps": gaps,
            }
            if not _matches_query(query, started_at, attributes):
                continue
            refs.append(
                RecordingRef(
                    recording_id=str(manifest.get("sessionId") or path.stem),
                    uri=path.resolve().as_uri(),
                    started_at=started_at,
                    attributes=attributes,
                )
            )

        refs.sort(
            key=lambda ref: (
                ref.started_at or datetime.min.replace(tzinfo=UTC),
                ref.recording_id,
            )
        )
        return refs[: query.limit] if query.limit is not None else refs

    def fetch(self, ref: RecordingRef) -> Recording:
        path = _path_from_uri(ref.uri)
        try:
            path.resolve().relative_to(self.sessions_root)
        except ValueError as error:
            raise ValueError("recording is outside the CCR sessions root") from error
        if not path.is_file():
            raise FileNotFoundError(path)
        inspected = _inspect_session(path)
        if inspected is None or not inspected[1]:
            raise ValueError(
                f"CCR session is not a closed recording: {ref.recording_id}"
            )
        if self.exporter is not None:
            text = self.exporter(path)
        else:
            try:
                result = subprocess.run(
                    [self.ccr_command, "export", "--format", "atif", str(path)],
                    capture_output=True,
                    text=True,
                    timeout=self.export_timeout_seconds,
                    check=False,
                )
            except subprocess.TimeoutExpired as error:
                raise RuntimeError(
                    f"CCR ATIF export timed out for {ref.recording_id}"
                ) from error
            if result.returncode != 0:
                detail = result.stderr.strip()[:300]
                raise RuntimeError(
                    f"CCR ATIF export failed for {ref.recording_id}: {detail}"
                )
            text = result.stdout
        if not text.strip():
            raise ValueError(f"CCR ATIF export was empty for {ref.recording_id}")
        return Recording(ref=ref, text=text)

    def _matches_repository(self, cwd: str) -> bool:
        if not self.repositories:
            return True
        candidate = Path(cwd).expanduser().resolve()
        for repository in self.repositories:
            if candidate == repository:
                return True
            worktrees = repository / ".worktrees"
            try:
                candidate.relative_to(worktrees)
                return True
            except ValueError:
                continue
        return False


def _inspect_session(path: Path) -> tuple[dict, bool, list[str]] | None:
    try:
        recording = SessionRecording.read(path)
        if not recording.start:
            return None
        return recording.start, recording.end is not None, recording.gaps
    except OSError:
        return None


def _timestamp(value: object) -> datetime | None:
    if not value:
        return None
    try:
        parsed = datetime.fromisoformat(str(value).replace("Z", "+00:00"))
    except ValueError:
        return None
    return (
        parsed.replace(tzinfo=UTC) if parsed.tzinfo is None else parsed.astimezone(UTC)
    )


def _matches_query(
    query: RecordingQuery,
    started_at: datetime,
    attributes: dict[str, object],
) -> bool:
    lower = _as_utc(query.started_at_or_after)
    upper = _as_utc(query.started_before)
    if lower is not None and started_at < lower:
        return False
    if upper is not None and started_at >= upper:
        return False
    return all(attributes.get(key) == value for key, value in query.attributes.items())


def _as_utc(value: datetime | None) -> datetime | None:
    if value is None:
        return None
    return value.replace(tzinfo=UTC) if value.tzinfo is None else value.astimezone(UTC)


def _path_from_uri(uri: str) -> Path:
    parsed = urlparse(uri)
    if parsed.scheme != "file":
        raise ValueError(
            f"CCR session source only fetches file URIs, got {parsed.scheme!r}"
        )
    return Path(os.path.abspath(unquote(parsed.path)))
