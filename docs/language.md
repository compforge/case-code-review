# Language：源码事实层

## 1. 理念 / 概念

Language 是 CCR 唯一的源码语言边界。它把 Go、Python、TypeScript 等不同语法与语义后端，统一成
上层可消费的源码事实；不决定 Unit 如何合并，也不参与 finding 判断。

```text
source files
  └─ language backend
       ├─ symbol / definition / span
       ├─ reference / call edge
       ├─ doc / import / dependency root
       └─ stable identity
            └─ Unit / Clue / tools
```

这层的核心约束是：**有证据的事实才向上输出，解析失败保持 unknown**。上层可以选择降级策略，
但不能把语言层的猜测当成确定关系。

## 2. 流程

### 2.1 单文件分析

Analyzer 面向一份明确的源码快照，提供：

- 可定位的定义及其 span；
- 文件内可识别的符号、文档和依赖；
- 从 diff hunk 到所属定义的归属；
- 在给定语言能力内可证明的引用关系。

这些事实支持 Fragment 切分、范围预载、评论定位和局部搜索。后端不能可靠识别时，应退化到文件
级范围，不能制造虚假的函数边界。

### 2.2 仓库级索引

RepositoryIndex 把单文件事实组合成仓库查询面，用于 definition lookup、reference lookup、repo map
和调用关系。索引是可重建的事实视图，不持有 review 业务状态。

不同查询对置信度要求不同：

- repo map 和搜索建议可以容忍低置信候选，因为模型仍会读取源码核实；
- caller/callee 上下文需要更高置信，否则会注入无关材料；
- call-chain Unit 会改变评审边界，只接受类型或等价语义解析证明的边。

### 2.3 统一身份

`symbol-id` 是仓库内连接 diff、definition、reference、Unit 和 history 的主要身份。它应由语言结构
生成，而不是简单拼接显示名。作者声明的跨仓契约使用 `fqn`；两者解决的问题不同，不应互相替代。

当 Forge 或外部数据只保留文件锚点时，上层可以降级到 path，但 Language 不应通过猜测恢复一个
看似精确的 symbol-id。

## 3. 关键设计

### 3.1 Backend 隔离语言差异

语言专属 parser、type checker、tree-sitter grammar 和 fallback 都封装在 backend 内。上层依赖统一
接口，而不是散落 `if language == ...`。新增语言时，优先补齐事实能力表；缺失能力明确返回 unknown，
而不是复制另一门语言的近似语义。

### 3.2 图是事实投影，不是第二套语言模型

代码图由 definition、reference 和 call edge 投影而来。Language 负责边的来源与置信度，Unit 层负责
如何消费：低置信边可做提示，高置信边才可参与 Unit formation。这样既避免图实现侵入 Runner，
也避免同一调用关系在多个模块各自猜一次。

### 3.3 快照一致性优先于索引复用

一次 review 的 diff、源码读取和索引查询必须指向同一份 review snapshot。工作区、range 和 commit
模式可以使用不同 Git 读取方式，但不能让 definition 来自当前工作区、diff 却来自旧 commit。
缓存只有在快照身份一致时才能复用。

### 3.4 复杂度边界

Language 解决“代码事实是什么”，不解决：

- 哪些 Fragment 应合并成 Unit；
- 哪些上下文值得注入；
- agent 该调用几轮工具；
- Hypothesis 是否应成为 Finding。

这些分别属于 Unit、Unit Review、Harness 和 Hypothesis Review。保持该边界，才能让语言能力增长
而不把评审策略固化进 parser。

## 4. 演进方向：从 File Outline 到 Code Graph

File Outline 可以把一个文件压缩为 type、function、field 等结构事实；汇总整个项目的 Outline 后，
首先得到的是 `SymbolIndex`，而不是完整的 Call Graph。后者还需要识别调用点，并通过作用域、import、
类型和继承关系把调用点绑定到具体定义。无法可靠绑定的关系应继续保持候选或 unknown。

长期可以把这些事实统一投影为 `CodeGraph`：

```text
File Outline ─▶ SymbolIndex ─▶ reference resolution ─▶ CodeGraph
                                                        ├─ containment
                                                        ├─ import / dependency
                                                        ├─ definition / reference
                                                        ├─ call
                                                        └─ inheritance / implementation
```

`CallGraph` 只是其中 callable-to-callable 的一个投影。构建 CodeGraph 的目标不是追求一张尽可能
完整的仓库图，而是让它对 Unit 的 prompt context 有稳定贡献。Review 不直接接收完整仓库图，而是
围绕当前 Unit 裁剪出有界的 `GraphSlice`：

```text
Unit
├─ changed symbols
├─ containing type / file / component
├─ callers
├─ callees
├─ implementations / inheritance
├─ related entrypoints / handlers
└─ related tests
```

这份切片用于解释“改了什么、处于哪里、谁依赖它、它依赖谁、哪些入口和测试受影响”。每条边保留
来源与置信度，避免用低置信语法猜测扩大 Unit 或制造无关上下文；是否注入以及注入到何种细节，仍由
Unit Review 的上下文策略决定。

CodeGraph 的另一个作用是计算 symbol 或 file 之间的 `proximity`：根据图距离、边类型和置信度衡量
两个节点在代码结构上有多近。这里使用 proximity，而不是笼统的“亲密度”；`graph distance` 表示原始
路径距离，`affinity` 更适合 Lane 等聚类场景，`relevance` 则表示某份材料对当前 Unit 是否值得注入。
图上接近不等于评审相关，因此 CodeGraph 提供 proximity，Unit Review 再结合 diff 和预算判断
context relevance。

GraphSlice 也为上下文预载提供依据：图距离、关系类型和置信度越高，文件与当前 diff 的相关性通常
越强。Unit Review 可以据此提前注入模型大概率会读取的材料，把多轮 `read_files` 探索变成一次已知
上下文；优先注入相关 symbol span 或 File Outline，小文件再按需注入全文，弱关系只保留路径和关系
说明。`read_files` 仍用于补充静态图无法预测的证据，而不是重复获取已经确定的近邻材料。

Language 只输出关系事实及其来源，不决定预载排序；Unit Review 结合 Unit、token 预算和消息压缩
能力选择实际材料。效果可通过“预载文件与后续实际读取文件的重合率”及“无效预载比例”持续验证，
避免为了减少工具调用而无界扩大初始 prompt。

可参考的演进路径：SCIP 的跨语言符号与引用协议、Stack Graphs 的跨文件名称解析、
tree-sitter-graph 的语法树到图投影，以及 Joern / Code Property Graph 对多类程序关系的统一表达。
CCR 优先渐进扩展现有 Analyzer / RepositoryIndex，不以引入重量级图数据库作为前提。

## References

- [`kernel.md`](kernel.md) — Language 在 CCR Kernel 中的位置
- [`unit-model.md`](unit-model.md) — 源码事实如何形成 Unit 与 Clue
- [`harness.md`](harness.md) — `read_files` / `search_code` 等只读工具的执行边界
- [SCIP](https://github.com/scip-code/scip) · [Stack Graphs](https://github.github.com/stack-graph-docs/) ·
  [tree-sitter-graph](https://github.com/tree-sitter/tree-sitter-graph) ·
  [Joern](https://github.com/joernio/joern) — Code Graph 的协议、解析与完整模型参考
