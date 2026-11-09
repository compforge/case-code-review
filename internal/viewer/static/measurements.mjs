import { el, number, duration, details } from './ui.mjs';

// One implementation serves the full Session and the deduplicated Review.
export function callStatistics(pages) {
  const result = { models: 0, tools: 0, usageKnown: 0, tokenKnown: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 }, modelTimeKnown: 0, toolTimeKnown: 0, input: 0, output: 0, cacheRead: 0, cacheWrite: 0, modelTime: 0, toolTime: 0, issues: 0 };
  const seen = new Set();
  for (const page of pages) {
    if (seen.has(page.session.id)) continue;
    seen.add(page.session.id);
    result.issues += page.issues?.length || 0;
    for (const item of page.items || []) {
      if (item.kind === 'model_call') {
        result.models++;
        if (item.usage != null) {
          result.usageKnown++;
          for (const [field, key] of [['input', 'input_tokens'], ['output', 'output_tokens'], ['cacheRead', 'cache_read_input_tokens'], ['cacheWrite', 'cache_write_input_tokens']]) {
            if (item.usage[key] != null) {
              result.tokenKnown[field]++;
              result[field] += item.usage[key] + (field === 'input' ? (item.usage.cache_write_input_tokens || 0) : 0);
            }
          }
        }
        if (item.duration_ms != null) { result.modelTimeKnown++; result.modelTime += item.duration_ms; }
      } else if (item.kind === 'tool_call') {
        result.tools++;
        if (item.duration_ms != null) { result.toolTimeKnown++; result.toolTime += item.duration_ms; }
      }
    }
  }
  return result;
}

export function renderCallStatistics(parent, stats) {
  parent.append(el('h3', 'Observed call statistics'));
  const grid = el('div', null, 'metric-grid');
  const metrics = [
    ['Model calls', number(stats.models), 'Recorded calls'], ['Tool calls', number(stats.tools), 'Recorded executions'],
    ['Input tokens', number(stats.tokenKnown.input ? stats.input : null), `Includes cache read and cache write · ${stats.tokenKnown.input}/${stats.models} calls`],
    ['Output tokens', number(stats.tokenKnown.output ? stats.output : null), `${stats.tokenKnown.output}/${stats.models} calls`],
    ['Cache read', number(stats.tokenKnown.cacheRead ? stats.cacheRead : null), `Subset of input tokens · ${stats.tokenKnown.cacheRead}/${stats.models} calls`],
    ['Cache write', number(stats.tokenKnown.cacheWrite ? stats.cacheWrite : null), `Subset of input tokens · ${stats.tokenKnown.cacheWrite}/${stats.models} calls`],
    ['Model time', duration(stats.modelTimeKnown ? stats.modelTime : null), 'Sum of recorded call durations'],
    ['Tool time', duration(stats.toolTimeKnown ? stats.toolTime : null), 'Sum of recorded execution durations'],
  ];
  for (const [name, value, note] of metrics) {
    const card = el('article', null, 'metric-card');
    card.append(el('h3', name), el('div', value, 'metric-value'), el('p', note, 'muted')); grid.append(card);
  }
  parent.append(grid, el('p', `Usage ${stats.usageKnown}/${stats.models} model calls · model duration ${stats.modelTimeKnown}/${stats.models} · tool duration ${stats.toolTimeKnown}/${stats.tools}. Cumulative time can overlap and is not wall time.`, 'muted'));
  if (stats.issues) parent.append(el('p', `${stats.issues} recording gaps; these are observed totals and may be incomplete.`, 'notice'));
}

export function renderMeasurements(parent, snapshot, sessionID) {
  parent.append(el('h3', 'Measurements'));
  if (snapshot?.status !== 'ready') {
    const messages = { missing: 'No measurement snapshot has been generated.', stale: 'Measurements are out of date because the recording changed. Regenerate the snapshot.', error: 'Measurement snapshot could not be read.' };
    parent.append(el('p', messages[snapshot?.status] || messages.missing, 'notice')); return;
  }
  const projection = snapshot.views;
  const view = sessionID ? projection.sessions.find(item => item.session.id === sessionID) : projection.global;
  if (!view?.values?.length) { parent.append(el('p', 'No measurements recorded for this scope.', 'muted')); return; }
  parent.append(el('p', `Measured ${projection.created_at} · ${projection.run_id}`, 'muted'));
  const table = el('table'); const head = el('tr');
  for (const name of ['Measurement', 'Value', 'Aggregation', 'Coverage', 'Status']) head.append(el('th', name));
  table.append(head);
  for (const item of view.values) {
    const row = el('tr');
    const name = el('td', `${item.measurer_title} · ${item.name}`);
    if (item.description) name.append(el('small', item.description));
    const value = item.value == null ? 'unknown' : item.unit === 'ms' ? duration(item.value) : `${number(item.value)} ${item.unit || ''}`;
    row.append(name, el('td', value), el('td', item.aggregation || '—'), el('td', Object.entries(item.coverage || {}).map(([key, count]) => `${count} ${key}`).join(' · ')), el('td', item.status));
    table.append(row);
    if (sessionID && item.evidence?.length) {
      const evidence = el('tr'), cell = el('td'); cell.colSpan = 5;
      cell.append(details('Recorded measurement evidence', item.evidence)); evidence.append(cell); table.append(evidence);
    }
  }
  parent.append(table);
}
