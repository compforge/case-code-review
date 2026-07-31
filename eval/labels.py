#!/usr/bin/env python3
"""Harvest human finding-labels from forge review threads → labels jsonl.

Convention (eval/README "在 PR/MR 上形成 ground truth"): a ccr-posted review
comment carries a `ccr:fp=<fingerprint>` footer; a human replies to it with a line starting
`ccr:label=<important|minor|debatable|wrong|repeat>` (free-text rationale may follow,
on the same line or below — it becomes `note`; `#tag`s in the note are extracted
into `tags`, using the vocabulary documented in README). Additionally a
human may comment `ccr:missed — <desc>` directly on any diff line (NOT a reply)
to record a miss ccr never reported — the recall-side negative sample, emitted
with `label=missed` and no fingerprint. This script joins them all and emits one
jsonl line per label, keyed by fingerprint — the same join key posterior.py
labels use, so human and posterior labels merge in eval rollups.

Usage:
    python3 eval/labels.py github <owner>/<repo> <pr-number> [--out labels.jsonl]
    python3 eval/labels.py gitlab <group/subgroup/proj> <mr-iid> --host <gitlab-host> [--out labels.jsonl]

Output line shape:
    {"fingerprint": "abc123def456" | null, "finding": "original finding comment",
     "label": "wrong", "note": "...",
     "tags": ["textbook"], "path": "x/y.py", "line": 216,
     "source": "github:owner/repo#9", "comment_url": "...", "reply_id": 123,
     "by": "login", "at": "..."}

- fingerprint is null for comments posted before the footer convention —
  path/line are kept so the label can be back-filled by hand.
- `--out` upserts on (source, reply_id), so re-harvesting is idempotent and can
  enrich records written by older harvester versions.
- v1 harvests inline (diff-anchored) threads only; labels on the summary
  comment's fallback list have no per-finding thread to hang on.

github needs an authenticated `gh` CLI. gitlab (any GitLab-API forge, incl.
self-hosted) needs `GITLAB_TOKEN` in the env and the instance host via `--host`
or `GITLAB_HOST` — host/token stay invocation-side, never in this repo.
"""

from __future__ import annotations

import argparse
import json
import os
import re
import subprocess
import sys
import urllib.parse
import urllib.request
from pathlib import Path

FP_RE = re.compile(r"ccr:fp=([0-9a-f]{6,40})")
LABEL_RE = re.compile(
    r"^\s*ccr:label=(important|minor|debatable|wrong|repeat)\b[ \t:—-]*(.*)$",
    re.M,
)
# missed 是"ccr 没报、人发现了"的负样本——对 diff 任意行直接评论，不是对 ccr 评论的回复。
# 它没有 fingerprint（没有 finding 可指），靠 path/line + 描述入册，补齐召回侧 ground truth。
MISSED_RE = re.compile(r"^\s*ccr:missed\b[ \t:—-]*(.*)$", re.M)
# 病因 tag：note 里的 #tag（如 #textbook #padding #out-of-diff #cross-file），
# 推荐词表见 eval/README；开放集合，不在此枚举校验。
TAG_RE = re.compile(r"#([a-z][a-z0-9-]+)")
CCR_HEAD = "devloop code-review"  # fp-less fallback: recognize ccr comments by header


def _gh_json(path: str) -> list[dict]:
    out = subprocess.run(
        ["gh", "api", path, "--paginate"], capture_output=True, text=True, check=True
    ).stdout
    # --paginate concatenates arrays; gh emits them back-to-back
    items: list[dict] = []
    dec = json.JSONDecoder()
    i, s = 0, out.strip()
    while i < len(s):
        obj, j = dec.raw_decode(s, i)
        items.extend(obj if isinstance(obj, list) else [obj])
        i = j
        while i < len(s) and s[i] in " \n\r\t":
            i += 1
    return items


def harvest_github(repo: str, pr: int) -> list[dict]:
    comments = _gh_json(f"repos/{repo}/pulls/{pr}/comments")
    by_id = {c["id"]: c for c in comments}
    labels: list[dict] = []
    for c in comments:
        body = c.get("body") or ""

        # ccr:missed —— 漏报负样本：人对 diff 行直接评论，无父级 ccr finding
        mm = MISSED_RE.search(body)
        if mm and not c.get("in_reply_to_id"):
            note = (mm.group(1) + body[mm.end():]).strip()
            labels.append(
                {
                    "fingerprint": None,
                    "label": "missed",
                    "note": note,
                    "tags": sorted(set(TAG_RE.findall(note))),
                    "path": c.get("path"),
                    "line": c.get("line") or c.get("original_line"),
                    "source": f"github:{repo}#{pr}",
                    "comment_url": c.get("html_url"),
                    "reply_id": c["id"],
                    "by": (c.get("user") or {}).get("login"),
                    "at": c.get("created_at"),
                }
            )
            continue

        m = LABEL_RE.search(body)
        parent = by_id.get(c.get("in_reply_to_id") or -1)
        if not m or parent is None:
            continue
        pbody = parent.get("body") or ""
        if CCR_HEAD not in pbody and not FP_RE.search(pbody):
            continue  # a labelled reply, but not on a ccr finding
        fp = FP_RE.search(pbody)
        # note = same-line remainder + any following lines of the reply
        note = (m.group(2) + body[m.end():]).strip()
        labels.append(
            {
                "fingerprint": fp.group(1) if fp else None,
                "finding": pbody,
                "label": m.group(1),
                "note": note,
                "tags": sorted(set(TAG_RE.findall(note))),
                "path": parent.get("path"),
                "line": parent.get("line") or parent.get("original_line"),
                "source": f"github:{repo}#{pr}",
                "comment_url": parent.get("html_url"),
                "reply_id": c["id"],
                "by": (c.get("user") or {}).get("login"),
                "at": c.get("created_at"),
            }
        )
    return labels


