import { el, badge, duration, number, field } from './ui.mjs';

/**
 * @spec Global covers one Review journal; shared Lane Sessions are counted once and Job outcomes are not quality scores
 * @see {@link component://docs/observability.md}
 */
export function reviewOverview(review) {
  const jobs = review.Jobs || [];
  const sessions = review.Sessions || [];
  const outcomes = new Map();
  for (const job of jobs) {
    const outcome = job.outcome || 'unknown';
    outcomes.set(outcome, (outcomes.get(outcome) || 0) + 1);
  }
  return {
    jobs, sessions, outcomes,
    completed: jobs.filter(job => job.outcome === 'completed' && !job.unassessed_hypotheses?.length).length,
    hypotheses: Object.values(review.JobCounts || {}).reduce((total, count) => total + count.Hypotheses, 0),
    executions: sessions.reduce((total, session) => total + (session.executions?.length || 0), 0),
  };
}

function metric(parent, title, value, note) {
  const card = el('article', null, 'metric-card');
  card.append(el('h3', title), el('div', value, 'metric-value'), el('p', note, 'muted'));
  parent.append(card);
}

export function renderGlobal(parent, review) {
  const overview = reviewOverview(review);
  const summary = review.Summary;
  parent.append(el('div', 'GLOBAL VIEW', 'eyebrow'), el('h2', 'Review overview'), el('p', 'One Review across all Unit Jobs and native Sessions.', 'muted'));
  const health = el('div', null, 'metric-grid');
  metric(health, 'Completion', summary.Completion?.status || 'unknown', 'Recorded pipeline outcome');
  metric(health, 'Wall time', duration(summary.HasDuration ? summary.DurationSec * 1000 : null), 'Whole Review elapsed time');
  metric(health, 'Completed Jobs', `${overview.completed} / ${overview.jobs.length}`, 'Completed Units with no unassessed hypotheses');
  metric(health, 'Findings', number(review.Findings), `${number(overview.hypotheses)} recorded hypotheses`);
  parent.append(health);
  const distribution = el('div', null, 'metrics');
  for (const [outcome, count] of overview.outcomes) distribution.append(badge(`${outcome}: ${count}`));
  distribution.append(el('span', `${overview.sessions.length} native Sessions · ${overview.executions} associated Executions`));
  parent.append(distribution);
  field(parent, 'Review error', summary.Completion?.error);
  parent.append(el('p', 'Completion and Finding counts describe execution and delivery. They do not measure review accuracy or prove publication to the Forge.', 'muted'));

  const usage = summary.Tokens;
  parent.append(el('h3', 'Token usage'));
  const costs = el('div', null, 'metric-grid');
  metric(costs, 'Input tokens', number(usage?.prompt_tokens), 'Includes cache read and cache write');
  metric(costs, 'Output tokens', number(usage?.completion_tokens), 'Recorded Review total');
  metric(costs, 'Cache read', number(usage ? (usage.cache_read_tokens ?? 0) : null), 'Subset of input tokens');
  metric(costs, 'Cache write', number(usage ? (usage.cache_write_tokens ?? 0) : null), 'Subset of input tokens');
  parent.append(costs, el('p', usage ? `${usage.estimated ? 'Includes estimated usage.' : 'Recorded usage.'} Totals come from Review finalization; per-call coverage is not recorded here. Inspect a Session for call-level gaps.` : 'Review usage was not recorded. Inspect available Sessions for observed call usage.', 'muted'));

}
