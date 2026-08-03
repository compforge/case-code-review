# case-code-review (`ccr`)

> Agentic code review organized around behavioral Units, two agent reviews plus deterministic Trial, and language/project knowledge. Built on [open-code-review](https://github.com/alibaba/open-code-review). ｜ 中文见 [README.zh-CN.md](./README.zh-CN.md)

## Philosophy

Code review is not “send every changed file to a model.” File boundaries describe storage, not behavior; a diff alone is often too narrow, while reading the whole repository is usually too expensive and still cannot reveal unstated business rules.

ccr follows three principles:

1. **Review behavior, not files.** A review should cover the smallest scope that explains a change and its effects. ccr calls that scope a **Unit**.
2. **Separate discovery from verification.** Finding a plausible issue is open-ended; proving that it is real, caused by the current change, actionable, and new is narrower. Different jobs deserve different agent loops.
3. **Bring relevant knowledge, not maximum context.** Language structure and project structure decide what belongs together and which nearby evidence is worth showing to the model.

ccr does not try to enumerate every possible defect. It focuses bounded agent exploration on concrete mistakes that are easy to miss while implementing a requirement: broken caller assumptions, boundary handling, error paths, API misuse, and similar reviewable failures. Syntax remains lint's job; hidden business constraints still need explicit background or authored knowledge.

![CCR agentic review pipeline from Diff to Finding](docs/review-pipeline.svg)

## Core model

### Unit: the review boundary

A **Unit** is one behavioral review scope. Depending on the change, it can be:

- a **function** when one symbol is the natural boundary;
- a **file** when the diff touches only one reviewable file;
- a **cross-file call chain** when changed functions collaborate and separate file reviews would rediscover the same context.

This makes review granularity more flexible than file-by-file review. The practical goal is for Review 1 to use no more loops than there are reviewable files, and fewer when related cross-file changes can be reviewed together.

Making Unit—not file—the basic review boundary depends on practical caller/callee discovery. This has become feasible as [`gotreesitter`](https://github.com/odvcencio/gotreesitter) has matured in cross-language parsing, symbol resolution, and call analysis.

### Review 1 discovers; Review 2 verifies; Review 3 gates

| stage | responsibility | output | public-prosecution analogy |
|---|---|---|---|
| **Unit Review (Review 1)** | explore a Unit, follow evidence, identify plausible defects | `Hypothesis` | investigation proposes a case theory |
| **Hypothesis Review (Review 2)** | independently check source, diff, baseline, impact, attribution, and duplication | `Assessment` | prosecutor reviews whether evidence supports the allegation |
| **Trial (Review 3)** | apply deterministic delivery gates | `Finding` or rejection | court gate decides what may be delivered |

The analogy explains separation of duties; these are code-review stages, not legal simulations. Review 1 is encouraged to discover. Review 2 is encouraged to disprove weak hypotheses. **Review 3** is an alias for deterministic Trial, not another agent loop, so incomplete or unsupported work cannot silently become a public comment. The three passes also echo the Chinese mnemonic “吾日三省吾身”: review the result repeatedly before delivering it.

### Language Knowledge and Project Knowledge

Two knowledge foundations support Unit formation and both review stages:

| knowledge | what it contributes |
|---|---|
| **Language Knowledge** | syntax-aware symbols, spans, definitions, references, calls, imports, documentation, and extraction/matching of authored contracts |
| **Project Knowledge** | Repository and Component boundaries, file roles, entrypoints, handlers, manifests, locks, and other project conventions |

Some context types—such as spec, case, rule, link, and source documentation—depend on language parsing even though they are not “language knowledge” by themselves. [`spec-case`](https://github.com/qiankunli/spec-case) remains an optional way to author and distribute this contract context; ccr also works without it.

Design details: [`Kernel`](docs/kernel.md) · [`Project`](docs/project.md) · [`Language`](docs/language.md) · [`Unit`](docs/unit-model.md) · [`Unit Review`](docs/unit_review.md) · [`Hypothesis Review`](docs/hypothesis_review.md) · [`Harness and observability`](docs/harness.md)

## How to use

### Install

```bash
git clone https://github.com/compforge/case-code-review && cd case-code-review
make install        # installs `ccr` into ~/.local/bin; re-signs on macOS
# or: go install github.com/qiankunli/case-code-review/cmd/ccr@latest
```

### Configure the model

```bash
ccr config provider     # choose or add a provider
ccr config model        # choose a model
ccr llm test            # verify connectivity
```

Configuration lives in `~/.casecodereview/config.json`. Built-in and OpenAI-compatible custom providers can also be configured non-interactively; see `ccr config --help`.

### Review a change

```bash
ccr review                              # staged + unstaged + untracked changes
ccr review --from main --to my-branch  # branch against merge base
ccr review --commit abc123              # one commit against its first parent
ccr review --background "requirement"   # add business or requirement context
ccr review --format json                # machine-readable output for CI/bots
```

For continuous PR/MR review, pass earlier delivered findings with `--history prior.json`. The forge comments are the durable source; the caller fetches them for each revision so ccr can distinguish new findings from repeat delivery.

### Inspect before spending tokens

```bash
ccr review --preview            # changed files and their review/exclusion roles
ccr review --dry-run            # formed Units and assembled context, without an LLM call
ccr review --dry-run --format json
```

### Observe a run

```bash
ccr viewer                      # sessions, token/time/tool totals, prompts and decisions
```

Session JSONL preserves the actual messages, model responses, tool calls, artifacts, warnings, and completion state. The Viewer turns that trace into run-level statistics and per-loop timelines for diagnosing quality, cost, and incomplete reviews.

### Optional authored context and feature gates

Place a generated `spec.json` at `.casecodereview/spec.json`, pass `--spec`, or configure user-level contracts to add authored spec/case/rule/link context. Named feature gates support ablation, for example:

```bash
ccr review --feature caller_callee=off
ccr review --feature callchain=off
ccr review --feature doc=off
```

Run `ccr review --help` for the full command and feature list.

## Status

Actively developed. Current foundations include project-aware file roles, language analysis, Unit formation, two agent review stages plus deterministic Trial, cross-revision history, bounded agent execution, and an observable session viewer.

## License

Apache-2.0 (see `LICENSE` / `NOTICE`).
