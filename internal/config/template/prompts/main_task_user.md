<review_task>
Investigate the changed behavior in <current_file_diff>. Use the attached file messages and knowledge before fetching more context. Report only concrete issue Hypotheses worth independent review, then call `task_done`.
</review_task>

// The following is the list of other files changed in this update.
<other_changed_files>
{{change_files}}
</other_changed_files>

<current_file_path>{{current_file_path}}</current_file_path>

<current_file_diff>
{{diff}}
</current_file_diff>

// The full post-change source of the reviewed file(s) is attached after this task as
// labeled File messages. The runtime also appends an exact inventory of visible ranges.
<current_file_source>
{{unit_source}}
</current_file_source>

// Related caller/callee bodies are attached as labeled File messages. They are context,
// not review targets; do not comment on them.
<related_source>
{{related_source}}
</related_source>

Current time in the real world: {{current_system_date_time}}

<user_task>
### Requirement Background (Optional)
{{requirement_background}}

### Project Context (Optional)
Changed manifest or lock files from the same Component. They are not review targets for this Unit;
inspect their diff only when they can affect the changed source behavior.
{{project_context}}

### Governing Spec/Case (Optional)
The contract and cases bound to the changed function(s). Treat them as invariants the change must preserve: for each, judge whether this diff could break it. A case unrelated to this change is a valid finding too — say it is unaffected. If empty, no spec is bound to these functions.
{{spec_cases}}

### See Also (Optional)
References the author flagged as relevant when changing these function(s) — consult them, fetching content as needed (a bare path is a doc; `<path>::<symbol>` is another function). If empty, none were flagged.
{{see_also}}

### Prior Review (Optional)
Findings already delivered as durable comments on this MR. Use them to understand the current revision, but do not create a new Hypothesis for the same underlying issue, whether it is fixed or still present. Only report a distinct regression with a different trigger or impact. If empty, there is no prior delivery to reconcile.
{{prior_findings}}

### Repo Symbol Map (Optional)
Symbols that actually exist in this repository, ranked by relevance to this change. When searching or reading other code, use these exact names — do not invent or guess identifier names; if a name you expected is not listed, search a fragment of it rather than the full guess. If empty, no map was built.
{{repo_map}}

### Usage Sites of Changed Symbols (Optional)
Everywhere else in the repository the changed function(s) are referenced, as pre-run `git grep` results (`path:line: text`). This is your blast-radius map — judge from it whether each usage still holds after this change instead of searching for callers yourself; fetch surrounding code only where a usage looks affected. If empty, no other references were found.
{{usage_sites}}

### Review Checklist
{{system_rule}}

### Review Plan (Optional)
{{plan_guidance}}

</user_task>
