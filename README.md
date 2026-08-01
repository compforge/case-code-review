# case-code-review (`ccr`)

> AI-powered code review CLI — built on top of [open-code-review](https://github.com/alibaba/open-code-review) and deepened further. ｜ 中文见 [README.zh-CN.md](./README.zh-CN.md)

## Philosophy

A diff alone is too little to review well: it cannot tell whether a change breaks the requirement it serves or the code that depends on it. ccr is built around three ideas:

**1. Capture relevant knowledge.** Language Knowledge supplies definitions, references, calls and source documentation. Project Knowledge identifies repository components and file roles such as source, entrypoint, handler, manifest and lock. Authored specs, cases, rules and links add explicit contracts.

**2. Review per *Unit*, not per file.** A Unit is one behavioral review scope: a function, a file, or a cross-file call chain. When the diff has only one reviewable file, it becomes one file Unit. When changed functions across files call each other, ccr merges them into one call-chain Unit because separate file loops would read the same code anyway. The goal is no more Review 1 loops than reviewable files, and fewer when the change crosses file boundaries.

**3. Separate discovery from decision.** Each Unit Review produces zero or more falsifiable Hypotheses, not public comments. Today, all Hypotheses from one run enter a single change-set CaseFile; Hypothesis Review verifies them with read-only evidence and produces Assessments. Deterministic Trial rules publish only supported, actionable and non-duplicate Findings caused by the current change.

ccr does not try to read the whole repository or enumerate every possible issue. It uses relevant, bounded context to catch concrete mistakes that are easy to miss while implementing a requirement — boundary handling, error paths, API misuse, or a broken caller assumption. Hidden business constraints need explicit background, specs, cases or rules; loading more unrelated code cannot manufacture that knowledge. Syntax stays lint's job.

Review quality is judged on three goals that pull against each other — **robustness, accuracy, cost** — pursued through three levers all centered on the review loop: its capability, its granularity, and its context. See `AGENTS.md` for the full frame.

![CCR review pipeline from Diff to Finding](docs/review-pipeline.svg)

Design details: [`Kernel`](docs/kernel.md) · [`Unit`](docs/unit-model.md) · [`Unit Review`](docs/unit_review.md) · [`Hypothesis Review`](docs/hypothesis_review.md) · [`Harness and observability`](docs/harness.md)

## Knowledge and context

Project Knowledge first decides how a changed file participates in review:

| file role | review behavior |
|---|---|
| **source** | may become a Unit target |
| **entrypoint / handler** | remains source and adds project-specific review focus |
| **manifest / lock** | accompanies source changes as project context; alone it does not start a Unit Review |
| **version** | release metadata for observability; alone it does not start a Unit Review |

For each review unit, ccr assembles a set of *clues*. A clue is one piece of evidence of a **kind**, reached along a **relation** — two orthogonal axes (see [`docs/unit-model.md`](docs/unit-model.md)):

| kind | what it is | source |
|---|---|---|
| **spec** | the symbol's contract (what it must guarantee) | authored (`spec.json`) |
| **case** | concrete scenarios to verify | authored |
| **rule** | review criteria — what to watch for | authored |
| **link** | curated "see also" — a doc or another function | authored |
| **doc** | the symbol's docstring / doc comment | **derived from source** |
| **history** | a finding delivered by an earlier revision | forge input |
| **project** | component, file-role or project-structure knowledge | derived from the repository |

| relation | the symbol it reaches |
|---|---|
| **self** | the changed symbol itself |
| **owner** | its enclosing class/type (a method change surfaces the class's contract) |
| **caller** | who uses it — walking up to the nearest authored spec (the governing contract) |
| **callee** | what it relies on — direct dependencies' contracts |
| **used** | a type/func the diff references (import-resolved, so same-named symbols disambiguate) |
| **project** | the component or project fact that contextualizes the Unit |

Two properties worth knowing:

- **`doc` needs zero adoption.** The authored kinds require [`spec-case`](https://github.com/qiankunli/spec-case) markers; `doc` is extracted from source at review time (Python docstrings, Go doc comments) — including from your *dependencies'* source. A repo that never heard of spec-case still gets contract context on every relation.
- **Cross-repo by fqn.** Locally, symbols are addressed by `relpath::symbol`. A dependency ships its own `spec.json` inside the package (Go module cache / Python site-packages); its entries are matched **only** by fqn (`import.path.Symbol`), resolved through your imports — so a framework's "per-request only" rule fires when your diff uses that framework type.

## How to Use

### Install

```bash
git clone https://github.com/compforge/case-code-review && cd case-code-review
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

For continuous PR/MR review, the forge comments are the durable history. On each revision,
the caller fetches the current PR/MR comments, materializes a temporary `prior.json`, and
passes it to ccr; devloop and review-harness do this automatically. Keys prefer symbol IDs
and may fall back to repo-relative paths when the forge only retains a file anchor, for
example `{"path/to/file.go::Symbol":[{"msg":"prior finding","sha":"abc123"}]}`.

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
ccr viewer                      # WebUI: Diff→Review 1 funnel, run totals, prompt timelines and decisions
```

## Status

Actively developed. In: Project and Language Knowledge, project-aware file roles, Unit formation (function / file / call chain), two-stage evidence review, feature gates, dry-run metrics, cross-revision history reconciliation, and an observable session viewer.

## License

Apache-2.0 (see `LICENSE` / `NOTICE`).
