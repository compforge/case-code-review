## Hypothesis Review Input

The change set, background, rules, clues, and hypothesis below are the material for this Review 2 decision.

### Change set

```json
{{change_set}}
```

The originating Unit already supplies the target diff plus file, related-diff, and search snapshots retained from Review 1. Verify that Unit context first. Call `read_diffs`, `read_base_files`, `read_files`, or `search_code` only for a specific fact still missing; batch independent targets in one call.

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

{{prior_assessments}}

### Hypothesis to review

```json
{{hypothesis}}
```

Use `trigger` and `uncertainty` to identify the decisive premises before investigating consequences. Gather only repository evidence needed to decide those premises. If a decisive premise belongs only to an external provider, SDK, or API and is not already proven here, call `check_external_evidence` once and then submit `insufficient` rather than investigating it. If independent local facts are already known, batch them in the same tool call. When the decision is ready, call `submit_assessment` once; a valid submission ends this Review 2 execution.
