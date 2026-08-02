# case-code-review (`ccr`)

> 围绕行为 Unit、两次 Agent Review、确定性 Trial 和 Language / Project Knowledge 构建的 Agentic Code Review CLI。基于 [open-code-review](https://github.com/alibaba/open-code-review) 演进。｜ English: [README.md](./README.md)

## 理念

Code Review 不是“把每个改动文件分别交给模型”。文件边界表达的是存储位置，不一定是行为边界；只看 diff 往往太窄，通读整个仓库又通常太贵，而且仍然无法推断没有表达出来的业务规则。

ccr 围绕三个原则构建：

1. **评审行为，而不是文件。** 一次评审应覆盖能够解释改动及其影响的最小作用域，ccr 将它称为 **Unit**。
2. **把发现与验证分开。** 发现一个可能的问题是开放式任务；验证它是否真实、是否由当前改动造成、是否值得行动、是否重复则更收敛。不同任务应由不同 agent loop 承担。
3. **提供相关 Knowledge，而不是最大 Context。** Language 结构和 Project 结构共同决定哪些改动应放在一起，以及哪些邻近证据值得交给模型。

ccr 不追求穷举所有缺陷，而是在有界的 agent 探索中发现实现需求时容易忽略的具体问题，例如 caller 假设被破坏、边界处理、错误路径和 API 误用。语法问题仍归 lint；隐含业务约束仍需要显式需求背景或作者提供的 Knowledge。

![CCR 从 Diff 到 Finding 的 Agentic Review 流水线](docs/review-pipeline.svg)

## 核心模型

### Unit：评审边界

一个 **Unit** 是一次行为评审的作用域。根据改动形态，它可以是：

- 自然边界明确时的一个**函数**；
- 整个 diff 只有一个需评审文件时的一个**文件**；
- 多个改动函数彼此协作、分开评审会重复探索时的一条**跨文件调用链**。

因此 Unit 比逐文件评审更灵活。实践目标是 Review 1 loop 数不多于需评审文件数；当跨文件改动存在明确关系时，用更少的 loop 一起理解它们。

Unit 能取代固定的文件粒度成为基本评审单元，一个重要前提是 caller/callee 关系已经具备实用的分析能力；这受益于 [`gotreesitter`](https://github.com/odvcencio/gotreesitter) 在跨语言语法解析、符号定位和调用分析上的持续成熟。

### Review 1 发现，Review 2 验证，Review 3 门禁

| 阶段 | 职责 | 产出 | 公检法类比 |
|---|---|---|---|
| **Unit Review（Review 1）** | 探索 Unit、沿证据调查、提出可能的缺陷 | `Hypothesis` | 公安侦查并提出案件假设 |
| **Hypothesis Review（Review 2）** | 独立核对源码、diff、baseline、影响、归因与重复性 | `Assessment` | 检察院复核指控是否有证据支持 |
| **Trial（Review 3）** | 执行确定性的交付门禁 | `Finding` 或驳回 | 法院门禁决定什么可以交付 |

这个类比只用来解释职责分离，并不是在代码里模拟法律系统。Review 1 鼓励发现，Review 2 鼓励证伪弱假设；**Review 3 是 Trial 的别名**，不是第三个 agent loop，而是用确定性规则决定是否交付。三个阶段也借“吾日三省吾身”作记忆点：结论在交付前要经过反复审视。

### Language Knowledge 与 Project Knowledge

两类 Knowledge 共同支持 Unit formation 和两个评审阶段：

| Knowledge | 提供什么 |
|---|---|
| **Language Knowledge** | 语法感知的 symbol、span、definition、reference、call、import、源码文档，以及作者契约的抽取与匹配 |
| **Project Knowledge** | Repository / Component 边界、文件角色、entrypoint、handler、manifest、lock 和其它项目约定 |

spec、case、rule、link、源码文档等上下文不全是“语言知识”，但它们的抽取和定位强依赖语言语法分析。[`spec-case`](https://github.com/qiankunli/spec-case) 仍是可选的契约编写与分发方式，而不再是 ccr 的产品前提；没有采用它的仓库也能使用 ccr。

设计展开：[`Kernel`](docs/kernel.md) · [`Unit`](docs/unit-model.md) · [`Unit Review`](docs/unit_review.md) · [`Hypothesis Review`](docs/hypothesis_review.md) · [`Language`](docs/language.md) · [`Harness 与可观测性`](docs/harness.md)

## 使用

### 安装

```bash
git clone https://github.com/compforge/case-code-review && cd case-code-review
make install        # 安装 `ccr` 到 ~/.local/bin；macOS 自动重签名
# 或：go install github.com/qiankunli/case-code-review/cmd/ccr@latest
```

### 配置模型

```bash
ccr config provider     # 选择或新增 provider
ccr config model        # 选择 model
ccr llm test            # 验证连通性
```

配置位于 `~/.casecodereview/config.json`。内置 provider 和 OpenAI-compatible 自定义 provider 也可以非交互配置，详见 `ccr config --help`。

### 评审改动

```bash
ccr review                              # staged + unstaged + untracked 改动
ccr review --from main --to my-branch  # 分支相对 merge base
ccr review --commit abc123              # 单个 commit 相对第一父提交
ccr review --background "需求背景"      # 注入业务或需求上下文
ccr review --format json                # 面向 CI / bot 的机器可读输出
```

连续评审 PR/MR 时，可用 `--history prior.json` 传入之前已经交付的 Finding。Forge comments 是持久事实源；调用方在每个 revision 拉取它们，使 ccr 能区分新问题与重复交付。

### 花 Token 前先检查

```bash
ccr review --preview            # 改动文件及其评审 / 排除角色
ccr review --dry-run            # 不调用 LLM，查看形成的 Unit 和装配 Context
ccr review --dry-run --format json
```

### 观察一次运行

```bash
ccr viewer                      # session、token/time/tool 总览、prompt 与决策轨迹
```

Session JSONL 持久化真实 message、模型回复、工具调用、阶段产物、warning 和完成状态；Viewer 将它们组织成 run 级统计和每个 loop 的时间线，用于分析效果、成本与未完成评审。

### 可选作者 Knowledge 与 Feature Gate

把生成的 `spec.json` 放到 `.casecodereview/spec.json`、通过 `--spec` 传入，或配置用户级契约，即可补充 spec/case/rule/link。具名 feature gate 可用于消融：

```bash
ccr review --feature caller_callee=off
ccr review --feature callchain=off
ccr review --feature doc=off
```

完整命令和 feature 列表见 `ccr review --help`。

## 状态

活跃开发中。当前基础包括：项目感知 FileRole、语言分析、Unit formation、两次 Agent Review 与确定性 Trial、跨 revision history、有界 Agent 执行和可观测 Session Viewer。

## License

Apache-2.0（见 `LICENSE` / `NOTICE`）。
