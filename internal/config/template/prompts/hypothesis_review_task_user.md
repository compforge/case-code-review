## Dossier

The change set, background, rules, clues, and hypotheses below are the Dossier transferred from the investigative reviews.

### Change set

```json
{{dossier}}
```

Use `file_read_diff` to inspect the complete diff for each path relevant to a hypothesis. Use `file_read_base` to compare the exact baseline when deciding whether the change introduced the behavior. Use `file_read` and `code_search` for surrounding implementation and call-path evidence.

### Evidence paths already visited by Unit Review

These paths combine Unit inputs, structured evidence locations, and successful file reads that helped form this Dossier. They explain grouping and provide navigation hints, but do not prove any hypothesis by themselves.

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

### Related earlier dossiers

These are completed assessments from earlier, strongly related dossiers in this run. Treat them as prior decisions to reconcile, not as proof.

```json
{{prior_assessments}}
```

### Hypotheses to review

```json
{{hypotheses}}
```

Review every hypothesis independently. Gather only the missing evidence needed to decide it. Submit each completed assessment immediately (alone or in a small batch) instead of waiting until the end; after every hypothesis has an assessment, call `task_done`.
