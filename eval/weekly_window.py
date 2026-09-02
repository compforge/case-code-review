"""Shared ISO-week and ignored-dataset path resolution for CCR eval reports."""

from __future__ import annotations

import subprocess
from dataclasses import dataclass
from datetime import date, datetime, time, timedelta, timezone
from pathlib import Path
from typing import cast
from zoneinfo import ZoneInfo

DEFAULT_DATASET_PATHS = (
    Path("eval/data/datasets/review-comments-public.jsonl"),
    Path("eval/data/datasets/review-comments-private.jsonl"),
)


@dataclass(frozen=True, slots=True)
class WeekWindow:
    key: str
    start: datetime
    end: datetime

    @classmethod
    def from_key(cls, key: str, zone: ZoneInfo) -> "WeekWindow":
        try:
            year_text, week_text = key.split("-W", maxsplit=1)
            year, week = int(year_text), int(week_text)
            if len(year_text) != 4 or len(week_text) != 2:
                raise ValueError
            start_date = date.fromisocalendar(year, week, 1)
        except ValueError as error:
            raise ValueError(f"invalid ISO week {key!r}; expected YYYY-Www") from error
        start = datetime.combine(start_date, time.min, tzinfo=zone)
        return cls(key=key, start=start, end=start + timedelta(days=7))

    @classmethod
    def previous_complete(
        cls, zone: ZoneInfo, now: datetime | None = None
    ) -> "WeekWindow":
        local_now = (now or datetime.now(timezone.utc)).astimezone(zone)
        current_monday = local_now.date() - timedelta(days=local_now.weekday())
        previous_monday = current_monday - timedelta(days=7)
        iso_year, iso_week, _ = previous_monday.isocalendar()
        return cls.from_key(f"{iso_year}-W{iso_week:02d}", zone)

    def previous(self) -> "WeekWindow":
        previous_date = self.start.date() - timedelta(days=7)
        iso_year, iso_week, _ = previous_date.isocalendar()
        return WeekWindow.from_key(
            f"{iso_year}-W{iso_week:02d}", cast(ZoneInfo, self.start.tzinfo)
        )

    def contains(self, value: datetime) -> bool:
        local = value.astimezone(self.start.tzinfo)
        return self.start <= local < self.end


def default_dataset_paths(repo_root: Path | None = None) -> list[Path]:
    """Resolve ignored eval data from the main worktree when run in a worktree."""

    root = (repo_root or Path.cwd()).resolve()
    local = [root / path for path in DEFAULT_DATASET_PATHS]
    if any(path.is_file() for path in local):
        return local
    try:
        result = subprocess.run(
            ["git", "rev-parse", "--path-format=absolute", "--git-common-dir"],
            cwd=root,
            capture_output=True,
            text=True,
            timeout=5,
            check=True,
        )
    except (OSError, subprocess.SubprocessError):
        return local
    common_root = Path(result.stdout.strip()).resolve().parent
    shared = [common_root / path for path in DEFAULT_DATASET_PATHS]
    return shared if any(path.is_file() for path in shared) else local
