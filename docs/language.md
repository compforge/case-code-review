# Language：源码分析的接入边界

## 理念与职责

CCR 通过独立项目 [CodeGraph](https://github.com/compforge/codegraph) 分析源码：输入 Document，
构造 symbol graph，outline 从已发布图的节点与关系派生。语法解析、声明抽取、名称绑定、关系与置信度
由 CodeGraph 持有；CCR 的 Language 层提供输入材料，并把结果适配为评审使用的身份、范围和展示。

```text
review snapshot ─▶ Document ─▶ CodeGraph
                                  ├─ declarations / imports / references
                                  └─ symbol graph + diagnostics
                                           ↓
                             Language 的身份与展示适配
                                           ↓
                         Fragment / Unit / Clue / 源码导航
```

`internal/unit/sourcecontext` 消费这些结果，负责上下文相关性排序、寻找最近的契约、限制线索数量。
形成哪个 Unit、注入哪些材料、Hypothesis 是否成立，分别由评审领域决定。

作者声明的 `spec / case / link / rule / doc` 属于 Project Knowledge。Language 用源码身份和 import
信息帮助 CCR 找到对应声明，不决定声明表达的业务契约。

## 分析流程

### 单文件与仓库共用解析产物

Analyzer 把明确的路径和内容交给 CodeGraph Extractor，并发布图后再生成评审视图。Facts 只在提取和
构图入口传递，消费侧以 Node + Relation 为唯一代码事实来源。单文件导航与仓库图共享有界
ExtractionCache，缓存按路径与内容区分版本。声明、导入、使用角色和文档均从图投影。

RepositoryIndex 在一次 review 中延迟构建并共享。CCR 选择有界的源码集合，提供 Go module 根，
将提取结果交给 Builder 一次构造关系图。测试源码可以提供 caller/usage 证据，但不进入 repo map
的定义候选集；依赖目录、隐藏目录和过大的文件不参与分析。

Git provider 固定比较基线与目标 commit，捕获改动文件的前后内容。工作区模式优先使用捕获的改动内容，
其余文件在发布时读取；commit/range 模式从固定的目标 commit 读取。删除或重命名涉及的旧侧关系从基线
单独构图。同一侧的调用关系、usage 行文本、owner/used 契约和声明文档共享一次发布，新旧侧保留版本标识。
改动文件优先进入有界的源码集合，Go module 元数据先于源码读取。新的源码版本需要新的 review 实例。

读取、解析与构图受文件数、字节数和时间预算限制。CodeGraph 的 BuildReport 保留分析诊断，CCR
另记录输入加载缺口。Session 的 `codegraph` / `codegraph_before` artifact 保存构建耗时、规模、诊断计数与有界样本，图不可用或局部覆盖
不足时仍继续评审已有源码，不能把空关系解释成“没有调用者”。源码量相同也可能产生不同的图密度；
构图触发节点或关系预算时，按既定优先级缩小输入并在原时限内重试，记录被省略的文档数量。

### 评审身份与源码范围

CodeGraph 的节点 ID 标识图内声明；CCR 的 `path::qualifiedName` 是连接 Unit、spec 和历史反馈的
既有 join key。Language 通过声明的路径和 qualified name 转换身份，不从裸名称反向猜测目标。
同名或重载声明在 CCR 身份下无法唯一对应时，关系消费保持保守。Reference、Import、Export
是源码项，不能转换为声明 join key。owner 沿 contains 优先、encloses 补充查找，支持跨文件 Go
接收者；qualified name 的分隔符只用于身份展示，不证明归属。

CodeGraph location 的行号从 1 开始、字节结束位置不包含在范围中；Language 转换为 CCR 的闭区间
行范围。声明头读取 Node.Signature；CCR 只压缩展示空白、限制长度。缺少签名时以符号名展示。

### Outline 是导航投影

FileOutline 负责源码消息的结构摘要和范围裁剪。代码 outline 只消费 Graph 的 Node 与 Relation：
encloses 提供词法结构，contains 可将 Go 方法归到同文件接收者类型下，节点提供签名和位置。
JSON key 和 Markdown 标题由 CCR 的文档展示逻辑处理。

Outline 不能代替读取源码验证行为。上游拒绝输出或解析失败时，保留已有的源码/路径回退；初始 outline
尝试仍记录成功、失败与预算淘汰原因。展示层的取舍不会反向改变 symbol graph。

## 关系的消费规则

不同用途承担不同的错误成本：

- Repo map 对 CodeGraph 关系做按 diff 个性化的排序，可以使用低置信候选；它只是后续阅读的提示。
- caller/callee 契约消费声明间的 `Exact/Scoped` calls；Unit 分组也使用 references、extends、implements、aliases、exports。repo map 使用声明关系，
  排除 Reference 发出的源码位置边，避免把两个粒度重复计数。
- used 契约按 diff 对应版本的增加行或删除行定位 Reference，沿 references 与 aliases 读取声明或显式导入绑定；
  usage 反向遍历同一链路，并按源码行去重。必要环节均须达到 `Scoped`，不按裸名字匹配 catalog。
- 删除行只在旧侧快照中绑定；没有 hunk 坐标的内部 Fragment 只可使用已知声明范围。
  未解析使用仍在图中，但不作为确定的契约或邻接关系。
- 多个声明落到同一个 CCR 身份时，需要避免把候选集合解释成唯一目标。

这些置信度表示上游支持的静态证据强度，不表示完整编译器类型检查。Go 接口实现的启发式关系、动态
分派和未解析调用，不自动成为 Unit 合并依据。Git 文本搜索仍是 review 工具，搜索结果不写回源码图。

## 文档与外部契约

Documentation 的归属由 CodeGraph 确定，CCR 只处理注释定界符、首段和展示空白。同名方法使用
声明身份区分；单文件展示消费局部图，关系上下文消费共享 RepositoryIndex 的节点文档。

已证明的 Python 具名导入可用公开名称关联外部 contract catalog，即使目标源码不在图内。工作区
模式可从依赖根补读文档材料，仍交给 CodeGraph 分析；历史 ref 模式不读取当前 venv 文档。
外部 Go selector 缺少图中的目标绑定时，接收者拼写和导入路径不足以证明 used 契约，不再猜包名
或补做绑定。源码覆盖缺口应交给 CodeGraph 改进，CCR 保留本轮分析限制。

## 能力边界

语言覆盖随依赖版本演进，使用 CodeGraph 的实际产物和 diagnostics 判断，不能用“有 grammar”推断
“有完整语义关系”。当前接入有以下边界：

- Go、Python、JavaScript/TypeScript 有专用静态分析；其他语言可能仅有声明和 outline。
- JavaScript/TypeScript 的箭头函数绑定以变量声明提供，其改动保留在文件级 residual 中；对象字面量
  中的 callable 不承诺独立符号或调用边。
- 有歧义的扩展名沿用 CodeGraph 的语言选择。外部依赖未提供源码时，不承诺解析其真实包名或关系。
- 普通文档摘要属于评审展示；依赖材料发现和外部 contract catalog 查找由 CCR 负责。

新增源码能力应补在 CodeGraph；CCR 只增加所需的输入上下文、展示或消费策略，并以集成测试验证边界。

## References

- [CodeGraph](https://github.com/compforge/codegraph) — Document 分析、图模型与语言能力
- [`kernel.md`](kernel.md) — Language 在 CCR Kernel 中的位置
- [`unit-model.md`](unit-model.md) — 源码关系如何参与 Unit 与 Clue
- [`harness.md`](harness.md) — 只读源码工具与执行边界
