# Unit 与评审上下文

## 1. 理念 / 概念

**Unit 是一次行为审查的边界**。已改动目标之间的图关系决定哪些跨文件改动值得共同评审，
其余改动按文件收拢，让理解同一项行为变化所需的改动共享一次上下文。

**让相关改动得到完整、有界的共同评审**，是选择 Unit 作为基本单元的目的。调用方与被调用方、
公开绑定与使用方等协作改动可以共享一次理解。未参与跨文件分组的 import、声明和残余区段按文件
合并，不各自启动评审。Fragment 保留源码归属和改动范围，每条目标编辑恰好归属一个 Unit。
Unit 总数不超过进入 formation 的改动文件数；这是 CCR 的调度约束，必要时继续合并共享文件的组。

CodeGraph 的 Node + Relation 提供源码归属和关系，Language 适配版本、身份与范围，Formation 决定
哪些 Fragment 一起评审。图可以是局部的，静态分析也可能不完整；缺少关系表示未知，不妨碍保留目标改动。

Project Knowledge 先用 Repository / Component / FileRole 解释文件的稳定项目职责，再把 source 交给
Unit formation，把 manifest / lock 等项目事实投影为 Clue。Component 是静态项目边界，Unit 是一次
diff 动态形成的行为边界；具体分类与 snapshot 约束见 [`project.md`](project.md)。

```text
Git Change ─▶ Component / FileRole
                   ├─ source ─▶ Fragment ─────────────▶ Unit{Fragments, Clues, Review State}
                   │     └─ entrypoint / handler ─▶ project Clue ─────▲
                   ├─ manifest / lock ────────────▶ project Clue ─────▲
                   └─ version ─▶ no Unit Review
```

| 对象 | 语义 |
|---|---|
| `Change` | Git 层的一份文件变更 |
| `Fragment` | Change 中可独立定位的改动片段，对应声明、导入/导出绑定或残余文件区段 |
| `Unit` | 一次 run 的评审聚合根：稳定行为范围，以及逐阶段追加的事实快照、Hypothesis、Assessment 和 Trial decision |
| `Clue` | 与 Unit 有关系、可用于判断契约的事实或线索 |

Project、Unit 和上下文回答三个不同问题：文件在项目中是什么、哪些目标改动一起审、审它时带哪些
事实。Project 事实可被不同 Review 阶段复用；Unit 只保存与当前行为范围有关的投影。

## 2. 流程

### 2.1 先区分 Unit target 与项目上下文

每个 Change 先由 Project Knowledge 解析所属 Component 与可组合 FileRole。当前策略把 source 作为
target，把同 Component 中变化的 manifest / lock 作为 project Clue；entrypoint / handler 等角色作为
Unit 自身的项目先验。用户显式 include 仍可提升文件，未被 Component 认领的文件继续走全局规则。

Project 分类完成后才进入 formation；Clue 在 Unit scope 最终确定后挂载，避免静态 Component 边界
替代动态行为边界。Project 只提供事实，是否形成 Unit 仍由 formation 决定。

### 2.2 从 Fragment 形成 Unit

1. Git 固定比较基线和目标版本，并捕获改动文件内容。增加行在新侧图中定位，删除行在旧侧图中定位；
   重命名保留两侧路径。旧侧图按需构建，声明的文档与标记范围沿图中的归属一并定位。
2. Formation 按图中最内层源码归属切分编辑块。连续替换保留为同一个补丁，不用同名推断跨版本身份；
   无法定位的编辑保留为 residual。拆分前后校验编辑的坐标与内容，确保每条编辑恰好出现一次。
3. 以 `Exact/Scoped` 的调用、引用、继承、实现、别名和导出关系连接已改动目标，优先选择触及改动行的
   使用关系。共同依赖同一个未改动工具函数，不构成合并两处改动的依据。
4. 按稳定顺序合并有关联的 Fragment，并检查文件数、改动行数和 diff token 预算。保留形成的跨文件组，
   其余 Fragment 按文件组成剩余改动 Unit。关闭图关系分组时，每个文件形成一个 Unit。
5. 若跨文件组与剩余改动 Unit 的总数超过改动文件数，则反复合并共享文件且合计 diff 最小的两个组，
   直到满足数量上限。已提取的关联组保持完整；数量约束本身不构成图关系证据。

