# Unit 模型：Fragment 原子 + Unit 作用域

> 设计 spec。落地锚点见末尾 References；具体字段/阈值以代码为准。

## 理念

**初衷**：一个文件一个 review loop 太死板，所以把评审基本单位下沉到 **unit**（一函数一 unit）；但函数级会炸出太多 loop，所以**必要时合并**——除了按文件合（成本），还按调用链合（一个需求天然横跨上下游几个 func/文件）。在此之上，再给每个 loop 喂**更合适的上下文**（spec-case 标注 + 上一轮 review history + caller/callee）。本文档管前半截（unit 的粒度与结构）；上下文那半截已落地（见 References）。

ccr 区别于 ocr 的一等概念就是 **unit**——评审的作用域。一次 review loop 跑在一个 unit 上。
上游无论来自 git diff 还是 full-file scan，都先成为 `Change`；git source 负责采集，Unit
拥有形成作用域所需的 change/hunk 事实。它有两个层级、两个类型、两个流水线阶段：

- **Fragment（原子）**：单文件的一段变更——一个函数的 hunks，或函数外的残余。Splitter 产出。**纯数据**：无 context、无分组。
- **Unit（作用域）**：1..N 个 Fragment 按某条轴成组 + 收齐的上下文（Clues）。Merger 产出，**review loop 真正跑的东西**。

历史术语对照：旧的 *diff unit* → **Fragment**；*review unit* → **Unit**。两者拆成独立类型，各自演进——Fragment 是 Splitter 的稳定原子，只有 Unit 会长（文件粗化 → 调用链 → 未来类/模块）。

## 流程

```
git change / full scan ─▶ Change
     ─Splitter─▶ Fragment（diff：每文件每函数一个 + 残余；scan：整文件一个）
     ─Merger（两步顺序）─▶ Unit
          ① call-chain（语义轴）：跨文件、调用相邻、且都在 diff 里的 Fragment 成一簇
          ② file-coalesce（成本轴）：残余若超水位，按同文件并
     ─Finders 对 Unit 收 Clue（spec/case/rule/link · caller/callee · history）─▶ review loop
```

func unit = 1 Fragment；file 粗化 = 1 Fragment（多符号、整文件 diff）或同文件多 Fragment；call-chain = N 个跨文件 Fragment。

## 关键设计

1. **Fragment 原子 / Unit 作用域，两类型**——为何：类型级区分 diff 与 review（拿到哪个一眼清、传错即编译错），且各自独立演进。Fragment 不背 context/grouping，那些是 Unit 的。

2. **Clue 挂作用域、在 merge 之后收**——为何：context 是"这次一起审的范围"的属性。对最终 Unit 收一次，比旧的"在原子上收、合并时 union"少一道；且成员去重天然（已知整簇成员，caller/callee 命中本簇成员就跳过）。

3. **调用图两种用途，别混为一谈**：
   - *adjacency*（给 merge 决定分组）：轻量问"changed X 是否调 changed Y"。
   - *context*（给 Clue）：问"governing spec / 依赖的契约"。
   两者都用 call-graph，但目的、产物不同。

4. **merge = 两步顺序，不是框架**——为何：现仅两轴（语义/成本）。README 的粒度阶梯（类/模块）等**第三条真实轴落地**再泛化。YAGNI：不建 pass-pipeline / 注册表，就先 call-chain 再 file-coalesce 顺序写。

5. **跨文件 Unit 的落地**：reviewUnit 渲染**所有 Fragment 的 diff**（按文件分块、带文件头）；评论已 per-comment 自带路径，**评论路由不改**；change-files 列表遍历成员路径；行数 = 成员求和。

6. **命名**：`Unit` 留给作用域（贴合"unit 是评审作用域"的北极星）；原子叫 `Fragment`。

7. **`symbol-id` 不是 `Unit` 的 id**——三层关系要分清：
   ```
   symbol-id（<relpath>::<symbol>，一个函数，最细）
       ⊂ Fragment（单文件区域，覆盖 1+ symbol-id）
           ⊂ Unit（评审作用域，含 1+ Fragment → 引用一组 symbol-id）
   ```
   `symbol-id` 是 spec-case 的**函数级 spec 绑定键**（specgen 产、ccr 查），键格式 `<relpath>::<symbol>` **不变**；`Fragment.Symbols` 就是它覆盖的 symbol-id 们。
   > 这个概念早先叫 "unit-id"，与 ccr 的评审作用域 `Unit` 字面撞、且方向相反（symbol-id 命名最细那层、Unit 命名最粗那层）。已跨仓改名为 `symbol-id`（spec-case 术语/资产 + ccr 的 `symbol_id` 字段 + devloop 消费），把 "unit" 让给评审作用域；键格式 `::` 不变。

8. **unit 生命周期（完成边界）——已实现**：scope 状态机 `open → closing → closed`。loop 以 `Close(debrief)` 声明完成；异步 comment 工作（relocation）以 per-scope 计数（`BeginAsync`/`EndAsync`）注册，Close 时若仍在飞则停靠 debrief、最后一个异步任务归零时自动 finalize（聚合+落盘）——debrief 的完整性与并发安全由**生命周期**保证，不再靠编排顺序（此前 #90 竞态、debrief 队列、Await 时序注释是同一缺口的三次显影，队列机制已删除）。向 closed scope 写入被计数并告警（`LateWrites`）；panic 的 unit 也走 Close（`outcome=panic`，进健壮性仪表盘）。team 方向的 final bulletin 挂点即在 Close（`docs/cross-unit.md`）。

## 两条合并轴：动机不同（语义 vs 成本）

- **call-chain（语义 / 按需求分组）**先做：一个需求天然横跨一条调用链——`X 调 Y`、两者都在本次 diff，就按这条链当一个 unit 审，**和 reviewer 脑中"这个需求改了哪些地方"对齐**（抓出贯穿调用链的交互 bug 是顺带收益）。只合**都变了的**相邻；只一个变的邻居走 context 注入（不合并）。
- **file-coalesce（成本兜底）**收尾：大改动会炸出太多 func unit → loop 爆；同文件并粗，**降 loop 数、不降 context**。
- **簇大小上限**：一个改动函数扇出多个改动 callee 时，连通分量可能很大 → 设上限（累计变更行 / 成员数超阈值就不再吸纳），超出的退回成本轴，**防单个 unit 巨到塞爆一次 loop**。一般改动（小簇）走语义轴、大改动（大簇/超量）落成本轴——正好对应初衷里"一般改动 vs 大改动"两种场景。
- adjacency 计算 costly，沿用现有预算闸门：unit 太多就跳过语义合并、退回文件粗化。

## References

- clue 后置那半截（unit 拿到后怎么收证据）：[`context-model.md`](context-model.md)（Clue / ClueFinder / Relation / Dossier，两轴正交）。
- 实现锚点：`internal/unit`（`Fragment` / `Unit` / `Splitter` / `Merger`）及其
  `change`/`spec`/`codegraph`/`history` 子包，`internal/runner/source` 与
  `internal/runner/scan`（输入），以及 `internal/runner`（finder 装配与 clue 收集时机）。
- 上层定位：`AGENTS.md`（unit 一等概念）、`README.md`（理念）。
