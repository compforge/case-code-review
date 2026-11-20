# AGENTS.md — case-code-review (`ccr`)

## 项目定位与边界

CCR 是以 Unit 为评审边界、检查改动是否守住既有契约的 AI code review CLI。它基于
[open-code-review](https://github.com/alibaba/open-code-review) 重写并独立演进，衍生归属见 [NOTICE](NOTICE)。
评审效果同时看准确性、完成度和成本。改进效果时结合阶段产物、实际 Session 轨迹、源码事实和项目声明的契约。

## 评审主流程

1. Runner 捕获变更前后的源码快照。Project Knowledge 用 Repository、Component 和 FileRole 解释工程结构，
   并提供作者用 `spec / case / link / rule / doc` 声明的业务契约。Language 接入同一版本的 CodeGraph 图，把声明和关系关联到源码。
2. Formation 调用 RepoKit 将相关改动的 Fragment 组装为 Unit，再将相关项目、源码和契约线索作为 Clue 挂到 Unit。
   Unit 是一次 run 的评审聚合根，持有目标改动，并逐步保存实际读到的证据和阶段结果。
3. Unit Review（R1）通过 Harness 调查 Unit 并提交可证伪的 Hypothesis。Hypothesis Review（R2）在 Lane 中独立复核，形成 Assessment。
   Trial（R3）执行确定性规则，记录裁决并决定是否交付 Finding。完整仓库扫描另走 `scan file → Harness execution → Finding`。

各阶段按单条结果推进。已成熟的 Hypothesis 可以在其他 Unit 仍在调查时进入复核。[评审流程图](docs/review-pipeline.svg)

## 代码地图与核心模块

```text
cmd/ccr/             CLI 入口、参数和契约加载
internal/
  runner/            固定评审快照，编排 formation、unitreview、hypothesisreview 和 trial
  project/           解释 Repository、Component、FileRole，投影项目事实
  material/          在 CodeGraph 的通用材料分类上追加 CCR 标签
  language/          将 CodeGraph 图适配为评审需要的源码身份、范围和关系
  unit/              适配 RepoKit Unit，汇集 Clue、证据快照与阶段结果
  harness/           通过 AgentGo 运行单次 Execution，管理消息、工具、预算与事件
  recording/         保存 Review 业务产物、Operation 和原生 Session 的关联
  viewer/            从录制事实投影 Review、Job 和 Session
  llm/               模型连接配置、token 预算与估算
  config/            prompt 模板、规则和工具配置
eval/                固定样本的效果评测与原始 Session 的轨迹分析
```

## 跨模块约定

- **源码与契约。** RepoKit 拥有 Fragment 分类和 Unit 组装。CodeGraph 的 Node 和 Relation 是消费方的源码事实源。
  图、源码工具和仓库契约按各自的 before/after 快照读取，无法绑定的身份保留为 unknown。
  契约定义、`spec.json` 协议和 `specgen` 由 [spec-case](https://github.com/compforge/spec-case) 维护。
- **评审与执行。** 阶段包负责产生判断，结果追加到来源 Unit。Runner 通过 ExecutionSpec、工具、hook 和事件接入 Harness。
  Harness 负责执行机制，依赖方向从评审领域指向 Harness。评审只读，Finding 是否发布由调用方决定。
- **覆盖与完成。** 组装遇到容量边界时保留目标并报告。每个 Unit 的时间预算覆盖发现、复核排队与裁决。
  退出时汇合仍运行的阶段，保留已完成结果，最终排序独立于完成顺序。未完成的 Unit 和未复核的 Hypothesis
  在输出与录制中标明 partial / incomplete。判定 clean 需要完成证据。
- **记录与观测。** 业务结果和 Operation 归 Review 日志，实际模型输入、回复和工具调用归 AgentGo Session。
  Viewer、export、stats 和 eval 消费同一批事实。一个 Unit 对应一个 JobView，共享 Lane 中的调用按明确的业务关联归属。

## 开发与验证

- 仓库出现可提交的改动时同步递增 `VERSION`。被忽略的本地数据与运行产物不触发版本升级。
- Go 通用操作优先使用标准库或 `go-stdx`。提交 Go 改动前运行 `go build ./...` 和 `go test ./...`。
- 修改 Knowledge、Unit 组装或 Clue 时，核对项目或源码事实的来源、目标改动覆盖和证据相关性。
- 修改 Execution 时，验证终态、资源边界和失败可见性。修改评审策略时，用 Session 和人工标签核对具体判断，
  同时比较 wrong、missed、完成度与成本。

## 主题文档

| 要修改或排查的部分 | 阅读入口 |
|---|---|
| Component、文件角色与作者声明的业务契约 | [Project Knowledge](docs/project.md) |
| review snapshot、symbol-id、CodeGraph 关系与绑定缺口 | [Language](docs/language.md) |
| Fragment 组装、数量和 token 预算、Unit 证据快照 | [Unit 与评审上下文](docs/unit-model.md) |
| R1 发现、R2 复核与 Trial 裁决的输入、交接和终态 | [评审流程](docs/review-pipeline.md) |
| Execution 生命周期、模型、消息、工具与预算 | [Harness](docs/harness.md) |
| 运行记录、Viewer、成本归属与评测 | [可观测性](docs/observability.md) · [评测入口](eval/README.md) |