例如 `file1.func1` 调用 `file2.func2`，且两者都发生改动：

- 若这次只修改两者，就形成一个 `func1 + func2` Unit。
- 若 `file1` 还修改了无关的 `func3`，则形成 `func1 + func2` 和 `func3` 两个 Unit。
- 若同样两个文件中有多组独立跨文件关系以及剩余改动，初步分组可能超过两个；此时按数量约束继续
  合并共享文件的组。所有目标仍恰好覆盖一次，已提取的关联组不会被拆散。

大小预算限制按图关系扩张；按文件收拢和满足数量上限时可以超过该预算。Fragment 数量反映源码
结构的细度，不单独决定合并预算。最终分属不同 Unit 的连接关系保留为跨 Unit 线索。
大 Unit 仍保留完整目标并进入评审，由 Harness 的上下文、时间和 token 预算控制执行；真正无法
完成时报告 incomplete。大小预算不会把补丁切成更多评审循环，也不会直接跳过目标。
`budget_exceeded` 表示 Unit diff 超过合并阈值，不表示该 Unit 已被跳过。

图决定“哪些关系有依据”，CCR 决定“这次哪些目标一起审”。分组依据和预算边界随 Unit 保存，
`--dry-run --format json` 与 Session 可以解释每个目标的归属、合并关系、切断关系和材料缺口。

### 2.3 为 Unit 组织上下文

Clue 用两个正交维度表达上下文：

- **Relation**：事实与 Unit 的关系，如 `self`、`owner`、`caller`、`callee`、`used`、`project`。
- **Kind**：事实的来源或契约种类，如 `spec`、`case`、`rule`、`link`、`doc`、`history`、`project`。

其中 `spec / case / link / rule / doc` 是 Project Knowledge 中作者声明的 Biz Knowledge：它们表达项目
希望代码守住的业务契约、具体场景、关系和说明。Language Knowledge 负责识别注释、装饰器、
symbol-id / fqn 等语法与身份，把这些声明绑定到 definition / relation；Project Knowledge 负责声明
内容本身。两者分开后，新增语言只需实现可靠绑定，不必复制业务契约模型。

因此“caller 的 spec”和“self 的 history”无需新增专用字段。ClueFinder 只负责发现并挂载事实，
不决定 prompt 排版；Unit 保存完整的 Fragments 与 Clues，Runner 再按预算、优先级和消息形状投影为
Review Messages。

```text
Unit
  └─ ClueFinder[]
       └─ Clue{Relation, Kind, Ref, Content}
            └─ Unit.Clues
                 └─ Review Messages
```

每个 Unit 都按自己的符号集合寻找 self / owner / used / caller / callee，上下文查询不依赖 file、func
或 related 的展示标签。邻域搜索有独立深度、数量和访问预算；改动总量变大不会关闭整批 Unit 的上下文。
已归入 Unit 的成员提供自身契约与文档，避免合并后因排除“内部邻居”而丢失证据。旧侧线索带基线标识，
旧的仓库契约从基线版本读取；删除侧 finding 保留旧路径和旧行号。

### 2.4 初始消息与按需工具

初始消息只预载高确定性、高复用的信息：Unit 自身 diff、必要源码、直接契约和少量高价值邻域。
未知路径和低概率细节由 Review loop 通过只读工具按需获取。实际进入上下文或由工具成功读取的仓库
事实按其真实形状追加到 Unit：文件内容是 `FileSnapshot`，额外变更切片是 `DiffSnapshot`，检索输出是
`SearchResult`；Unit 自身目标 diff 仍只来自 Fragment，避免重复事实源。Review Messages 是这些完整
事实到 Execution 的可压缩投影，不再引入一个泛化的材料对象。

这条边界同时控制两个风险：

- 全量预载会让每个 Unit 重复携带仓库材料，成本随 Unit 数放大；
- 完全依赖工具会浪费轮次重新寻找本可确定注入的事实。

当内容超预算时，应先降级为范围、摘要或可重取指针，而不是静默丢掉整个 Unit。无法完成的 Unit
必须显式标为 partial/incomplete，不能伪装成 clean。

## 3. 关键设计

