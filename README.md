# case-code-review (`ccr`)

> AI-powered code review CLI — built on top of [open-code-review](https://github.com/alibaba/open-code-review) and deepened further. ｜ 中文见 [README.zh-CN.md](./README.zh-CN.md)

## Philosophy

A diff alone is too little to review well — it can't tell whether a change breaks the requirement it serves or the code that depends on it. ccr's two ideas:

**1. Capture more context.** ccr locates the *functions* a diff changes, then gathers the context the diff doesn't carry — along typed relations, from two sources (see below).

**2. Review per *unit*, with a merge step.** A *unit* is the scope of one review loop, and its granularity is a ladder: **function → class → file → module / directory**. A single-file change remains one file unit. For multi-file changes, ccr first locates changed functions and then **merges** them into the units that actually trigger loops: functions changed together along a call chain become one cross-file unit, while a sweeping change coalesces into file-level units. The result preserves semantic focus without multiplying loops unnecessarily — one loop per merged unit.

In diff review, Unit reviews investigate broadly and produce falsifiable hypotheses, not public comments. A separate evidence review then examines the change as one case, uses read-only code tools to verify or refute every hypothesis, and publishes only supported, actionable findings.

The payoff: ccr finds the bugs that need background — a change quietly breaking a caller's assumption, or violating an invariant the diff doesn't show — checklist-checked against the function's real contract. (Syntax stays lint's job.)

Review quality is judged on three goals that pull against each other — **robustness, accuracy, cost** — pursued through three levers all centered on the review loop: its capability, its granularity, and its context. See `AGENTS.md` for the full frame.

![CCR two-stage evidence review pipeline](docs/kernel-pipeline.svg)

## The context model: evidence kinds × relations

For each review unit, ccr assembles a dossier of *clues*. A clue is one piece of evidence of a **kind**, reached along a **relation** — two orthogonal axes (see `docs/context-model.md`):

| kind | what it is | source |
|---|---|---|
| **spec** | the symbol's contract (what it must guarantee) | authored (`spec.json`) |
| **case** | concrete scenarios to verify | authored |
| **rule** | review criteria — what to watch for | authored |
| **link** | curated "see also" — a doc or another function | authored |
| **doc** | the symbol's docstring / doc comment | **derived from source** |

| relation | the symbol it reaches |
|---|---|
| **self** | the changed symbol itself |
| **owner** | its enclosing class/type (a method change surfaces the class's contract) |
| **caller** | who uses it — walking up to the nearest authored spec (the governing contract) |
| **callee** | what it relies on — direct dependencies' contracts |
| **used** | a type/func the diff references (import-resolved, so same-named symbols disambiguate) |

Two properties worth knowing:

- **`doc` needs zero adoption.** The authored kinds require [`spec-case`](https://github.com/qiankunli/spec-case) markers; `doc` is extracted from source at review time (Python docstrings, Go doc comments) — including from your *dependencies'* source. A repo that never heard of spec-case still gets contract context on every relation.
- **Cross-repo by fqn.** Locally, symbols are addressed by `relpath::symbol`. A dependency ships its own `spec.json` inside the package (Go module cache / Python site-packages); its entries are matched **only** by fqn (`import.path.Symbol`), resolved through your imports — so a framework's "per-request only" rule fires when your diff uses that framework type.

## How to Use

### Install

```bash
git clone https://github.com/qiankunli/case-code-review && cd case-code-review
make install        # builds and installs `ccr` into ~/.local/bin (re-signs on macOS)
# or: go install github.com/qiankunli/case-code-review/cmd/ccr@latest
```

### Configure the LLM

Config lives in `~/.casecodereview/config.json`. Interactive setup:

```bash
ccr config provider     # pick a built-in provider or add a custom one (url / protocol / api_key)
ccr config model        # pick a model for the active provider
ccr llm test            # verify connectivity
```

Non-interactive (CI / scripts):

```bash
ccr config set provider anthropic
ccr config set providers.anthropic.api_key $ANTHROPIC_API_KEY
ccr config set providers.anthropic.model claude-sonnet-4-6
```

Custom providers (private gateways, OpenAI-protocol endpoints) support `url`, `protocol`, `extra_body`, `extra_headers`, `timeout_sec`, and a `models` list — see `ccr config --help`.

### Review

```bash
ccr review                              # workspace: staged + unstaged + untracked
ccr review --from main --to my-branch  # branch vs base (merge-base mode)
ccr review --commit abc123              # a single commit vs its parent
ccr review --format json                # machine-readable output (CI, bots)
ccr review --background "$(cat mr.md)"  # inject requirement/business context for precision
ccr review --history prior.json         # prior findings, re-checked against the new diff
```

### Inspect before spending tokens

Both are LLM-free:

```bash
ccr review --preview            # which files would be reviewed / excluded
ccr review --dry-run            # + each unit's fully assembled context (what the LLM would see)
ccr review --dry-run --format json   # + structural metrics: unit/scope counts and the
                                     #   clue_coverage matrix (relation/kind, e.g. owner/rule, callee/doc)
```

`--dry-run --format json` is the free A/B layer: diff two runs' metrics to see exactly what a feature or a spec.json adds, without an LLM call.

### Feature gates (ablation)

Every capability sits behind a named gate, all **on** by default. Turn one off to measure its marginal effect (leave-one-out):

```bash
ccr review --feature doc=off             # no derived docstring clues
ccr review --feature caller_callee=off   # no call-graph walk
ccr review --feature callchain=off       # no cross-file call-chain units
```

Kind gates (`spec_case` / `rule` / `link` / `doc`) switch an evidence kind across *all* relations; `caller_callee` is the cost gate for the call-graph walk. Also settable via `features:{}` in config or the `CCR_FEATURES` env. Run `ccr review --help` for the full list.

### Authored contracts (optional, recommended)

Mark functions/classes with [`spec-case`](https://github.com/qiankunli/spec-case) (Go doc-comment markers / Python decorators), generate `spec.json` with its `specgen`, and drop it at `.casecodereview/spec.json` — ccr auto-loads it (plus `~/.casecodereview/spec.json` and `--spec path`, highest wins). Dependencies' packaged `spec.json` files are discovered automatically and matched by fqn.

### More

```bash
ccr scan                        # review whole files, no diff required (--path to narrow)
ccr rules                       # inspect which review rules apply to which paths
ccr viewer                      # WebUI: browse past review sessions, per-unit prompts and replies
```

## Status

Actively developed. In: function-level splitting (Go + Python), call-chain merge + cost governor, the full kind×relation context model above (authored + derived, local + dependency), feature gates, dry-run metrics, history reconciliation, session viewer.

## License

Apache-2.0 (see `LICENSE` / `NOTICE`).
