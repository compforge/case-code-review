# eval — 采集 CCR 评审数据

本目录提供可公开复用的采集、规范化和重放工具。最短路径是：在 PR/MR finding 线程中完成
`ccr:label` 标注，用 `labels.py` 回收标签，再用 `build_label_dataset.py` 生成自包含 JSONL
数据集。

## 数据边界

```text
eval/
├── *.py、reviewbench/   工具代码和配置模板，可进入 Git
└── data/                真实 corpus、labels、datasets、trajectory 和运行产物，不进入 Git
```

所有真实数据都写入 `eval/data/`，该目录已整体 gitignore。公开仓只跟踪通用 GitHub/GitLab
采集逻辑；绑定私有 forge、CLI 或实例协议的 adapter 仍应留在本地。

不要把 token、内部 URL、仓库名、用户名或真实 finding 复制到脚本、README、测试 fixture
或 tracked 配置中。

## 五分钟上手

以下命令都在仓库根目录执行。

### 1. 准备环境

- Python 3.10+；基础采集脚本只使用标准库。
- GitHub：安装 `gh` 并确认 `gh auth status` 成功。
- GitLab：在当前 shell 或 secret manager 中提供 `GITLAB_TOKEN`，并设置 `GITLAB_HOST`；
  不要把 token 写入仓库文件。
- 采集本地 trajectory 时，额外要求 `ccr` 已安装并产生过
  `~/.casecodereview/sessions/`。

先确认本地数据目录确实被忽略：

```bash
git check-ignore -v eval/data/
```

### 2. 在 PR/MR 上形成 ground truth

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

### 3. 回收 GitHub 标签

同一仓库的多个 PR 反复写入同一个文件即可；脚本按 `(source, reply_id)` upsert，重复执行安全。

```bash
python3 eval/labels.py github <owner>/<repo> <pr-number> \
  --out eval/data/labels/<owner>-<repo>.jsonl
```

### 4. 回收 GitLab 标签

先在调用环境中安全注入 `GITLAB_TOKEN`：

```bash
export GITLAB_HOST=gitlab.example.com
python3 eval/labels.py gitlab <group>/<repo> <mr-iid> \
  --host "$GITLAB_HOST" \
  --out eval/data/labels/<group>-<repo>.jsonl
```

`GITLAB_HOST` 也可以只通过 `--host` 传入。脚本支持标准 GitLab discussions API；私有平台若
协议不同，应在本地维护 adapter，并保持 gitignore。

### 5. 构建规范化数据集

```bash
python3 eval/build_label_dataset.py
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
```

这里的 `public/private` 是按 forge 来源分桶，两个文件都属于真实数据，都会被 gitignore。
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
python3 eval/build_hypothesis_dataset.py
```

默认从两个 finding 数据集提取带阶段产物的样本，生成
`eval/data/datasets/review-hypotheses.jsonl`。其中 `expected_delivery` 由人工标签推导：
`important/minor` 应通过，其余标签应被拦截；后续 reviewer/Trial 实验必须消费同一批 Hypothesis，
才能把收敛精度变化与 Unit Review 的召回变化分开。

## 可选：采集本地 review trajectory

`collect.py` 从 CCR session 导出 ATIF、comments 和按 unit 汇总：

```bash
python3 eval/collect.py \
  --repo <reviewed-repo-path> \
  --since <YYYY-MM-DD> \
  --out eval/data/runs/<collection-name>
```

不调用 LLM 的客观链路诊断：

```bash
python3 eval/trajectory_judge.py \
  eval/data/runs/<collection-name>/<trajectory>.atif.jsonl \
  --no-llm
```

后验扫描 finding 指向的代码是否被后续提交修改：

```bash
python3 eval/posterior.py <session.jsonl-or-dir> \
  --repo <reviewed-repo-path> \
  --labels eval/data/labels/<name>-posterior.jsonl
```

`line_touched` 只是后验候选，仍需人工确认后续 commit 是否确实在修该 finding。

## 可选：建立固定 corpus 并重放

从本地 clone 构建 merge-parent corpus：

```bash
cd eval/reviewbench
uv sync
uv run python -m reviewbench.corpus_build <repo-path> \
  --limit 30 \
  --out ../data/corpus/<name>.json
cd ../..
```

用相同 corpus 对比 feature arms：

```bash
python3 eval/replay.py eval/data/corpus/<name>.json \
  --repo <repo-path> \
  --arm base \
  --arm candidate:<feature>=on \
  --out eval/data/runs/<replay-name>
```

同工作负载比较时同时看 finding 质量、token/轮数成本和 incomplete/timeout；不能仅用 finding
数量判断优劣。

## 给协作者 AI 的执行约定

可以把下面这段直接交给协作者的 AI：

```text
在仓库根目录按 eval/README.md 采集 CCR 数据。
1. 先读取 AGENTS.md，确认公开仓脱敏规则。
2. 不打印、记录或提交 token；认证缺失时停下并告诉我缺什么。
3. 对每条 finding 查真实 diff/代码后再打 ccr:label，五类都收集；wrong 给具体反证，
   repeat 指向本 MR 更早的同问题 comment。
4. 使用 eval/labels.py 回收，每个仓库持续 upsert 到 eval/data/labels/。
5. 运行 eval/build_label_dataset.py，报告总数、label 分布和 unpaired 数。
6. 最后确认 git ls-files eval/data 和
   git ls-files --others --exclude-standard eval/data 都没有输出。
7. 不执行 git add/commit/push，除非我明确要求。
```

## 收口检查

```bash
python3 -m py_compile \
  eval/labels.py \
  eval/build_label_dataset.py \
  eval/build_hypothesis_dataset.py \
  eval/collect.py \
  eval/posterior.py \
  eval/replay.py \
  eval/trajectory_judge.py

git ls-files eval/data
git ls-files --others --exclude-standard eval/data
git status --short
```

前两个 `git ls-files` 命令必须没有输出。最终只汇报样本数量和分布，不在终端或聊天中粘贴
真实 finding 正文。

## 常见问题

- **采集为 0**：确认 label 是 finding 线程的回复，父 comment 带 `ccr:fp=` 或 CCR header。
- **GitLab 401/403**：确认 token 有读取 MR discussions 的权限，host 没带协议前缀。
- **出现 unpaired**：优先重新采集带 finding 正文的 forge thread；早期记录可由本地 session
  fingerprint 回填。
- **找不到 session**：确认 `--repo` 是当时运行 CCR 的仓库路径，且对应 session 尚在
  `~/.casecodereview/sessions/`。
- **重复采集**：可以直接重跑；labels 输出是幂等 upsert，不需要手工去重。
