import { callStatistics, renderCallStatistics, renderMeasurements } from './measurements.mjs';
import { el, badge, duration, number, meta, button, link, details, field, notices } from './ui.mjs';

export function renderCallBody(result) {
  const parent = el('div', null, 'call-detail'); const node = result.node;
  parent.append(badge(node.status), el('code', node.id)); notices(parent, result.issues);
  if (node.response?.error) field(parent, 'Call error', node.response.error.message);
  if (result.kind === 'model_call') {
    const response = node.response;
    if (!response) parent.append(el('p', 'Response not recorded', 'notice'));
    else {
      const body = response.output;
      parent.append(el('h3', 'Current response'));
      for (const part of body?.content || []) parent.append(details(part.type || 'Content part', part.text ?? part.thinking ?? part));
      parent.append(details('Usage', response.usage || 'Unknown'), details('Response facts', response));
    }
  } else if (result.kind === 'tool_call') {
    parent.append(details('Actual tool input', node.request?.input ?? 'Not recorded'));
    if (result.searches?.length) {
      parent.append(el('h3', 'Search query outcomes'));
      for (const [i, search] of result.searches.entries()) {
        const row = el('div', null, 'search-result'); row.append(badge(search.status), el('strong', `${i + 1}. ${search.query}`), el('small', (search.scope || []).join(', ')), details('Recorded evidence', search.evidence || 'Not recorded')); parent.append(row);
      }
    }
    parent.append(details('Tool output', node.response?.output ?? 'Not recorded'));
  } else parent.append(details('Recorded event', node));
  return parent;
}

