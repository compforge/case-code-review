# case-code-review (`ccr`)

> AI 代码评审 CLI——在 [open-code-review](https://github.com/alibaba/open-code-review) 的基础上继续深化。｜ English: [README.md](./README.md)

## 理念

只看 diff 不足以做好评审——diff 说不出这次改动有没有破坏它服务的需求、有没有影响依赖它的代码。ccr 的两个核心想法:

**1. 捕获更多上下文。** ccr 从 diff 定位到被改动的**函数**,再沿着有类型的关系、从两个来源收集 diff 本身不携带的上下文(见下节)。

**2. 按 *unit* 评审,带归并。** *unit* 是一次 review loop 的作用域,粒度是一条阶梯:**函数 → 类 → 文件 → 模块/目录**。只有一个文件改动时,ccr 保持一个 File Unit;多个文件改动时,先定位改动函数,再按策略**归并**成真正触发 loop 的 unit:沿调用链一起改的函数并成一个跨文件 unit,大范围改动则并粗到文件级。这样既保留语义焦点,也避免无谓增加 loop——一个归并后的 unit 一个 loop。

diff review 中,Unit review 负责发散调查,产出可证伪的 Hypothesis,不直接发布评论。随后独立的证据 Review 把本次变更作为一个案卷,用只读代码工具逐条核实或排除 Hypothesis,只交付证据支持且值得行动的 Finding。

回报:ccr 能找到**需要背景才看得出来**的 bug——悄悄破坏 caller 假设的改动、违反 diff 里看不见的不变量——按函数真实契约逐条核对。(语法问题归 lint 管。)

评审质量盯三个互相拉扯的追求——**健壮性、准确性、成本**;落地手段以 review loop 为核心,分能力 / 粒度 / context 三个抓手。展开见 `AGENTS.md`。

![CCR 从 Diff 到 Finding 的评审流水线](docs/review-pipeline.svg)

## 上下文模型:证据种类 × 关系

ccr 为每个 review unit 汇集一份证据卷(dossier)。一条线索(clue)= 一种**证据种类**,经某种**关系**到达——两条正交轴(详见 `docs/context-model.md`):

| 种类 | 是什么 | 来源 |
|---|---|---|
| **spec** | 符号的契约(必须保证什么) | authored(`spec.json`) |
| **case** | 要核对的具体场景 | authored |
| **rule** | 审查准则——盯什么 | authored |
| **link** | 作者策展的 "see also"——文档或另一个函数 | authored |
| **doc** | 符号的 docstring / doc 注释 | **运行时从源码抽取** |

| 关系 | 到达的符号 |
|---|---|
| **self** | 被改动的符号本身 |
| **owner** | 所属类/类型(改方法时看到类的契约) |
| **caller** | 谁在用它——上溯到最近带 authored spec 的祖先(治理契约) |
| **callee** | 它依赖什么——直接被调方的契约 |
| **used** | diff 里引用到的类型/函数(经 import 解析,同名符号可消歧) |

两个值得知道的性质:

- **`doc` 零采纳成本。** authored 四类需要 [`spec-case`](https://github.com/qiankunli/spec-case) 标注;`doc` 在评审时直接从源码抽取(Python docstring、Go doc 注释)——**包括依赖的源码**。一个从没听说过 spec-case 的仓,五个关系上照样有契约上下文。
- **跨仓靠 fqn。** 仓内符号用 `relpath::symbol` 寻址;依赖把自己的 `spec.json` 随包发(Go module cache / Python site-packages),其条目**只按 fqn**(`import路径.符号`)匹配、经你的 import 解析——所以 diff 用到某框架类型时,框架标的"仅 per-request"规则会命中。

## 使用

### 安装

```bash
git clone https://github.com/qiankunli/case-code-review && cd case-code-review
make install        # 构建并安装 `ccr` 到 ~/.local/bin(macOS 自动重签名)
# 或:go install github.com/qiankunli/case-code-review/cmd/ccr@latest
```

### 配置 LLM

配置存于 `~/.casecodereview/config.json`。交互式:

```bash
ccr config provider     # 选内置 provider 或添加自定义(url / protocol / api_key)
ccr config model        # 为当前 provider 选模型
ccr llm test            # 验证连通性
```

非交互(CI / 脚本):

```bash
ccr config set provider anthropic
ccr config set providers.anthropic.api_key $ANTHROPIC_API_KEY
ccr config set providers.anthropic.model claude-sonnet-4-6
```

自定义 provider(私有网关、OpenAI 协议端点)支持 `url`、`protocol`、`extra_body`、`extra_headers`、`timeout_sec` 和 `models` 列表——见 `ccr config --help`。

### 评审

```bash
ccr review                              # 工作区:staged + unstaged + untracked
ccr review --from main --to my-branch  # 分支 vs 基线(merge-base 模式)
ccr review --commit abc123              # 单个 commit vs 其父
ccr review --format json                # 机器可读输出(CI、bot)
ccr review --background "$(cat mr.md)"  # 注入需求/业务背景以提准
ccr review --history prior.json         # 上轮 findings,对新 diff 复核
```

连续评审 PR/MR 时,Forge comments 才是持久 history。每个 revision 由调用方临时拉取当前
PR/MR comments,生成一次性的 `prior.json` 并传给 ccr；devloop 与 review-harness 会自动完成。
key 优先用 symbol-id；Forge 只保留文件锚点时可退化为仓库相对 path,例如
`{"path/to/file.go::Symbol":[{"msg":"prior finding","sha":"abc123"}]}`。

### 花 token 之前先看装配

两者都不调 LLM:

```bash
ccr review --preview            # 哪些文件会被评审 / 被排除
ccr review --dry-run            # + 每个 unit 装配好的完整上下文(LLM 将看到什么)
ccr review --dry-run --format json   # + 结构指标:unit/scope 计数与 clue_coverage
                                     #   矩阵(关系/种类,如 owner/rule、callee/doc)
```

`--dry-run --format json` 是免费的 A/B 层:对比两次运行的指标,不花一次 LLM 调用就能看清某个特性或某份 spec.json 带来了什么。

### Feature gates(消融)

每项能力都有具名开关,默认**全开**。关掉一个,测它的边际效果(leave-one-out):

```bash
ccr review --feature doc=off             # 关 derived docstring 线索
ccr review --feature caller_callee=off   # 关 call-graph 邻域
ccr review --feature callchain=off       # 关跨文件调用链 unit
```

kind 门(`spec_case` / `rule` / `link` / `doc`)把一种证据在**所有关系**上一起开关;`caller_callee` 是 call-graph 遍历的成本门。也可经 config 的 `features:{}` 或 `CCR_FEATURES` 环境变量设置。完整列表见 `ccr review --help`。

### authored 契约(可选,推荐)

用 [`spec-case`](https://github.com/qiankunli/spec-case) 标注函数/类(Go doc 注释 marker / Python 装饰器),用其 `specgen` 产出 `spec.json`,放到 `.casecodereview/spec.json`——ccr 自动加载(另有 `~/.casecodereview/spec.json` 与 `--spec path`,高优先层胜)。依赖包内随包发的 `spec.json` 自动发现、按 fqn 匹配。

### 其它

```bash
ccr scan                        # 全文件评审,不需要 diff(--path 缩小范围)
ccr rules                       # 查看哪些评审规则作用于哪些路径
ccr viewer                      # WebUI:浏览历史评审 session、每个 unit 的 prompt 与回复
```

## 状态

活跃开发中。已有:函数级切分(Go + Python)、调用链归并 + 成本调控、上述完整的 种类×关系 上下文模型(authored + derived、本仓 + 依赖)、feature gates、dry-run 指标、上轮 findings 复核、session viewer。

## License

Apache-2.0(见 `LICENSE` / `NOTICE`)。
