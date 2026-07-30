# Codegraph：统一图消费层

> `internal/unit/codegraph` 统一承接 language facts 的图用途：repo-map 排名、caller/callee 上下文与 call-chain 邻接。源码事实和解析后端只在 `internal/language`；codegraph 只拥有图算法、成本策略与评审语义。

## 理念

### 一张图，按边的置信度分层消费

不同消费者对边精度的容忍度天然不同，这是整个设计的支点：

| 消费者 | 吃错边的代价 | 可接受的边 |
|---|---|---|
| 符号地图排序（repo_map） | 浪费几个 token | 名字配对边（语法扫描） |
| caller 上溯找治理 spec | **契约基线错**——按错的 spec 评审 | 类型消解边 |
| chain merge 邻接 | **评审作用域被污染**——不相干改动并成一条链 | 类型消解边 |

所以图不必全对就能全用：`internal/language` 的低置信 `RepositoryIndex`（名字配对）只喂排序；Go semantic overlay（go/packages 类型检查）喂 scope 决策。**歧义即弃**是高置信侧的铁律：接口分发调用点不建边、仓外目标丢弃——错误的边比没有边更糟（参考 CodeGraph 项目同款纪律）。

### 复杂度红线（防"自研 codegraph 内卷"）

- 本包上限 = **抽取 + 排序 + 直接调用边**。一旦发现在写类型推断、跨文件数据流、符号消歧启发式——越界信号，改用现成工具（Go 侧 `golang.org/x/tools`，跨语言语义索引走外部服务如 CodeGraph via MCP，消费不自建）。
- 新消费场景按 **eval 失败模式驱动**，不按图的能力驱动：backlog（backlink 文档过期提醒、governor 影响面预算、spec 覆盖缺口报告）全部挂起，等 eval 数据点名再取用。
- ccr 面向多语言，语言不能永久停在 file 档；新增一等语言时优先复用该语言的权威 parser，把函数边界与 symbol-id 接入统一 Unit 契约，解析器不可用时再降级到 file 档。

## 流程

```
                    language.ScanRepository
diff 种子           → RepositoryIndex (definitions/references)
(文件+符号)          → codegraph adapter → Extraction → Rank(PageRank,
                                              diff 种子加权) → BuildMap → {{repo_map}}
                                                                              (每 unit prompt)
                    language Go semantic overlay (go/packages + TypesInfo)
                        → CallGraph（类型消解直接调用边，test 文件除外）
                        → codegraph.TypedGraph（懒构建、每 review 一次、60s 超时）
                              ├→ CallerFinder/CalleeFinder（clue：spec 上溯 / 依赖契约）
                              └→ CallAdjacency（merge：chain 邻接）
                          （非 Go 符号 / 构建失败 → ok=false → 原 grep 启发式兜底）
```

工具侧同源治理：`code_search` 零命中时返回近似真实标识符（`code_search_suggest.go`）——L1 地图把瞎猜消灭在源头后，它退化为保险网。

## 关键设计

1. **为什么 rank 在文件级建图、符号级出分**：图规模 = 文件数不是符号数（PageRank 迭代便宜），rank 按出边权分摊到 `(文件, 符号)` 仍能挑出具体符号——aider repo-map 的内核，工程外围（磁盘缓存、pygments 兜底、refresh 策略）全部不移植，per-review 一次性用不上。

2. **为什么 TypedGraph 是"权威或闭嘴"**：对 Go 符号，图的回答可信到"空也算数"（类型检查器看过全模块，没 caller 就是没 caller）；对非 Go 符号 / 构建失败，返回 `ok=false` 而不是空列表——调用方必须能区分"权威的无"和"不知道"，否则 grep 兜底永远不会触发。空目录/非模块同理必须报错，不能伪装成权威空图。

3. **为什么懒构建 + 单句柄共享**：packages.Load 秒级成本只在第一个 Go 邻居查询时支付一次；费边门（大 diff 短路 caller-walk）成立时根本不建图。clue 和 merge 穿同一个句柄，保证两个消费者看到同一张图。

4. **共享路径不准写 stdout**：repo_map 和 typed 图的构建日志都曾污染 `--format json`（两次同款事故）。规则：被 dry-run 复用的路径只写 stderr / telemetry。

5. **实证记录**（2026-07-03，mono-sandbox/hostel，dry-run A/B 零 LLM 成本）：`Shell.Run` 的邻域，grep 模式 8 个引用符号中 5 个错（chromium 的同名 `Run` 撞车 + 测试函数混入）；typed 模式精确 3 个（真 caller ×2 + callee ×1）。空搜索治理（L0+L1）实测：基线最差三单 32/53（60%）空搜索 → 0/25，总 tool call 降 1/3，prompt token 净降 10%。

## References

- 评估方法论与基线：`eval/README.md`
- unit / context 模型：`docs/unit-model.md` · `docs/context-model.md`
- 算法蓝本（工作区只读参考）：aider `repomap.py`（PageRank 内核）、CodeGraph（置信度阶梯、did-you-mean、24K 硬顶经验）
