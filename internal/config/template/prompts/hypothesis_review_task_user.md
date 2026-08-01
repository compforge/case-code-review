## CaseFile

The change set, background, rules, clues, and hypotheses below are the CaseFile transferred from the investigative reviews.

### Change set

```json
{{change_set}}
```

Use `file_read_diff` to inspect the complete diff for each path relevant to a hypothesis. Use `file_read_base` to compare the exact baseline when deciding whether the change introduced the behavior. Use `file_read` and `code_search` for surrounding implementation and call-path evidence.

### Requirement background

{{requirement_background}}

### Governing review rules

{{system_rules}}

### Existing clues

This is context gathered around the affected Units. It is not automatically evidence: use it only after checking that it actually supports or contradicts a hypothesis.

```json
{{clues}}
```

### Hypotheses to review

```json
{{hypotheses}}
```

Review every hypothesis independently. Gather only the missing evidence needed to decide it, submit all assessments together, then call `task_done`.
