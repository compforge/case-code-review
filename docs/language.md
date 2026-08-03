# Language：源码事实层

## 1. 理念 / 概念

Language 是 CCR 唯一的源码语言边界。它把 Go、Python、TypeScript 等不同语法与语义后端，统一成
上层可消费的源码事实；不决定 Unit 如何合并，也不参与 finding 判断。

```text
source files
  └─ language backend
       ├─ symbol / definition / span
       ├─ outline / containment
       ├─ reference / call edge
       ├─ symbol / file proximity
       ├─ doc binding / import / dependency root
       └─ stable identity
            └─ Unit / Clue / tools
```

这层的核心约束是：**有证据的事实才向上输出，解析失败保持 unknown**。上层可以选择降级策略，
但不能把语言层的猜测当成确定关系。

Language 对 `spec / case / link / rule / doc` 只拥有语法抽取、代码身份和 relation 绑定；这些声明表达的
契约与业务场景属于 Project Knowledge 中作者声明的 Biz Knowledge。也就是说，Language 回答“这条
声明绑定到哪段代码”，Project Knowledge 回答“这条声明要求代码守住什么”。

## 2. 流程

### 2.1 单文件分析

Analyzer 面向一份明确的源码快照，提供：

- 可定位的定义及其 span；
- 文件结构的 Outline 与 containment；
- 文件内可识别的符号、文档和依赖；
- 从 diff hunk 到所属定义的归属；
- 在给定语言能力内可证明的引用关系。

这些事实支持 Fragment 切分、范围预载、评论定位和局部搜索。后端不能可靠识别时，应退化到文件
级范围，不能制造虚假的函数边界。

同一批事实也可以投影成 `FileOutline`：代码保留 type/callable 及语言原生的数据成员层级，JSON
保留 key 结构，Markdown 保留标题层级。Outline 是源码消息在上下文收紧时的导航摘要，不是新的
事实来源，也不能替代读取源码验证行为。

### 2.2 仓库级索引

RepositoryIndex 把单文件事实组合成仓库查询面，用于 definition lookup、reference lookup、repo map
和调用关系，并从可证明的结构边推导 symbol/file proximity。索引是可重建的事实视图，不持有 review
业务状态。

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

数据成员同样由 backend 映射：Go/Java 可称为 field，TypeScript 称为 property，Python 称为
attribute。FileOutline 统一消费它们的结构角色，但展示语言自身的术语；不支持该能力的 backend
只输出已有 type/callable，不影响分析主链路。

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

## 4. 演进方向：CodeGraph 作为源码关系查询面

File Outline 提供 type、function、field 等结构节点；再通过作用域、import、类型和继承解析连接定义、
引用与调用，便可以形成表达 file / symbol 关系的 `CodeGraph`。`CallGraph` 只是其中
callable-to-callable 的一个投影，完整关系还包括 containment、dependency、reference、inheritance
和 implementation。

CodeGraph 的目的不是追求一张尽可能完整的仓库图，而是为 Review 提供统一、可查询的源码关系事实。
同一份事实可以有三种消费方式：

1. **Context compaction**：完整源码可降级为 File Outline 与关键关系；节点关系也帮助上下文管理器
   判断哪些材料应优先保留。Language 提供可压缩的事实，实际压缩仍由 Harness 负责。
2. **Initial context**：以 Unit 为中心找到高相关 file / symbol，在 loop 开始前注入相关 span、Outline
   或小文件全文，把可预测的多轮 `read_files` 探索变成一次上下文供给。
3. **On-demand tools**：初始上下文无法覆盖的问题，继续通过 `find_callers`、`find_callees`、
   `find_implementations` 等只读工具查询；工具和预载应复用同一 CodeGraph，避免产生两套关系判断。

CodeGraph 围绕 Unit 可提供如下关系视图：

```text
Unit
├─ changed symbols
├─ containing type / file / component
├─ callers / callees
├─ implementations / inheritance
├─ related entrypoints / handlers
└─ related tests
```

symbol 或 file 的 `proximity`（关系接近度）用于衡量图结构上的接近程度，可以综合 graph distance、边类型和置信度；
Unit Review 再结合当前 diff、Unit 和 token 预算判断 context relevance。图上接近不等于评审相关，
因此 Language 只提供关系、proximity、来源与置信度，不决定最终预载内容；Lane 等聚类场景再结合
Component、目录与实际证据重合计算 review affinity。

Review 不直接接收完整仓库图，而只接收有界的相关材料。强关系优先提供 symbol span 或 Outline，
小文件可按需提供全文，弱关系只暴露路径和关系说明；`read_files` 留给静态图无法预测的证据。效果可
通过预载文件与实际读取文件的重合率、无效预载比例持续验证，避免为了减少工具调用而无界扩大初始
prompt。

实现上优先渐进扩展现有 Analyzer / RepositoryIndex，并保证每条关系与 review snapshot 一致；SCIP、
Stack Graphs、tree-sitter-graph 和 Joern / Code Property Graph 可作为协议、名称解析与图模型参考，
不以引入重量级图数据库作为前提。

## References

- [`kernel.md`](kernel.md) — Language 在 CCR Kernel 中的位置
- [`unit-model.md`](unit-model.md) — 源码事实如何形成 Unit 与 Clue
- [`harness.md`](harness.md) — `read_files` / `search_code` 等只读工具的执行边界
- [SCIP](https://github.com/scip-code/scip) · [Stack Graphs](https://github.github.com/stack-graph-docs/) ·
  [tree-sitter-graph](https://github.com/tree-sitter/tree-sitter-graph) ·
  [Joern](https://github.com/joernio/joern) — Code Graph 的协议、解析与完整模型参考