export function renderSession(parent, page, state, signal) {
  const { session, items } = page;
  parent.append(el('div', 'SESSION VIEW', 'eyebrow'), el('h2', session.role), el('code', session.id));
  const models = items.filter(i => i.kind === 'model_call');
  renderCallStatistics(parent, callStatistics([page]));
  renderMeasurements(parent, page.measurements, session.id);
  const focus = state.params.get('execution');
  if (focus) parent.append(el('p', `Highlighted Execution ${focus}. Full Session remains visible; totals cover the full Session.`, 'notice'));
  const controls = el('div', null, 'filters');
  const filter = el('input'); filter.placeholder = 'Filter calls, tools or errors'; filter.setAttribute('aria-label', 'Filter Session');
  const kind = el('select'); kind.setAttribute('aria-label', 'Call kind'); for (const [v, label] of [['', 'All events'], ['model_call', 'Model calls'], ['tool_call', 'Tool calls'], ['failed', 'Failures / query errors']]) { const o = el('option', label); o.value = v; kind.append(o); }
  const sort = el('select'); sort.setAttribute('aria-label', 'Sort calls'); for (const [v, label] of [['source', 'Recorded order'], ['duration', 'Longest duration'], ['input', 'Largest context']]) { const o = el('option', label); o.value = v; sort.append(o); }
  const fragment = el('input'); fragment.type = 'checkbox'; fragment.id = 'fragment'; fragment.disabled = !focus;
  const label = el('label', 'Selected Execution only'); label.prepend(fragment);
  controls.append(filter, kind, sort, label); parent.append(controls);
  const scope = el('p', null, 'muted'); const rows = el('div', null, 'call-list'); parent.append(scope, rows);
  const maxInput = Math.max(1, ...models.map(i => (i.usage?.input_tokens || 0) + (i.usage?.cache_write_input_tokens || 0)));
  const selected = state.params.get('node');
  filter.value=state.params.get('tool') || '';
  if(focus) controls.append(button('Jump to selected Execution',()=>rows.querySelector('.focused')?.scrollIntoView({block:'center'})));
  async function show(item) {
    state.detail(item.name, el('p', 'Loading recorded call…', 'loading'));
    try {
      const result = await state.call(session.id, item.id);
      if (signal.aborted) return;
      const body = renderCallBody(result);
      body.append(details('Native identity', { session: session.id, ref: item.ref, operation: item.operation_id }));
      state.detail(item.name, body);
    } catch (error) { if (!signal.aborted) state.detail('Evidence unavailable', el('p', error.message, 'notice')); }
  }
  const draw = () => {
    rows.replaceChildren();
    let visible = items.filter(i => (!fragment.checked || i.execution_id === focus) && (!kind.value || kind.value === i.kind || kind.value === 'failed' && (i.status === 'failed' || i.searches?.some(q => q.status === 'error' || q.status === 'incomplete'))) && `${i.name} ${i.summary} ${i.error} ${i.id} ${i.tool_call_id}`.toLowerCase().includes(filter.value.toLowerCase()));
    if (sort.value !== 'source') visible = [...visible].sort((a, b) => sort.value === 'duration' ? (b.duration_ms ?? -1) - (a.duration_ms ?? -1) : ((b.usage?.input_tokens ?? -1) + (b.usage?.cache_write_input_tokens || 0)) - ((a.usage?.input_tokens ?? -1) + (a.usage?.cache_write_input_tokens || 0)));
    scope.textContent = `${fragment.checked ? 'Execution fragment' : 'Full Session'} · showing ${visible.length} / ${items.length} events. ${sort.value === 'source' ? 'Recorded sequence; parallel calls can overlap.' : 'Sorted for inspection; this is not execution order.'}`;
    let lastExecution;
    for (const item of visible) {
      if (sort.value === 'source' && item.execution_id && item.execution_id !== lastExecution) {
        lastExecution = item.execution_id;
        const execution = session.executions.find(e => e.id === lastExecution);
        rows.append(el('div', `Execution ${lastExecution}${item.hypothesis ? ` · ${item.hypothesis}` : ''} · ${execution?.attributes?.outcome || execution?.status || 'unknown'}`, 'segment'));
      }
      const row = el('article', null, `call-row ${item.kind==='operation' ? 'event-row' : ''} ${item.id === selected ? 'selected' : ''} ${focus && item.execution_id === focus ? 'focused' : ''}`);
      const main = el('div', null, 'call-main');
      main.append(link(item.name, { view: 'session', session: session.id, node: item.id, context: item.kind === 'model_call' ? '1' : null }, state)); if(item.kind!=='operation') main.append(badge(item.status));
      if(item.model_call_id) main.append(link('Requesting model →',{view:'session',session:session.id,node:item.model_call_id,tool:null,context:'1'},state));
      row.append(main, el('p', item.error || item.summary || item.tool_call_id || item.id, 'call-preview'), el('small', item.kind==='operation' ? item.started_at : meta(item)));
      if (item.kind === 'model_call') {
        row.append(el('small', `Input ${number(item.usage ? (item.usage.input_tokens ?? 0) + (item.usage.cache_write_input_tokens ?? 0) : null)} · output ${number(item.usage?.output_tokens)} · cache read ${number(item.usage?.cache_read_input_tokens)}`));
        const bar = el('meter'); bar.min = 0; bar.max = maxInput; bar.value = (item.usage?.input_tokens || 0) + (item.usage?.cache_write_input_tokens || 0); bar.setAttribute('aria-label', 'Relative context tokens'); row.append(bar);
      }
      if (item.searches?.length) {
        const counts = new Map(); for (const q of item.searches) counts.set(q.status, (counts.get(q.status) || 0) + 1);
        const results = el('div', null, 'query-outcomes'); for (const [status, count] of counts) results.append(badge(`${status}: ${count}`)); row.append(results);
      }
      rows.append(row);
    }
  };
  filter.oninput = draw; kind.onchange = draw; sort.onchange = draw; fragment.onchange = draw; draw();
  if (selected && state.params.get('context') !== '1') { const item = items.find(i => i.id === selected); if (item) show(item); else parent.prepend(el('p', 'Selected node is not present in this Session snapshot.', 'notice')); }
}

// Receipts can refer to R1 evidence or any earlier call in the shared Lane.
// Resolve using explicit Unit membership and native tool IDs, never a preferred phase.
export async function findToolSessions(sessions, unit, tool, load) {
  const matches = [];
  for (const session of sessions.filter(s => !unit || s.units.includes(unit))) {
    const page = await load(session.id);
    const count = page.items.filter(i => i.tool_call_id === tool).length;
    if (count) matches.push({ id: session.id, role: session.role, count });
  }
  return matches;
}
