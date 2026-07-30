# AGENTS.md — case-code-review (`ccr`)

## 项目定位与边界

`ccr` 是**函数级、契约守恒**的 AI code review CLI，基于 [open-code-review (ocr)](https://github.com/alibaba/open-code-review) 地基重写、独立演进（衍生归属见 `NOTICE`）。两个理念支柱（详见 `README.md`）：

1. **捕获更多 context**——从 diff 定位到改动**函数**，收集它的 caller/callee 邻域 + 作者附着的 **spec/case/rule/link**。
2. **按 *review unit* 触发 review loop**——unit 是评审作用域，粒度是一条阶梯（函数 → 类 → 文件 → 模块/目录）。

> **与 ocr 的本质区别 = 把 unit 当一等概念**。一切按 unit 走：`--spec`(契约) / `--rule`(准则) / `--history`(上轮评审反馈) 都是按 symbol-id 注入的 **per-unit 上下文**，`Clue`/`ClueFinder` 把 context 挂到 unit。ocr 停在 file 级、没有 unit。判断一个新能力是否"对味"，就看它有没有让 unit 更一等。

**三追求 × 三抓手**——评估任何改动方向的坐标系：

- **追求**（互相拉扯，不能单独优化）：①**健壮性**——每个 unit 的 loop 真跑完（大文件不被预算跳过、长链不被超时截断；截断的沉默最危险，长得像"没问题"）；②**准确性**——真问题找得到（不空转交 clean、不编造 finding）；③**成本**——时间/token 有界（跟不上开发节奏，再准也会被绕过）。
- **抓手**（都围着 review loop 这一个核心）：①**loop 能力**——工具面、记忆机制（压缩、超时 wrap-up）；②**loop 粒度**——file → unit，贴近"一次改动 = 一个需求及其相关文件"（函数 → 调用链 → 文件的归并阶梯）；③**loop context**——自捕获的（现场解析、grep、call graph、doc 抽取）追求零采纳成本，外部输入的（spec/case/rule/link、history、需求背景）追求高信号。
- 量测归 `eval/README.md`：质量轴 = 准确性，效率轴 = 成本，截断/超时/未收尾信号 = 健壮性。

**边界**：ccr 是**消费侧引擎**。spec/case/rule/link 资产的**定义、各语言写法、`spec.json` schema、symbol-id 契约**，以及产 `spec.json` 的 **`specgen` 抽取器**（Go + Python 参考实现），都在独立项目 [`spec-case`](https://github.com/qiankunli/spec-case)，**不在 ccr**。ccr 只消费 `spec.json` + 现场解析函数边界。

## 代码地图与核心模块

```
case-code-review/
├── cmd/ccr/        CLI 入口：review/scan/config/… 子命令；组装 Args、加载 spec.json
└── internal/
    ├── language/   ★ 唯一源码语言边界：Analyzer / RepositoryIndex 输出 symbol-id、definition/span、call/reference/doc 与依赖根；专用 parser、go/types 与 gotreesitter 通用 grammar 都封装在内。详见 `docs/language.md`
    ├── unit/       ★ 两类型两阶段：`Fragment`（原子，Splitter 消费 language facts）→ Merger 归并成 `Unit`（评审作用域，WatermarkMerger）；context 抽象 Clue(kind×relation) / ClueFinder / Dossier（merge 后挂 `Unit.Dossier`，去重后喂 loop）。详见 `docs/unit-model.md` + `docs/context-model.md`
    ├── spec/       ★ 消费 spec.json：SpecFinder/RuleFinder/LinkFinder 把 spec/case/rule/link 找成 Clue（廉价 finder）
    ├── history/    ★ 消费 --history（上轮评审 findings，symbol-id keyed）：Finder 挂成 ClueHistory，渲染成"核验是否已修"的 prompt（廉价 finder；评审反馈闭环的消费侧）
    ├── codegraph/  ★ language facts 的图消费层：repo-map 排名 + caller/callee 邻域 Clue + call-chain 邻接；只拥有图算法与评审策略，不解释源码语法。详见 `docs/codegraph.md`
    ├── agent/      ★ 评审编排：split→找 Clue（廉价 + 按预算闸门的昂贵 finder）→merge→每 review unit 一个 loop；按 Clue 渲染上下文；unit 的 Briefing（briefer 协议按 scope 定预载材料：own source / usage-sites / callchain 邻居函数体，共享预算引擎）；loop 收尾每 unit 落一条 **Debrief**（outcome/formed/降级/成本 → session，指标体系的常开采集面见 `eval/README.md`）；file 级 review-filter；`--dry-run` 只装配上下文、不调 LLM
    ├── diff/       diff/hunk 解析、评论行号解析
    ├── msg/        review 领域消息模型：loop 货币 `[]msg.Msg`，wire 格式只在 lowering 边界出现——`docs/message-model.md`
    ├── board/      Review Team v0 共享案情板（gate review_team 默认关）：Bulletin 定向路由 + 增量注入，unit loop 间互通进展——`docs/cross-unit.md`
    ├── llmloop/    agentic 评审 loop（自 ocr 引擎独立演化：会话货币 msg.Msg + 1:1 lowering、wrap-up 截断纪律、file dedup/evict、Outcome 均为 ccr 侧新增——`docs/message-model.md`）
    ├── config/     模板 prompt、rule.json、tools 配置
    └── model · gitcmd · session · telemetry · tool · scan · viewer …   支撑模块
```

**主链路**：

```
diff ─Splitter─▶ Fragment ─Merger─▶ Unit ─ClueFinder 找 Clue(spec/case/rule/link + caller/callee)─▶ review loop
```

> 即：Splitter 把每个文件 diff 切成 `Fragment`（一函数一个 + 残余）→ Merger 归并成 `Unit`（评审作用域）→ 各 ClueFinder **对 Unit** 找 Clue 挂到 `Unit.Clues`（spec/case/rule/link 廉价直查；caller/callee 经 call-graph 上溯/下探）→ 一个 Unit 一个 review loop。context 后置（对最终作用域收一次）。
>
> **Clue / ClueFinder**：context 抽象的三件——找的动作（`ClueFinder.Find(u Unit) []Clue`，对评审作用域 Unit 找）、找的结果（`Clue{Kind, Text, Ref}`，Text 内联 / Ref 按需指针）、挂哪（`Unit.Clues`，merge 后收）。加一类 context = 加一个 finder，不动主链路。

## 关键约定（核心六条）

1. **评审语义 = 契约守恒，不是找语法 bug**：核对 diff 有没有破坏函数的 spec/case/rule 不变量；**语法 / 静态检查交给 lint 类工具**（Python `ruff`、Go `go build`/`go vet` 之类），不是 ccr 的活。
2. **diff unit vs review unit 别混**：Splitter 从 diff 切出 diff unit；Merger 归并成 review unit（触发 loop 的那个，可能是单个、也可能是几个沿阶梯归并的更粗 unit）。成本超水位按文件归并——**降 loop 粒度、不降 context**。
3. **边界现场算、`spec.json` 只语义**：函数边界评审时由 `internal/language` 现场解析、**永不落盘**（不 stale）；parser / compiler / gotreesitter 不得泄漏到 unit/codegraph，使用方只消费语言事实；`spec.json` 只有 `FuncID → spec/cases/rules/links`、**无行号**；join key 是 symbol-id `<relpath>::<symbol>`（与 spec-case 一致）。
4. **上下文分廉价 / 昂贵两档，重活有闸**：廉价 finder（spec.json 查 spec/case/rule/link）总跑；昂贵 finder（caller/callee 的 call-graph grep）走**预算闸门**——diff unit 数超水位就跳（反正要归并、per-func 上下文也被稀释）。link 指向的 doc/函数**内容**仍按需 tool 取，不预塞。

5. **通用操作不就地手写**：先查 stdlib（`slices`/`maps`/内置 `min`/`max`），stdlib 没有的查/进 [`go-stdx`](https://github.com/qiankunli/go-stdx)（自 `pkg/stdx` 孵化毕业，收录纪律见其 AGENTS.md）；第三方 common 库（samber/lo、bytedance/gopkg 等）已评估过暂不引——出现第三个"纯 transform 链"调用点再议。

6. **review loop 结构上只读——是受守护的信任边界**：主评审 loop 的工具面是封闭只读集（`file_read`/`code_search`/`file_find`/`file_read_diff` 全走 git 只读动词，`code_comment` 仅写内存 collector，`task_done` 是控制信号），无 shell/exec、无 git 写动词、无文件写、`file_read` 有仓根沙箱防穿越。**给评审 loop 新增任何能写文件 / 改仓库状态 / 跑 shell 的工具，即破坏这条边界**——评审节点一旦能产生副作用，就从"看代码的"变成"改状态的"（业界事故：reviewer 执行 checkout 制造孤儿提交）。要加此类能力先问：它属于评审，还是属于评审之后的独立执行环节？后者不进这个工具集。

> 另：Go 改动先 `go build ./...` / `go test ./...` 再提交。

## References

- 理念：`README.md` · `README.zh-CN.md`
- spec/case/rule/link 资产、各语言写法、`spec.json` schema、symbol-id 契约、**产 `spec.json` 的 `specgen`**（Go + Python）：[`spec-case`](https://github.com/qiankunli/spec-case)
- 查覆盖 / 调试：`ccr review --dry-run` 打印每个 review unit 装配的上下文，不调 LLM（端到端：marker → specgen → spec.json → `--dry-run`）
- Unit 模型：`Fragment` 原子 + `Unit` 作用域、两条合并轴（call-chain 语义 / file 成本）、clue 后置——`docs/unit-model.md`
- 源码语言边界：Analyzer / RepositoryIndex、symbol-id owner、后端隔离与降级——`docs/language.md`
- Context 模型：unit → dossier——`Clue`(kind: spec/case/rule/link/doc) × `Relation`(self/owner/caller/callee/used) 两轴正交、doc 运行时抽取（adoption-free）、symbol-id 仓内 / fqn 跨仓、依赖 spec 随包发——`docs/context-model.md`
- Review Team（设计定稿，v0 待实现）：Board/Bulletin/动态 cross_check，治跨文件一致性漏报；固定 phase 碰头会与角色化均已论证否决——`docs/cross-unit.md`
- Review Run 绿地改造（待实施）：ReviewRun / UnitExecution 生命周期、总 token 预算与 Unit 级断点恢复——`docs/run-model.md`
- 上游归属（Apache-2.0 衍生）：`NOTICE`
