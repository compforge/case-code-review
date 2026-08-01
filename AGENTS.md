# AGENTS.md — case-code-review (`ccr`)

## 项目定位与边界

`ccr` 是以 **Unit 为一等评审边界、以契约守恒为目标**的 AI code review CLI，基于
[open-code-review (ocr)](https://github.com/alibaba/open-code-review) 地基重写、独立演进
（衍生归属见 `NOTICE`）。稳定主链路是：

```text
Project / Language Knowledge ─▶ Unit ─Review 1─▶ Hypothesis
    ─CaseFile / Review 2─▶ Assessment ─Trial─▶ Finding
```

Project Knowledge 解释 Repository / Component / FileRole，Language Knowledge 提供源码事实；它们共同
参与 Unit formation，并按作用域向 Review 1 / Review 2 提供上下文。相对 OCR 固定按 file 发起 loop，
CCR 的 Unit 可以是函数、文件或跨文件 call-chain，以行为边界组织评审并减少重复探索。

CCR 不追求通读仓库或穷举所有问题，而是在相关、有界的上下文内发现具体缺陷。任何优化都要同时观察
**健壮性、准确性、成本**：loop 必须真实完成，结论必须有证据，时间、token 和工具调用必须有界。

`ccr` 是 contract 资产的消费侧引擎；spec/case/rule/link 的定义、`spec.json` 协议和 `specgen`
抽取器由独立项目 [`spec-case`](https://github.com/qiankunli/spec-case) 持有。

## 代码地图与核心模块

```
case-code-review/
├── cmd/ccr/        CLI 入口：review/scan/config/… 子命令；组装 Args、加载 spec.json
└── internal/
    ├── runner/     ★ 顶层编排；`formation` 形成 Unit，`unitreview` 产生 Hypothesis，`hypothesisreview` 产生 Assessment，`trial` 确定性地产出 Finding
    ├── project/    Repository、manifest 定义的 Component 与可组合 FileRole；提供项目结构知识，决定 source 进入 Unit，并把 entrypoint/handler、manifest/lock 投影为项目 Clue
    ├── language/   ★ 唯一源码语言边界：Analyzer / RepositoryIndex 输出 symbol-id、definition/span、call/reference/doc 与依赖根；专用 parser、go/types 与 gotreesitter 通用 grammar 都封装在内。详见 `docs/language.md`
    ├── unit/       ★ `change.Change`→`Fragment`→`Unit` 及其评审知识；`spec`/`history`/`codegraph` 子包沿 relation 将 Clue 汇入 Unit，再投影为 Briefing。详见 `docs/unit-model.md`
    ├── harness/    ★ 通用执行域：适配 agentcore 的 loop、工具 hook、上下文与事件；`msg`/`tool`/`board`/`session` 提供执行机制，不依赖 Runner/Unit/Finding。`llmloop` 作为旧实现自包含保留，仓库其他包不再依赖。详见 `docs/harness.md`
    ├── llm/        基础模型 client、provider 协议与 token 估算；作为稳定基础设施平铺
    ├── config/     模板 prompt、rule.json、tools 配置
    └── gitcmd · telemetry · viewer …   独立支撑能力
```

**主链路**：

```
git change ─▶ Change ─Component/FileRole─▶ source ─Splitter─▶ Fragment ─Merger─▶ Unit
                                      └─▶ entrypoint/handler、manifest/lock ─▶ project Clue
    ─ClueFinder 找 Clue─▶ Unit Review ─▶ Hypothesis ─▶ CaseFile
    ─▶ Hypothesis Review ─▶ Assessment ─Trial─▶ Finding
full scan ─▶ scan file ─▶ Harness execution ─▶ Finding
```

Formation 把 Change 切成 Fragment，再按行为关系形成 Unit；Unit 持有 target Fragments 与相关 Clues，
Briefing 只是本轮上下文投影。Harness 执行 Review loop，但不拥有 Unit、Hypothesis 或 Finding。

## 关键约定

1. **Knowledge owner 唯一**：Project 拥有 Repository / Component / FileRole；Language 拥有源码事实与
   稳定身份；Unit 拥有行为作用域和 Clues；Harness 只拥有 Execution 机制，依赖方向不得反转。
2. **Unit 不等于文件**：只有一个 target 文件时收为一个 File Unit；多文件改动按高置信行为关系形成
   call-chain Unit。目标是 Review 1 loop 不多于需评审文件数，同时不靠错误合并牺牲准确性。
3. **发现、复核、裁决分离**：Review 1 只产生 Hypothesis，Review 2 形成 Assessment，Trial 用确定性
   规则决定 Finding。partial / incomplete 必须显式存在，不能把 0 Finding 自动解释为 clean。
4. **Review Execution 有界、只读、可观测**：确定上下文先进入 Briefing，未知事实再通过只读工具补证；
   AgentCore 只存在于 Harness 边界内，Session JSONL 必须记录实际 prompt、response、工具与完成状态。
5. **事实源不重复**：源码事实现场解析，contract schema / 生成器归 `spec-case`，发布版本归 `VERSION`。
   Go 通用操作优先 stdlib / `go-stdx`；Go 改动提交前运行 `go build ./...` 与 `go test ./...`。

## References

- 理念：`README.md` · `README.zh-CN.md`
- 内核分层与依赖方向：Project / Language 产事实、Unit 汇总评审知识、Harness 执行——`docs/kernel.md`
- Harness 执行模型：Execution 生命周期、Agent Loop、上下文管理、预算、工具扩展点、Session JSONL
  与 HTML Viewer 可观测性——`docs/harness.md`
- spec/case/rule/link 资产、`spec.json` 协议与 `specgen`：[`spec-case`](https://github.com/qiankunli/spec-case)
- Component、Unit 与上下文：`FileRole`、`Fragment` / `Unit` 作用域、Clue 两轴上下文、图事实消费与 Briefing
  ——`docs/unit-model.md`
- 源码语言边界：Analyzer / RepositoryIndex、symbol-id owner、后端隔离与降级——`docs/language.md`
- Unit Review：Review 1 的有界探索、原子完成、上下文治理与 Board/Bulletin 跨 Unit 协作
  ——`docs/unit_review.md`
- Hypothesis Review：CaseFile、四轴 Assessment、evidence receipt、prior delivery 与确定性 Trial
  ——`docs/hypothesis_review.md`
- 上游归属（Apache-2.0 衍生）：`NOTICE`