def _gitlab_json(host: str, token: str, path: str) -> list[dict]:
    items: list[dict] = []
    page = 1
    while True:
        url = f"https://{host}/api/v4/{path}?per_page=100&page={page}"
        req = urllib.request.Request(url, headers={"PRIVATE-TOKEN": token})
        with urllib.request.urlopen(req, timeout=30) as resp:
            batch = json.loads(resp.read().decode("utf-8"))
        if not isinstance(batch, list):
            return [batch]
        items.extend(batch)
        if len(batch) < 100:
            return items
        page += 1


def harvest_gitlab(host: str, project: str, mr: int) -> list[dict]:
    """GitLab 的线程是一等公民：discussion.notes[0] 即 ccr finding（fp footer），
    后续 notes 是回复——不用像 GitHub 那样靠 in_reply_to_id 自己拼父子关系。"""
    token = (os.environ.get("GITLAB_TOKEN") or "").strip()
    if not token:
        sys.exit("gitlab harvest needs GITLAB_TOKEN in the env")
    proj = urllib.parse.quote(project, safe="")
    discussions = _gitlab_json(host, token, f"projects/{proj}/merge_requests/{mr}/discussions")
    source = f"gitlab:{host}/{project}!{mr}"
    labels: list[dict] = []
    for d in discussions:
        notes = [n for n in d.get("notes") or [] if not n.get("system")]
        if not notes:
            continue
        head = notes[0]
        hbody = head.get("body") or ""
        # position 只在 diff-anchored discussion 上有；plain note 的 path/line 为 null
        pos = head.get("position") or {}
        path = pos.get("new_path") or pos.get("old_path")
        line = pos.get("new_line") or pos.get("old_line")
        head_url = f"https://{host}/{project}/-/merge_requests/{mr}#note_{head['id']}"

        # ccr:missed —— 漏报负样本：人开的新 discussion，不是对 ccr finding 的回复
        mm = MISSED_RE.search(hbody)
        if mm:
            note = (mm.group(1) + hbody[mm.end():]).strip()
            labels.append(
                {
                    "fingerprint": None,
                    "label": "missed",
                    "note": note,
                    "tags": sorted(set(TAG_RE.findall(note))),
                    "path": path,
                    "line": line,
                    "source": source,
                    "comment_url": head_url,
                    "reply_id": head["id"],
                    "by": (head.get("author") or {}).get("username"),
                    "at": head.get("created_at"),
                }
            )
            continue

        if CCR_HEAD not in hbody and not FP_RE.search(hbody):
            continue  # not a ccr finding thread
        fp = FP_RE.search(hbody)
        for reply in notes[1:]:
            rbody = reply.get("body") or ""
            m = LABEL_RE.search(rbody)
            if not m:
                continue
            note = (m.group(2) + rbody[m.end():]).strip()
            labels.append(
                {
                    "fingerprint": fp.group(1) if fp else None,
                    "finding": hbody,
                    "label": m.group(1),
                    "note": note,
                    "tags": sorted(set(TAG_RE.findall(note))),
                    "path": path,
                    "line": line,
                    "source": source,
                    "comment_url": head_url,
                    "reply_id": reply["id"],
                    "by": (reply.get("author") or {}).get("username"),
                    "at": reply.get("created_at"),
                }
            )
    return labels


def upsert_records(path: Path, records: list[dict]) -> tuple[int, int]:
    existing: list[dict] = []
    if path.exists():
        existing = [
            json.loads(line)
            for line in path.read_text(encoding="utf-8").splitlines()
            if line.strip()
        ]
    positions = {
        (record.get("source"), record.get("reply_id")): index
        for index, record in enumerate(existing)
    }
    fresh = 0
    updated = 0
    for record in records:
        key = (record["source"], record["reply_id"])
        if key not in positions:
            positions[key] = len(existing)
            existing.append(record)
            fresh += 1
            continue
        index = positions[key]
        merged = existing[index] | record
        if merged != existing[index]:
            existing[index] = merged
            updated += 1
    if fresh or updated:
        path.parent.mkdir(parents=True, exist_ok=True)
        temp = path.with_suffix(path.suffix + ".tmp")
        with temp.open("w", encoding="utf-8") as output:
            for record in existing:
                output.write(json.dumps(record, ensure_ascii=False) + "\n")
        temp.replace(path)
    return fresh, updated


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("forge", choices=["github", "gitlab"])
    ap.add_argument("repo", help="owner/repo (github) or group/subgroup/proj (gitlab)")
    ap.add_argument("pr", type=int, help="PR number (github) or MR iid (gitlab)")
    ap.add_argument("--host", default=os.environ.get("GITLAB_HOST"),
                    help="gitlab instance host (or GITLAB_HOST env)")
    ap.add_argument("--out", type=Path, help="append (deduped on source+reply_id); default: stdout")
    args = ap.parse_args()

    if args.forge == "github":
        labels = harvest_github(args.repo, args.pr)
    else:
        if not args.host:
            ap.error("gitlab needs --host (or GITLAB_HOST env)")
        labels = harvest_gitlab(args.host, args.repo, args.pr)
    if not args.out:
        for rec in labels:
            print(json.dumps(rec, ensure_ascii=False))
        return 0

    # key 带 source：不同 forge 的 note/comment id 是独立序列，裸 id 会跨源撞。
    fresh, updated = upsert_records(args.out, labels)
    print(
        f"harvested {len(labels)} label(s), {fresh} new, {updated} updated → {args.out}",
        file=sys.stderr,
    )
    return 0


if __name__ == "__main__":
    sys.exit(main())
