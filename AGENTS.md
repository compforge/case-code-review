# AGENTS.md — case-code-review (`ccr`)

## 项目定位与边界

`ccr` 是以 **Unit 为一等评审边界、以契约守恒为目标**的 AI code review CLI，基于
[open-code-review (ocr)](https://github.com/alibaba/open-code-review) 地基重写、独立演进
（衍生归属见 `NOTICE`）。稳定主链路是：

```text
Project / Language facts ─▶ Unit ─Unit Review─▶ Hypothesis
    ─Hypothesis Review in Lane─▶ Assessment ─Trial (Review 3) gate─▶ Finding
```

Project Knowledge 既解释 Repository / Component / FileRole 等结构，也包含用
`spec / case / link / rule / doc` 表达的 Biz Knowledge；Language Knowledge 提供源码事实，并负责把
这些声明绑定到代码身份。两者共同参与 Unit formation 或按作用域向 Unit Review / Hypothesis Review
提供上下文。CCR 的 Unit 让相关改动得到完整、有界的共同评审，按源码关系组织行为边界并减少重复探索。

CCR 不追求通读仓库或穷举所有问题，而是在相关、有界的上下文内发现具体缺陷。任何优化都要同时观察
**健壮性、准确性、成本**：loop 必须真实完成，结论必须有证据，时间、token 和工具调用必须有界。

效果演进依靠三项长期能力共同推进：成熟稳定的 Review Pipeline 保证阶段结果逐条流动且可完成；
Session 轨迹分析持续校正 prompt、工具取舍与 schema；Language（接入 CodeGraph）与 Project Knowledge
持续提供更可靠的源码、项目结构和业务契约事实。不能只靠扩大模型上下文或堆叠 prompt 提升效果。

`ccr` 是 contract 资产的消费侧引擎；spec/case/rule/link 的定义、`spec.json` 协议和 `specgen`
抽取器由独立项目 [`spec-case`](https://github.com/compforge/spec-case) 持有。

## 代码地图与核心模块

```
case-code-review/
├── cmd/ccr/        CLI 入口：review/scan/config/… 子命令；组装 Args、加载 spec.json
└── internal/
    ├── runner/     ★ 顶层编排；`formation` 形成 Unit，`unitreview` 产生 Hypothesis，`hypothesisreview` 产生 Assessment，`trial` 确定性地产出 Finding
    ├── project/    Repository、manifest 定义的 Component 与可组合 FileRole；提供项目结构知识，决定 source 进入 Unit，并把 entrypoint/handler、manifest/lock 投影为项目 Clue。详见 `docs/project.md`
    ├── language/   ★ CodeGraph 接入边界：选择 review snapshot 的 Document，共享分析缓存与图，从 Node + Relation 投影 symbol-id、源码范围、outline、文档与契约关联；解析和关系绑定由独立 CodeGraph 项目持有。详见 `docs/language.md`
    ├── unit/       ★ `change.Change`→`Fragment`→`Unit` 及其评审知识；`spec`/`history`/`sourcecontext` 子包沿 relation 将 Clue 汇入 Unit，再由 Runner 组装评审消息。详见 `docs/unit-model.md`
    ├── harness/    ★ 通用执行域：适配 agentgo 的 loop、工具 hook、上下文与事件；`msg`/`tool`/`session` 提供执行机制，不依赖 Runner/Unit/Finding。`board` 是默认关闭的试验能力，`llmloop` 作为旧实现隔离保留。详见 `docs/harness.md`
    ├── llm/        基础模型 client、provider 协议与 token 估算；作为稳定基础设施平铺
    ├── config/     模板 prompt、rule.json、tools 配置
    └── gitcmd · telemetry · viewer …   独立支撑能力
```

**主链路**：

```
git change ─▶ Change ─Component/FileRole─▶ source ─Splitter─▶ Fragment ─Formation─▶ Unit
                                      └─▶ entrypoint/handler、manifest/lock ─▶ project Clue
    ─ClueFinder 找 Clue─▶ Unit Review ─▶ Hypothesis
    ─Lane─▶ Hypothesis Review ─▶ Assessment ─Trial (Review 3)─▶ Finding
full scan ─▶ scan file ─▶ Harness execution ─▶ Finding
```

Formation 把 Change 切成 Fragment，再按行为关系形成 Unit；Unit 是一次 run 的评审聚合根，先持有
Fragments / Clues，随后追加实际读取的文件、相关 diff、搜索结果以及 Hypothesis、Assessment 与 Trial decision。Runner
把 Unit 投影为评审消息，Harness 执行 Review loop，但不拥有这些评审领域状态。

## 关键约定

1. **Knowledge owner 唯一**：Project Knowledge 包含 Repository / Component / FileRole 等结构事实和
   `spec / case / link / rule / doc` 等 Biz Knowledge；CodeGraph 拥有源码解析与关系绑定，Language 负责 CodeGraph 接入和 CCR 身份适配，Runner 固定 graph、源码工具与仓库契约共用的输入视图；
   Unit 拥有一次 run 的行为作用域、完整事实快照与阶段结果；Harness 只拥有 Execution 机制，各 Review
   阶段只拥有产生结果的逻辑，依赖方向不得反转。
2. **Unit 以相关改动为边界**：改动前后分别定位源码归属，每个改动文件先形成一个 Unit，再按已改动声明的高置信图关系合并整个 Unit。
   同文件改动始终一起评审，Fragment 保留源码身份和范围；Unit 数量天然不超过改动文件数。
   缺少图事实时保留未绑定目标，预算切断的关系保留为上下文；无法在预算内评审的目标显式报告 incomplete。
   上下文能力不由 Unit 的展示形状决定。
3. **发现、复核、裁决分离**：Unit Review 只产生 Hypothesis，Hypothesis Review 形成 Assessment，Trial（Review 3）用确定性
   规则决定 Finding；成熟结果逐条向下游流动，不设置全局阶段屏障。partial / incomplete 必须显式存在，不能把 0 Finding 自动解释为 clean。
4. **Review Execution 有界、只读、可观测**：确定上下文先作为评审消息注入，未知事实再通过只读工具补证；
   AgentGo 只存在于 Harness 边界内，Session JSONL 必须记录实际 prompt、response、工具与完成状态。
   Session 唯一持有整轮 timeline：Stage 是执行身份、层级与生命周期事实，内容记录通过 Stage ID 关联，不并存另一套边界和计时。
   Runner、Harness 和模型客户端只记录自己拥有的阶段；Viewer、export、stats 与 eval 从共同记录投影，按请求身份归属时间和 token，保留缺口和并发关系；成本不依赖 debrief 是否生成。
5. **事实源不重复**：CodeGraph 的 Node + Relation 是消费侧唯一源码事实。Facts 只在构图入口使用；caller/callee、owner、used、usage 与文档共享各自版本的 review snapshot，不按裸名或文本扫描补做绑定；contract schema / 生成器归 `spec-case`，发布版本归 `VERSION`。
   只要产生可提交的仓库改动，就同步递增 `VERSION`；ignored 的本地数据与运行产物不触发版本升级。
   Go 通用操作优先 stdlib / `go-stdx`；Go 改动提交前运行 `go build ./...` 与 `go test ./...`。

## References

- 理念：`README.md` · `README.zh-CN.md`
- 内核分层与依赖方向：Project 组织项目事实、Language 接入源码事实、Unit 汇总评审知识、Harness 执行——`docs/kernel.md`
- Harness 执行模型：Execution 生命周期、Agent Loop、上下文管理、预算、工具扩展点、Session JSONL
  与 HTML Viewer 可观测性——`docs/harness.md`
- 可观测性：Session JSONL 是共同事实源，Viewer 用于单次运行诊断，eval 用固定数据，让 Detector、
  Verifier 与 Measurer 分别承担行为发现、契约判断和事实测量——`docs/observability.md`
- spec/case/rule/link 资产、`spec.json` 协议与 `specgen`：[`spec-case`](https://github.com/compforge/spec-case)
- 项目知识：Repository / Component / FileRole 等结构知识、作者声明的 Biz Knowledge 及其投影——`docs/project.md`
- Unit 与上下文：`Fragment` / `Unit` 作用域、Clue 两轴上下文与图事实消费——`docs/unit-model.md`
- 源码语言边界：Analyzer / RepositoryIndex、symbol-id 适配、CodeGraph 消费与覆盖边界——`docs/language.md`
- Unit Review：有界探索、增量提交、anytime 完成、上下文治理与效果优化；默认关闭的 Board/Bulletin
  作为试验特性单独记录——`docs/unit_review.md`
- Hypothesis Review：Lane、四轴 Assessment、evidence receipt、prior delivery 与确定性 Trial（Review 3）
  ——`docs/hypothesis_review.md`
- 上游归属（Apache-2.0 衍生）：`NOTICE`
