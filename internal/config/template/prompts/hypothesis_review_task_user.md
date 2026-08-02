## Hypothesis Review Input

The change set, background, rules, clues, and hypothesis below are the material for this Review 2 decision.

### Change set

```json
{{change_set}}
```

Use `file_read_diff` to inspect the complete diff for each path relevant to a hypothesis. Use `file_read_base` to compare the exact baseline when deciding whether the change introduced the behavior. Use `file_read` and `code_search` for surrounding implementation and call-path evidence.

### Evidence paths already visited by Unit Review

These paths combine Unit inputs, structured evidence locations, and successful file reads that helped form this hypothesis. They explain Lane assignment and provide navigation hints, but do not prove the hypothesis by themselves.

```json
{{evidence_paths}}
```

### Requirement background

{{requirement_background}}

### Governing review rules

{{system_rules}}

### Existing clues

This is context gathered around the affected Units. It is not automatically evidence: use it only after checking that it actually supports or contradicts a hypothesis.

```json
{{clues}}
```

### Related earlier assessments in this Lane

These are completed assessments from earlier, strongly related hypotheses in this run. Treat them as prior decisions to reconcile, not as proof.

```json
{{prior_assessments}}
```

### Hypothesis to review

```json
{{hypothesis}}
```

Gather only the missing evidence needed to decide this hypothesis. Submit its assessment immediately; after it has an assessment, call `task_done`.
