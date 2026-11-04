import { el, badge, field, details, notices, link, flatten } from './ui.mjs';

function artifact(record) { return record.data || record; }
function decision(parent, title, data) {
  const section = el('section', null, 'decision'); section.append(el('h3', title));
  if (!data) { section.append(el('p', 'Not recorded', 'muted')); parent.append(section); return section; }
  for (const [label, key] of [['Claim', 'content'], ['Trigger', 'trigger'], ['Impact', 'impact'], ['Change attribution', 'change_attribution'], ['Reason', 'reason'], ['Uncertainty / counterevidence', 'uncertainty']]) {
    if (!data[key]) continue;
    if (key==='content') { field(section,label,data[key].length>550 ? data[key].slice(0,550)+'…' : data[key]); if(data[key].length>550)section.append(details('Full claim',data[key])); }
    else section.append(details(label,data[key]));
  }
  if (data.evidence) section.append(details('Evidence',data.evidence));
  parent.append(section); return section;
}

export function renderJob(parent, job, state) {
  const result = job.result || {};
  const records = result.records || [];
  const nodes = flatten(job.nodes);
  const unit = job.attributes.unit_id;
  const paths = job.goal?.paths || result.unit?.paths || [];
  parent.append(el('div', 'JOB VIEW', 'eyebrow'), el('h2', paths[0] || unit), badge(job.status));
  field(parent, 'Scope', paths); field(parent, 'Completion reason', result.unit?.reason);
  notices(parent, job.issues);
  const hypotheses = records.filter(r => r.artifact_kind === 'review_hypothesis');
  parent.append(el('p', `${hypotheses.length} hypotheses · ${records.filter(r => r.type === 'finding').length} recorded findings. Trial delivery is not proof of a published Forge comment.`, 'muted'));
  const selected = state.params.get('hypothesis');
  if (!hypotheses.length) parent.append(el('p', 'No Hypothesis recorded. This alone does not establish a clean verdict.', 'empty'));
  for (const record of hypotheses) {
    const h = record.data;
    const related = records.filter(r => artifact(r).hypothesis_id === h.id);
    const assessments = related.filter(r => r.artifact_kind === 'review_assessment');
    const trials = related.filter(r => r.artifact_kind === 'trial_decision');
    const findings = related.filter(r => r.type === 'finding');
    const card = el('article', null, `hypothesis ${selected === h.id ? 'selected' : ''}`);
    card.id = h.id;
    const title = el('div', null, 'card-title'); title.append(link(`${h.path || 'Hypothesis'}${h.start_line ? `:${h.start_line}` : ''}`, { hypothesis: h.id }, state), badge(h.severity), el('code', h.id)); card.append(title);
    decision(card, 'R1 · Hypothesis', h);
    if (!assessments.length) decision(card, 'R2 · Assessment', null);
    for (const record of assessments) {
      const a = record.data; const section = el('section', null, 'decision'); section.append(el('h3', `R2 · Assessment #${a.submission_index ?? '?'}${a.replaced ? ' · replaced' : ''}`));
      const axes = el('div', null, 'axes'); for (const key of ['support', 'attribution', 'value', 'novelty']) axes.append(el('span', `${key}: ${a[key] || 'unknown'}`, 'badge')); section.append(axes);
      field(section, 'Reason', a.reason); section.append(details('Evidence', a.evidence || []));
      const lane=(state.review.Sessions||[]).find(s=>s.lane===a.lane_id);
      if(lane && a.evidence_receipts?.length) {
        const receipts=el('details');receipts.append(el('summary','Evidence receipts → recorded tool calls'));
        for(const receipt of a.evidence_receipts) {
          const row=el('div',null,'receipt');row.append(el('small',`${receipt.kind} · ${receipt.ref}`));
          if(receipt.tool_call_id && !receipt.tool_call_id.startsWith('unit:')) row.append(link(receipt.tool_call_id,{view:'session',session:null,unit,tool:receipt.tool_call_id,node:null,execution:null},state));
          else row.append(el('small','Preloaded Unit evidence'));
          receipts.append(row);
        }
        section.append(receipts);
      }
      card.append(section);
    }
    const trial = el('section', null, 'decision'); trial.append(el('h3', 'Trial · Delivery gate'));
    if (!trials.length) trial.append(el('p', 'Not recorded', 'muted'));
    for (const r of trials) { const t = r.data; trial.append(el('p', `Assessment #${t.assessment_submission_index ?? '?'} · passed: ${t.passed_trial ?? 'unknown'} · delivered: ${t.delivered ?? 'unknown'}`)); field(trial, 'Reason', t.reason || t.suppression_reason); trial.append(details('Recorded decision', t)); }
    card.append(trial);
    for (const f of findings) decision(card, 'Finding · Recorded output', f);
    const verdicts = related.filter(r => r.artifact_kind === 'verdict' || r.artifact_kind === 'review_verdict');
    const verdict = el('section', null, 'decision'); verdict.append(el('h3', 'Human Verdict'), el('p', verdicts.length ? 'Recorded annotation' : 'Unknown · no annotation in this recording', 'muted')); for (const v of verdicts) verdict.append(details('Annotation evidence', v)); card.append(verdict);
    const connections = el('div', null, 'connections');
    const executions = related.filter(r => r.artifact_kind === 'hypothesis_review_execution').map(r => r.data.execution_id);
    for (const session of state.review.Sessions || []) {
      const matching = session.executions.filter(e => executions.includes(e.id));
      if (session.role === 'R1' && session.units.includes(unit)) connections.append(link('R1 Session →', { view: 'session', session: session.id, unit, hypothesis: h.id, node: null, execution: null }, state));
      for (const execution of matching) connections.append(link(`R2 Session · ${execution.attributes?.outcome || execution.status || 'unknown'} →`, { view: 'session', session: session.id, unit, hypothesis: h.id, node: null, execution: execution.id }, state));
    }
    card.append(connections, details('Raw business evidence', [record, ...related])); parent.append(card);
  }
  const operations = el('details'); operations.append(el('summary', `Business operations · ${nodes.length}`));
  operations.ontoggle = () => {
    if (!operations.open || operations.children.length > 1) return;
    for (const op of nodes) {
      const line = el('div', null, 'operation'); line.append(el('strong', op.name || op.operation), badge(op.status));
      const ref = op.attributes?.session_ref;
      if (ref?.session_id && ['tool.execution','model.attempt','llm.request'].includes(op.name)) {
        const kind = op.name === 'tool.execution' ? 'tool_call' : 'model_call';
        const node = `call:${JSON.stringify([ref.run_id, kind, ref.execution_id, ref.attempt])}`;
        line.append(link('Session →', { view: 'session', session: ref.session_id, node, unit, execution: null }, state));
        if (kind === 'model_call') line.append(link('Context →', { view: 'context', session: ref.session_id, node, unit }, state));
      }
      if (op.attributes?.error) line.append(el('p', op.attributes.error, 'notice'));
      line.append(details('Operation facts', op)); operations.append(line);
    }
  };
  parent.append(operations, details('Job goal and completion', { goal: job.goal, unit: result.unit }));
  if (selected) requestAnimationFrame(() => document.getElementById(selected)?.scrollIntoView({ block: 'start' }));
}