### 3.1 稳定身份连接 diff、源码和契约

路径和短函数名不足以跨文件、重命名和依赖建立关系。语言层提供稳定 `symbol-id`；作者声明的
契约另保留可跨仓匹配的 `fqn`。Fragment 与 Unit 身份由完整路径、两侧图身份和补丁内容派生，成员顺序不改变 Unit 身份。
Clue 和历史反馈使用各自支持的身份连接，Forge 只剩文件锚点时
才退化到 path。

身份解析失败表示 `unknown`，不能用猜测的同名符号替代。这是防止“上下文看似丰富、实际属于
另一个函数”的基本准确性边界。

### 3.2 图事实按置信度消费

CodeGraph 负责产出 definition、reference、call edge 等源码事实；Unit 层决定这些事实能否参与合并
和上下文组织。

- repo map 可使用图已提供的低置信候选排序；它不从裸名匹配重新生成符号关系。
- `Exact/Scoped` 调用边可用于 caller/callee；分组也消费引用、类型和绑定关系，不宣称完整编译器语义。
- owner 沿图的语义归属或词法嵌套查找；used 按改动行选择 Reference，并沿 references / aliases 查找声明或显式公开绑定。
- 同一版本的 caller/callee、owner、used、usage 和声明文档共享图快照；新旧侧分别解释。源码项边与声明依赖边按用途选择，不重复计数。
- 无法判定的边保持 unknown，不升级成“确定调用”；contract catalog 提供作者的含义，不能通过同名命中证明源码绑定。

图既不是独立的最终产品，也不能直接控制 review loop。它是 Unit formation 和 Clue 的证据来源，
其错误成本取决于消费位置：展示错一个候选影响有限，错误合并 Unit 则会改变整个评审边界。

### 3.3 Unit 在一次 run 内是追加式聚合根

Unit 在 formation 后保持稳定身份和 Fragment 边界，并沿主链路追加四类状态：Review 实际读取的
文件/diff/搜索快照、Review 1 提出的 Hypothesis、Review 2 接受的 Assessment，以及 Trial decision。阶段包拥有
“如何产生”的逻辑，Unit 只保存“关于这个行为范围已经知道什么”，不吸收 Lane conversation、turn、
token 等执行状态；后者仍属于 Harness Session。

快照始终保存完整 raw 内容；Runner 为不同 Execution 投影独立的 File/Diff/Search AgentMessage，由消息
类型定义压缩方式，并按当前 Review 阶段赋予保留优先级。消息压缩不会反向修改 Unit，因此 Review 2
和 Trial 看到的领域事实不受某次 prompt 投影影响。

Hypothesis `ID` 标识来源 Unit 中的一次主张，`Fingerprint` 标识跨 Unit / revision 的同一底层 claim。
因此每个 Unit 都能保留自己的完整轨迹，而 Trial 仍可按 Fingerprint 去重交付。并发调度只改变执行
时机，不改变状态的语义归属。默认关闭的 Review Team 试验可以通过 Board / Bulletin 交换跨 Unit
主张，但同样不得反向修改已确定的静态 Unit 边界。

### 3.4 效果评估不能只看 comment 数

Unit 设计同时影响召回、准确率和成本，至少应观察：

- 原始 diff file 数、实际 review file 数与 Review 1 loop 数，区分文件过滤和 Unit formation 各自
  节省的 loop；
- 目标编辑覆盖率、Fragment 到 Unit 的合并比例、错误合并和预算切断样本；
- 每个 Unit 的预载字节、工具调用、token 和完成状态；
- 有真实 finding 的 Unit 是否获得了足够契约和邻域；
- 被合并或被拆开的 Unit 是否改变 wrong / missed；
- partial Unit 是否被单独统计，而非混入 clean。

## References

- [`kernel.md`](kernel.md) — CCR 总体主链路与领域边界
- [`project.md`](project.md) — Repository、Component、FileRole 与项目事实投影
- [`language.md`](language.md) — symbol、definition、reference 与图事实的生产边界
- [`unit_review.md`](unit_review.md) — Unit 进入 Review 1 后的探索、收敛与效果优化
- [`hypothesis_review.md`](hypothesis_review.md) — Hypothesis 在 Lane 中的复核与 Trial
