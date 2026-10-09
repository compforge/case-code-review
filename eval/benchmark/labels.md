# 人工标签与数据集

命令在仓库根执行，环境准备见 [eval](../README.md)。

GitHub 标签采集需要 `gh` 并通过 `gh auth status`；GitLab 使用 `GITLAB_TOKEN` 与 `GITLAB_HOST` 环境变量。
凭据留在本机，不进入仓库配置。

## 在 PR/MR 上形成 ground truth

CCR finding comment 末尾通常带 `ccr:fp=<fingerprint>`。逐条对照真实 diff、源码和执行路径
求证后，在 finding 的同一线程回复：

```text
ccr:label=important — 会破坏现有调用契约，已采纳修复
ccr:label=minor — 问题成立但影响较小，已采纳修复
ccr:label=debatable — 属于取舍或防御性建议，不作为缺陷采纳
ccr:label=wrong — 实际调用在进入此处前已被校验 #cross-file
ccr:label=repeat — 同一问题已由本 MR 更早的 comment 提出（附 comment 链接或 id）
```

发现 CCR 漏掉的真实问题时，直接在 diff 行创建新评论：

```text
ccr:missed — 这里在并发关闭后仍可能写入已关闭 channel
```

标注纪律：

- 每条 finding 都标，不能只收集 `wrong` 或只收集采纳项。
- 必须查代码求证，不能因为 finding 文本听起来合理就同意它。
- `wrong` 给出可验证反证；拿不准用 `debatable`。
- `repeat` 只表示同一 MR 的更早 comment 已交付同一问题，并附其链接或 id；它不表示
  “问题在本次 diff 之前就存在”。
- 本次 diff 之前已存在的行为不算有效 finding，按 `wrong #out-of-diff` 标注并给出 attribution 反证。
- 可选病因 tag：`#textbook`、`#padding`、`#out-of-diff`、`#stale`、
  `#cross-file`。

## 回收 GitHub 标签

按时间窗批量发现并回收当前用户已合并的 PR：

```bash
python3 -m eval.benchmark.github_labels \
  --owner <github-owner> \
  --author @me \
  --since 2026-08-17T00:00:00+08:00 \
  --until 2026-08-24T00:00:00+08:00
```

时间窗是带时区的 `[since, until)`，与周报的本地周边界对齐；不接受无时区日期，避免本地周初、周末
落到相邻 UTC 日期时漏采或重复采集。

默认按仓库 upsert 到 `eval/data/labels/<owner>-<repo>.jsonl`，并写入
`eval/data/labels/github-harvest.json`。manifest 记录源快照身份以及发现、成功和失败的 PR 数；周报用它区分
“确实没有 label”和“采集没有运行或只完成了一部分”。GitHub resolved thread 的 review comments
仍能从 REST API 读取，批量采集不会过滤它们。

需要只刷新一个 PR 时使用单 PR 入口：

同一仓库的多个 PR 反复写入同一个文件即可；脚本按 `(source, reply_id)` upsert，重复执行安全。

```bash
python3 -m eval.benchmark.labels github <owner>/<repo> <pr-number> \
  --out eval/data/labels/<owner>-<repo>.jsonl
```

## 回收 GitLab 标签

先在调用环境中安全注入 `GITLAB_TOKEN`：

```bash
export GITLAB_HOST=gitlab.example.com
python3 -m eval.benchmark.labels gitlab <group>/<repo> <mr-iid> \
  --host "$GITLAB_HOST" \
  --out eval/data/labels/<group>-<repo>.jsonl
```

`GITLAB_HOST` 也可以只通过 `--host` 传入。脚本支持标准 GitLab discussions API；私有平台若
协议不同，应在本地维护 adapter，并保持 gitignore。

## 构建规范化数据集

```bash
python3 -m eval.benchmark.build_label_dataset
```

默认读取：

```text
eval/data/labels/*.jsonl
~/.casecodereview/sessions/**/*.jsonl
```

默认生成：

```text
eval/data/datasets/review-comments-public.jsonl
eval/data/datasets/review-comments-private.jsonl
eval/data/datasets/label-dataset.json
```

这里的 `public/private` 是按 forge 来源分桶，两个文件都属于真实数据，都会被 gitignore。
`label-dataset.json` 记录数据集快照、所消费的 GitHub harvest 快照和两个 JSONL 的内容摘要；周报据此
确认自己读取的是本次采集生成的完整数据集，而不是用文件时间或最新 label 时间猜测新鲜度。
session finding 只用于补齐早期没有在 forge comment 中保存正文、但仍有 fingerprint 的记录。
新 session 还会按 fingerprint 与时间连接生成该 Finding 的 Hypothesis、Assessment 和执行身份；
找不到对应旧 session 时这些字段为空，不影响历史标签入集。

快速检查：

```bash
wc -l eval/data/datasets/*.jsonl
jq -s 'group_by(.label) | map({label: .[0].label, count: length})' \
  eval/data/datasets/review-comments-public.jsonl
```

单条规范化记录包含：

```json
{
  "id": "stable-example-id",
  "kind": "finding",
  "finding": "review comment body",
  "label": "wrong",
  "rationale": "human counter-evidence",
  "tags": ["cross-file"],
  "fingerprint": "finding-fingerprint",
  "path": "path/to/file",
  "line": 42,
  "model": "model-alias",
  "source": "forge:repo#change",
  "comment_url": "thread URL",
  "reply_id": "forge reply id",
  "by": "reviewer",
  "at": "RFC3339 timestamp",
  "engine": {
    "session_id": "session id",
    "tool_version": "ccr version",
    "model": "model id",
    "features": {},
    "params": {},
    "git_head": "reviewed HEAD",
    "hypothesis": {},
    "assessment": {}
  }
}
```

`engine` 让同一个 human verdict 能追溯到 generator、reviewer、feature 与 Trial 输入，避免把不同
引擎版本的结果混成同一组效果数据。若只比较收敛式 Hypothesis Review，不要重新运行 Unit Review；
先冻结候选集：

```bash
python3 -m eval.benchmark.build_hypothesis_dataset
```

默认从两个 finding 数据集提取带阶段产物的样本，生成
`eval/data/datasets/review-hypotheses.jsonl`。其中 `expected_delivery` 由人工标签推导：
`important/minor` 应通过，其余标签应被拦截；后续 reviewer/Trial 实验必须消费同一批 Hypothesis，
才能把收敛精度变化与 Unit Review 的召回变化分开。


## 后验扫描

后续源码修改是人工复查候选信号，不自动成为真值标签。

```bash
python3 -m eval.benchmark.posterior <session.jsonl-or-dir> \
  --repo <reviewed-repo-path> \
  --labels eval/data/labels/<name>-posterior.jsonl
```
