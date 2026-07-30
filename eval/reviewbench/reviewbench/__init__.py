"""reviewbench — review 评测编排。

corpus（merged PR 的 merge 双亲，冻结的评审范围）× arms（engine × feature gates）
跑成 eval_harness 的 Worksheet（大表 + reconciler 断点续跑 + pivot 报表）。

三条评测线共用这一套编排（eval/README 中重放与人工标注流程的工具化）：
- 全 PR 回放打 metric（成本/健壮性全自动，质量轴 = LLM judge + posterior/人工标注）
- ccr vs ocr（arm 换 engine 二进制，findings 归一化后盲评）
- feature gate 消融（arm 换 --feature 组合）
"""
