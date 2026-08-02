# 可观测性

CCR 的可观测性建立在同一条链路上：Session JSONL 持久化执行事实，Viewer 帮助人理解一次运行，
eval 使用这些事实和外部标注判断版本效果。三者依次是基础、诊断投影和效果验证，不能互相替代。

```text
Review Execution
      │
      ▼
Session JSONL                 # 基础：实际发生了什么
      │
      ├──────────▶ Viewer     # 诊断：这一次为什么会这样
      │
      └──────────▶ eval       # 验证：改动是否稳定提升效果
```

## 1. Session JSONL：共同事实源

Session JSONL 是可观测性的基础。Harness recorder 在执行过程中追加事件，记录实际发生的行为，而不是
模板渲染前的推测或运行结束后的摘要。稳定事实包括：

- run/scope、review snapshot、工具版本、feature、模型和业务身份；
- 每轮实际发送的 prompt、模型 response/reasoning、stop reason 和 usage；
- tool call 参数、结果、耗时、成功状态及其所属 request；
- warning、completion、partial 状态和 Execution 统计；
- Hypothesis、Lane、Assessment、Trial decision 等阶段 artifact。

追加式记录使异常退出或预算耗尽的运行仍可分析；Assessment 等中间结论也不会因为后续步骤未完成而
整体丢失。Viewer 和 eval 都应从这份事实投影，不各自解释 AgentGo 内部对象或维护另一套执行记录。

Session 只说明“发生了什么”，不直接说明“效果好不好”。它也不替代 Forge comment、代码仓和业务事实源。
Session 可能包含源码、prompt 与工具结果，应默认作为本地敏感数据处理，不自动上传。

## 2. Viewer：单次运行的人工诊断

Viewer 读取 Session JSONL，把事件组织成人容易检查的页面，主要回答：一次 review 花费在哪里、模型
看到了什么、loop 如何推进、最终决策如何形成，以及哪里发生中断或空转。

Viewer 保留两个互补视角：

1. **Run Overview**：token、时间、模型、工具调用、完成状态，以及
   `Diff Files → Review Files → Unit → Hypothesis → Assessment → Finding` 漏斗；
2. **Agent Loop Conversation**：system/user message、每轮 prompt、模型 response/reasoning、tool 参数与
   结果，以及 Hypothesis → Assessment → Trial 的 Decision Trail。

Viewer 可以增加简单、确定、便于人发现问题的数据统计，例如重复读取、context 与 `file_read` 路径重合、
空搜索和阶段未完成数。这些数据是诊断线索，不是效果分数。Viewer 不负责实验编排，不回写 Session 或
Trial 结果，也不能因为“0 Finding”就把 partial run 展示为 clean。

单次运行容易受 diff、模型路由、缓存、网络和上下文差异影响。Viewer 更像显微镜：适合发现问题、查看
prompt 和形成改进假设，不适合凭少量 session 断言整体效果提升。

## 3. eval：主要效果判断

主要效果问题由 eval 回答。它把 Session/ATIF 与人工标签、固定 corpus、阶段数据集和 Evaluator 连接，
在相同输入和判定标准下比较 baseline 与 candidate，并同时观察：

- **准确性**：Finding 真伪、重复交付、漏报，以及 Assessment/Trial 是否正确放行；
- **健壮性**：Unit/Lane 是否完成、Hypothesis 是否全部 Assessment、超时、partial 和执行错误；
- **成本**：token、时间、模型轮次、工具调用和 Unit 数量。

Viewer 中发现的重复 `file_read`、搜索空转或未完成 Unit，可以进一步沉淀为 Trajectory Evaluator；人工
确认的 Finding 则沉淀为 label 和固定数据集。只有在对照实验中确认问题具有普遍性、指标改善且没有召回
或成本回退，才能认为优化有效。

eval 的标签协议、数据边界、数据集构建、Trajectory 诊断、固定 corpus 与重放命令不在本文展开，统一见
[`eval/README.md`](../eval/README.md)。真实 corpus、labels、datasets 和 trajectory 放在被忽略的
`eval/data/`；公开仓只提交通用工具、匿名 fixture 与方法说明。

## References

- [`harness.md`](harness.md)——Session recorder、Execution 生命周期和 Viewer 投影契约
- [`../eval/README.md`](../eval/README.md)——效果评估的采集、数据集、Evaluator 与实验流程
- [`unit_review.md`](unit_review.md)——Review 1 的效果、上下文与完成契约
- [`hypothesis_review.md`](hypothesis_review.md)——Review 2 的 Assessment、Lane 与 Trial gate
