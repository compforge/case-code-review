# CCR 评测

`benchmark/` 用固定数据集观察评审效果、筛选待优化 case；`trajectory/` 回到这些 case 的
原始 Session，分析工具、上下文、阶段流转与成本。人工标签、生产周报和固定语料回放也可以提供 case。

```text
数据集 → 正常运行 Review 1 / 2 / 3 → 效果分析 → selected.jsonl
                                                  ↓
                                 原始 Session → 轨迹分析 → 改动与复测
```

## 开始使用

需要 Python 3.11+、uv，以及已配置的 CCR。以下命令在仓库根执行：

```bash
uv sync --project eval --locked
uv run --project eval python -m eval.benchmark.run --help
uv run --project eval python -m eval.trajectory.analyze_cases --help
```

- [数据集效果分析](benchmark/README.md)：AACR 接入、实验运行、阶段匹配与 case 筛选。
- [轨迹分析](trajectory/README.md)：筛选结果、单次 Session 诊断和轨迹数据集。
- [人工标签](benchmark/labels.md)：从 PR/MR 回收标注、规范化并关联 Session。
- [固定语料回放](benchmark/replay.md)：既有 corpus × arms 对照实验。
- [每周汇总](weekly.md)：标签效果、运行健康和轨迹成本的跨域报告。

## 数据与事实边界

真实 corpus、标签、Session、模型判断与报告统一放在 ignored `eval/data/`。
目录迁移不搬动已有数据；独立 worktree 不自动拥有主 worktree 的数据。
公开仓只保留通用代码、配置模板和匿名测试，私有 forge adapter 继续留在本地。

Session JSONL 是评审执行事实源，`session_recording.py` 负责共享读取，
`eval_snapshot.py` 负责内容身份。benchmark 保存 dataset/case/run/arm → Session 的关联及内容 hash；
轨迹分析使用同一份记录。Review 2 的 Assessment 是被评测输出，不能替代独立参考标注。
未完成运行和未判定的匹配显式保留，不能把 0 Finding 解释为 clean。

## 开发验证

```bash
make -C eval fix lint test
git ls-files eval/data
git ls-files --others --exclude-standard eval/data
```

后两个命令应没有输出。入口统一使用 `python -m eval.<模块>`，避免依赖隐式 `sys.path`。
可观测性职责与设计见 [observability](../docs/observability.md)。
