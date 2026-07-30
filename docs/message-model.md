# Message 模型：review 领域消息 + 单一 lowering 点

> 设计 spec。现状：PR A（passthrough 货币替换）已落地；typed 消息按消费者逐个引入。参考实现：pi 的 `coding-agent/core/messages.ts`（领域消息 + `convertToLlm` 单点转换 + 标准 role 直通）。

## 理念

LLM API 的 wire 格式只有 user / assistant / system / tool 四种 role，**role 抹掉了内容的身份**：一个文件的源码一旦被拍平进 user 字符串，就再也没法把它和指令区分开——不能对后来的 file_read 去重、不能在 context 紧张时按"可再生性"驱逐（文件内容随时可从磁盘重取→最先逐；assistant 推理不可再生→最后动）、不能为不同 provider 换渲染形态（user 文本 vs tool_result）。

所以 review loop 的会话货币是**领域消息 `[]msg.Msg`**（内容"是什么"：unit 的交底、一个文件的源码、板上的通报……），wire 格式只在 **lowering 边界**（`msg.Lower`，紧贴 API 调用）出现。渲染决策从组装期的字符串拼接，变成 lowering 处的 per-type 策略。

## 现状与演进（passthrough 先行，类型跟着消费者走）

- **PR A（已落地）**：`internal/harness/msg`——`Msg` 接口 + `Raw` 直通 + `Lower()`；`llmloop.RunPerFile` 与 compression 链的货币换成 `[]msg.Msg`，**wire 输出逐字节不变**（round-trip 等价测试保证）。plan / review-filter / relocation / scan 的单发调用保持 lowered 形态——它们没有 loop 货币问题。
- **PR B（已落地）**：`msg.File`（file_read 结果带 path+range 身份，从结果头解析——那是工具的输出契约）+ 第一个消费者 **file_dedup**（gate）：后读覆盖先读 → 先读原地 stub 成一行指针，**保位置、保 tool_call_id**（1:1 不变量与协议配对都不破）。dogfood 验证：真实流量零误伤（不同文件/不覆盖的区间不动）；loop 内重复本就比跨 unit 重复少——跨 unit 的同文件重复读（实测同 run 两个 unit 各读一次 `stdout.go`）是 P3 案情板的地盘，不是 loop 内去重的。
- **C1 按可再生性驱逐（已落地）**：**file_evict**（gate）——warn 阈值（80% MaxTokens）超限时，先把最老的未 stub File 逐个 stub（内容可从磁盘重取，驱逐是确定性、零成本、可恢复的），不够再走 LLM compression。stub 文本按 reason 分两种指针：superseded 指向下文的新副本，evicted 告诉模型 file_read 可取回。驱逐次序 = 可再生性排序的第一档；board 消息（可从板面重拉）与 assistant 推理（不可再生）的档位等相应类型引入时再接。
- **C2 briefing 预载 File 化（已落地，gate `typed_briefing` 默认关）**：消息形状变为 **[system, task, own Files…, related Files…]**——File 必须在 task **之后**（compression 冻结 `messages[0:2]` 为 [system, task] 并向 index 1 追加摘要，中间不能插东西）；template 的源码槽位改填指针文本（自定义模板照常替换）。预载成为 `msg.File` 后 file_dedup/file_evict 自动覆盖它——"模型有全文仍去 ranged read"由 dedup 直接吃掉。降级链不变（先丢 related Files 再丢 own Files → 哨兵文本）。**已翻默认开**（回归集 A/B，eval/README §9：70 unit/arm，timeout 9→3、成本持平、无质量回退证据）；「flip 后清理清单」自此可执行。
- **briefing 其余区 typed 化（spec/usage/repo_map 等）**：等出现消费者再做（YAGNI）。
- **Board / Bulletin 消息**：P3 立项时再加（见 `docs/cross-unit.md`），消息分级（intent/observation/confirmed）、路由键（symbols/paths）都是字段而非文本约定。

## 关键设计

0. **File 的身份 = 内容 + 配对，不含形态**：`File{Path,Range,Content,toolCallID}` 不存 wire 消息；tool_result vs user 文本是 `Lower()` 的渲染决策——这是将来 per-provider 形态 A/B 的前提（若形态在构造期烧死，关键设计 4 的承诺无法兑现）。
1. **lowering 1:1 不变量**：一条领域消息恰好降为一条 wire 消息。compression 按消息**索引**分区（frozen/compress/active、assistant+tool rounds），1:N 或 drop 型 lowering 会悄悄错位分区。要从 context 消失的消息走**驱逐**（从 `[]Msg` 移除），不走"降为空"。
2. **wire-shaped 的内容就该是 Raw**：loop 的操舵语（wrap-up、"call task_done" 重试提示）本质就是 wire 文本，不强行领域化——直通不是过渡态，是这类消息的终态。
3. **session 记 lowered 形态**：transcript 记录模型实际看到的东西（llm_request = `msg.Lower` 的结果），领域形态入库是 PR C 之后按需再议——refactor 不背 schema 变更。
4. **lowering 的最终归宿在 client 边界（讨论中）**：per-provider 的渲染决策（FileMsg 降成 user 文本还是 tool_result）逻辑上属于"知道 provider 是谁"的那一层，即 llm client。但今天只有一种 wire 模型、零个 per-provider 决策，且 `msg` 依赖 `llm`（wire 类型所在），client 直接收 `[]msg.Msg` 会造成 import 环。所以：**当下 lowering 留在 loop 侧**（`RunPerFile` 内、API 调用前一行）；当 PR B/C 真出现 provider 敏感的渲染决策时，再引入 client 包装层（`ReviewClient{llm.LLMClient; Renderer}`）或把 wire 类型下沉成独立包解环——那时搬迁只是移动一个调用点，因为 lowering 从未散开过。

## flip 后清理清单（typed_briefing 转默认开时执行，债务挂账）

- 删 classic 装配：`renderMaterials` 及 reviewUnit 的字符串降级分支（届时只剩 `assembleTypedBriefing` 一套降级）；
- `piece` 塌缩：byte-identical 纪律的过渡表示，briefer 渲染直接产 `File`；
- 模板 source 槽位语义简化（指针文本成为唯一形态）；
- feature 测试的实验清单移除 typed_briefing。

## 已知权衡

- **transcript 每轮全量记录**：`llm_request` 存当轮完整消息列表（O(轮数×context) 落盘），typed 预载会随轮重复入盘——换取 schema 简单与逐轮可回放；eval 侧读取应流式。继承自 ocr 的设计，若 transcript 体积成为瓶颈，改增量记录是独立的 schema v3 议题。

## References

- 实现锚点：`internal/harness/msg`（类型与 lowering）、`internal/harness/llmloop/loop.go`（货币与调用点）、`internal/harness/llmloop/compression.go`（索引分区，1:1 不变量的依赖方）
- 消费方向：`docs/cross-unit.md`（Board/Bulletin）、`docs/context-model.md` 关键设计 8（briefing）
- 参考：pi `packages/coding-agent/src/core/messages.ts`
