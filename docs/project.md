# Project Knowledge：Repository / Component / FileRole

## 1. 理念 / 概念

Project Knowledge 解释一份代码在当前项目里扮演什么角色。它不解析函数调用，也不判断 Finding；
它提供 Repository、Component 和 FileRole 这些稳定项目事实，让 Unit formation 与三个 Review 阶段
不必把所有文件都当成同一种输入。

```text
Repository
  └─ Component（manifest 定义的项目边界）
       └─ File ─▶ FileRole[]
                    ├─ source / test
                    ├─ entrypoint / handler
                    └─ manifest / lock / version
```

| 对象 | 语义 |
|---|---|
| `Repository` | 本次被评审的 Git snapshot，也是解析 Component 的入口 |
| `Component` | Repository 内由项目 manifest 定义的静态项目边界 |
| `FileRole` | 文件在所属 Component 中可组合的稳定职责 |
| Project Clue | Project 事实面向一个 Unit 的上下文投影，不是独立事实源 |

Component 是静态项目边界，Unit 是当前 diff 动态形成的行为评审边界。一个 Component 可以产生多个
Unit；一个 Unit 通常只消费所属 Component 的项目事实，不能因为同在 Repository 就传播全部上下文。

FileRole 不是互斥枚举，也不表示证据强度。`main.go` 可以是 `source + entrypoint`，`a_test.go`
可以是 `source + test`；同一个 lockfile 对依赖变化可能关键，对业务状态流转则可能只是背景。

## 2. 流程

### 2.1 在被评审的 snapshot 中解析 Component

每个 Change 从自身目录向 Repository 根查找最近的项目 manifest。当前内置 profile 为：

| Component | manifest | source | 补充角色与项目文件 |
|---|---|---|---|
| Python | `pyproject.toml` / `setup.py` | `.py` / `.pyi` | `test_*.py`、`*_test.py`、`conftest.py` 为 test；`__main__.py` 为 entrypoint；`uv.lock` 为 lock |
| Go | `go.mod` | `.go` | `*_test.go` 为 test；`main.go` 为 entrypoint；`go.sum` 为 lock；Component 根 `VERSION` 为 version |
| TypeScript | `package.json` | `.ts/.tsx/.js/.jsx/.mjs/.cjs` | `*.test.*` / `*.spec.*` 为 test；`main.*` 为 entrypoint；`tsconfig*.json` 为 manifest；常见包管理器 lockfile 为 lock |

monorepo 中嵌套 manifest 形成更近的 Component。一个目录同时存在多种 manifest 时，优先选择能解释
当前文件的 profile；若多个 profile 都不能明确归属，保持 unowned / unknown，而不是任意挑选。

Repository 的存在性查询必须面向 workspace、range target 或 commit target 对应的同一 snapshot。
不能用当前 checkout 的 manifest 判断旧 commit 的 Component，否则 diff、源码和项目知识会来自不同时间点。

### 2.2 将稳定角色投影为当前 Review 策略

Project 只回答文件是什么，Runner 再决定本次 Unit Review 如何消费它：

```text
Change ─▶ Component / FileRole
              ├─ source ───────────────▶ Unit target
              │    └─ entrypoint / handler / test ─▶ project Clue
              ├─ manifest / lock ─────▶ Component context
              └─ version ─────────────▶ no Unit Review
```

- `source` 是当前 Unit formation 的 target；
- 发生变化的 `manifest / lock` 只向同 Component Unit 提供项目 Clue，单独变化时不启动 agent loop；
- Component 根 `VERSION` 是发布元数据，保留角色用于观测，但不成为 target 或业务上下文；
- `entrypoint / handler` 与 source 组合，提示 Review 关注初始化、生命周期、输入契约、鉴权和响应语义；
- `test` 当前只记录职责，不在分类阶段直接决定跳过。是否独立形成 Unit、并入源码 Unit 或采用轻量
  Review，应由 formation / review policy 决定；
- 用户显式 include 可以提升文件为 target；未被 Component 认领的文件继续使用全局扩展名与路径规则。

这层分离使 FileRole 可以被 Review 1、Review 2 或未来其它 Reviewer 复用，而不把当前 admission
策略固化进项目知识。

### 2.3 用 Language 事实丰富项目语义

Project 与 Language 各自拥有不同事实：Language 提取 decorator、call、symbol 和 span，Project
解释这些事实在特定生态里的含义。例如 Python 文件只有出现 FastAPI route decorator 才增加
`handler` 角色；`routers/`、`routes.py`、`handlers/` 等路径名本身不是充分证据。`main.py` 只有实际
创建 `FastAPI` 时才增加 entrypoint 角色。

Project Clue 在 Unit scope 最终确定后挂载：source 自身的 entrypoint / handler 角色形成 `self/project`
Clue；同 Component 中变化的 manifest / lock 形成 `project/project` Clue。这样 Project 负责稳定事实，
Unit 负责当前行为范围，prompt 排版仍由各 Review 阶段决定。

## 3. 关键设计

### 3.1 项目事实与评审策略分离

`FileRole` 不能直接等同于 target/context/excluded。角色是可复用事实，admission 是当前 Reviewer 的
策略；把两者混在一起，会让新增 Review 视角时被迫修改 Project 模型。

### 3.2 最近 Component 优先，Repository 不是传播边界

Repository 可以包含多个语言和多个 Component。项目上下文按最近 manifest 归属，只在明确 Component
内传播；Repository 级 README、docs 或规则未来可以作为 Repository Knowledge，但不能借“同仓”默认
注入所有 Unit。

### 3.3 未知保持未知

无法识别 manifest、文件角色或语言语义时保持 unknown，交给全局规则降级。Project Knowledge 的价值
来自减少无意义 loop 和补充可靠先验，不来自用目录名或扩展名制造看似丰富的猜测。

### 3.4 新生态通过 profile 扩展

新增语言项目时，profile 至少应明确 manifest、source、test、entrypoint、context 文件和 snapshot
行为，并用 Repository 级测试覆盖 nested component、polyglot、unknown 与 role composition。只有稳定
项目事实进入 Project；代码语义解析继续留在 Language。

## References

- [`kernel.md`](kernel.md) — Project Knowledge 在 CCR Kernel 中的位置
- [`unit-model.md`](unit-model.md) — Project 事实如何参与 Unit formation 与 Clue 组织
- [`language.md`](language.md) — decorator、call、symbol 等源码事实的 owner
- [`unit_review.md`](unit_review.md) — Project Clue 进入 Review 1 后如何被消费
