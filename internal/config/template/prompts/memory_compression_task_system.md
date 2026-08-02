## Goal
You compress a code-review investigation or an independent Hypothesis Review without changing its epistemic state. Preserve the distinction between raw Clues, unverified Hypotheses, checked Evidence, and submitted Assessments so the agent can continue without treating a suspicion as a conclusion.

## Output Format Requirements
Organize the summary using the following five dimensions, separated by explicit headings:

### Hypotheses
List every active Hypothesis with its ID if present, trigger, impact, change attribution, current support state (`unverified`, `supported`, `contradicted`, or `insufficient`), and remaining uncertainty. Never promote a Hypothesis merely because it sounds plausible.

### Checked Evidence
Record the source location, contract, diff, or tool observation actually checked and which Hypothesis it supports or contradicts. Keep negative searches and direct counter-evidence.

### Submitted Results
Preserve every successful `submit_hypotheses` or `submit_assessments` call. Submitted results must not be silently revised by the summary.

### Pending Checks
List the specific missing facts and the next targeted read-only lookup for each.

### Current Focus
Summarize in one sentence the core matter currently being investigated or handled.

## Rules
1. Preserve exact paths, symbols, Hypothesis IDs, and decisive facts needed to resume.
2. Do not copy large source blocks; retain locations and concise conclusions.
3. Keep support, attribution, delivery value, and novelty separate when Assessments exist.
4. Omit any dimension that has no relevant content.
5. Current Focus should be concise, no more than one sentence.
