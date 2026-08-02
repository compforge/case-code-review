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

## References

- [`kernel.md`](kernel.md) — Language 在 CCR Kernel 中的位置
- [`unit-model.md`](unit-model.md) — 源码事实如何形成 Unit 与 Clue
- [`harness.md`](harness.md) — `read_files` / `search_code` 等只读工具的执行边界
