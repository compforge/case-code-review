import { el, button, link, details, number, field, notices } from './ui.mjs';
import { renderCallBody } from './session.mjs';

const encode = value => JSON.stringify(value);
const bytes = value => new TextEncoder().encode(typeof value === 'string' ? value : encode(value)).length;
const messageText = message => typeof message.content === 'string' ? message.content : (message.content || []).map(part => part.text ?? part.thinking ?? encode(part)).join('\n');

// Exact multiset matching preserves repeated-message multiplicity. It does not
// guess whether an unmatched message was semantically rewritten by compaction.
export function compareMessages(previous, current) {
  const positions = new Map();
  previous.forEach((message, index) => { const key = encode(message); if (!positions.has(key)) positions.set(key, []); positions.get(key).push(index); });
  const retained = [], added = [], used = new Set();
  current.forEach((message, index) => { const old = positions.get(encode(message))?.shift(); if (old == null) added.push(index); else { retained.push({ previous: old, current: index }); used.add(old); } });
  return { retained, added, removed: previous.map((_, index) => index).filter(index => !used.has(index)) };
}
export function duplicateMessages(messages) {
  const first = new Map(), duplicates = new Map();
  messages.forEach((message, index) => { const key = encode(message); if (first.has(key)) duplicates.set(index, first.get(key)); else first.set(key, index); });
  return duplicates;
}
function messageOutline(messages, container) {
  const duplicates = duplicateMessages(messages);
  const filter = el('input'); filter.placeholder = 'Filter messages or headings'; filter.setAttribute('aria-label', 'Filter context messages');
  const mode = el('select'); mode.setAttribute('aria-label', 'Message subset'); for (const [v, label] of [['all', 'All messages'], ['user', 'User messages / task inputs'], ['duplicate', 'Exact repeated messages']]) { const o = el('option', label); o.value = v; mode.append(o); }
  const controls = el('div', null, 'filters'); controls.append(filter, mode); container.append(controls);
  const list = el('div', null, 'message-list'); container.append(list);
  const rows = messages.map((message, index) => {
    const text = messageText(message); const headings = text.split('\n').filter(line => /^#{1,6}\s/.test(line)).map(line => line.trim());
    const d = el('details', null, 'message'); d.id = `message-${index}`;
    const summary = el('summary'); summary.append(el('strong', `#${index + 1} · ${message.role || 'unknown'}`), el('span', `${number(bytes(message))} JSON bytes`, 'muted'), el('span', `${headings[0] || text.slice(0, 120) || 'Structured content'}${duplicates.has(index) ? ` · repeats #${duplicates.get(index) + 1}` : ''}`, 'message-preview'));
    d.append(summary);
    if (headings.length) d.append(el('div', headings.join(' / '), 'headings'));
    let loaded = false;
    d.ontoggle = () => {
      if (!d.open || loaded) return; loaded = true;
      if (typeof message.content === 'string') d.append(el('pre', message.content));
      else for (const part of message.content || []) d.append(details(part.type || 'Content part', part.text ?? part.thinking ?? part, false));
      d.append(details('Exact message JSON', message));
    };
    list.append(d); return { d, text: `${message.role} ${text}`.toLowerCase(), role: message.role, duplicate: duplicates.has(index) };
  });
  const apply = () => { rows.forEach(row => { row.d.hidden = !row.text.includes(filter.value.toLowerCase()) || mode.value === 'user' && row.role !== 'user' || mode.value === 'duplicate' && !row.duplicate; }); };
  filter.oninput = apply; mode.onchange = apply;
  return duplicates.size;
}

export async function renderContext(parent, page, state, signal) {
  parent.append(el('div', 'CONTEXT VIEW', 'eyebrow'), el('h2', 'What the model actually received'), el('p', 'Ordered request messages and tool schemas. Response is separate from historical assistant messages. Sizes below are serialized UTF-8 bytes, not billed tokens.', 'muted'));
  const calls = page.items.filter(i => i.kind === 'model_call');
  if (!calls.length) { parent.append(el('p', 'No model calls recorded in this Session.', 'empty')); return; }
  const requested = state.params.get('node'); const selected = requested ? calls.find(i => i.id === requested) : calls[0];
  state.selectDefault('node',selected?.id);
  const nav = el('div', null, 'filters');
  const picker = el('select'); picker.setAttribute('aria-label', 'Model request');
  for (const [i, call] of calls.entries()) { const o = el('option', `#${i + 1} · ${call.status} · ${call.summary?.slice(0, 90) || call.id}`); o.value = call.id; o.selected = call === selected; picker.append(o); }
  picker.onchange = () => { const a = link('', { view: 'context', session: page.session.id, node: picker.value }, state); parent.append(a); a.click(); a.remove(); };
  nav.append(picker); parent.append(nav);
  if (!selected) { parent.append(el('p', 'Selected model call is not present in this Session. Choose a recorded request.', 'notice')); return; }
  nav.append(link('Session →', { view: 'session', session: page.session.id, node: selected.id }, state));
  const loading = el('p', 'Loading this request…', 'loading'); parent.append(loading);
  const current = await state.call(page.session.id, selected.id);
  if (signal.aborted) return; loading.remove(); notices(parent, current.issues);
  const request = current.node.request?.input;
  if (!request || !Array.isArray(request.messages)) { parent.append(el('p', 'Actual request was not recorded. Its contents are unknown.', 'notice'), renderCallBody(current)); return; }
  const messages = request.messages;
  parent.append(el('code', selected.id), el('p', `${messages.length} messages · ${request.tools?.length || 0} tools · ${number(bytes(request))} request JSON bytes`, 'metrics'));
  const compare = el('section', null, 'comparison');
  const compareButton = button('Compare with previous model request', async () => {
    compareButton.disabled = true;
    try {
      const previous = await state.call(page.session.id, selected.previous);
      if (signal.aborted) return;
      const old = previous.node.request?.input;
      if (!Array.isArray(old?.messages)) { compare.append(el('p', 'Previous request not recorded; differences are unknown.', 'notice')); return; }
      const diff = compareMessages(old.messages, messages);
      compare.append(el('p', `${diff.retained.length} retained exactly · ${diff.added.length} added / changed · ${diff.removed.length} removed / replaced`), el('p', encode(old.tools) === encode(request.tools) ? 'Tool schemas unchanged.' : 'Tool schemas changed.'), link('Open previous Context →', { view: 'context', session: page.session.id, node: selected.previous }, state));
      const changes = el('div', null, 'diff-columns');
      const added = el('section'); added.append(el('h4', 'Added / changed'));
      for (const index of diff.added) added.append(button(`#${index + 1} · ${messages[index].role}`, () => { const row = document.getElementById(`message-${index}`); row.hidden = false; row.open = true; row.scrollIntoView({ block: 'center' }); }));
      const removed = el('section'); removed.append(el('h4', 'Removed / replaced'));
      for (const index of diff.removed) removed.append(details(`Previous #${index + 1} · ${old.messages[index].role}`, old.messages[index]));
      changes.append(added, removed); compare.append(changes);
      if (diff.removed.length && diff.added.length) compare.append(el('p', 'Replacement candidates are shown as removal + addition. Semantic equivalence is not inferred.', 'muted'));
      const left = page.items.findIndex(i => i.id === selected.previous), right = page.items.findIndex(i => i.id === selected.id);
      const compacted = page.items.slice(left + 1, right).filter(i => i.name.includes('context_compact'));
      if (compacted.length) compare.append(el('p', `${compacted.length} recorded compaction events between these requests.`, 'notice'), link('Inspect compaction in Session →', { view: 'session', session: page.session.id, node: compacted[0].id }, state));
    } catch (error) { if (!signal.aborted) { compare.append(el('p', error.message, 'notice')); compareButton.disabled = false; } }
  });
  compareButton.disabled = !selected.previous;
  compare.append(compareButton); if (!selected.previous) compare.append(el('p', 'First recorded model request in this Session.', 'muted')); parent.append(compare);
  parent.append(el('h3', 'Request message outline'));
  const duplicates = messageOutline(messages, parent);
  parent.append(el('p', `${duplicates} exact repeated messages. Repetition is an observation, not a quality verdict.`, 'muted'), details('Tool schemas', request.tools || []), details('Call configuration', request.config || {}), details('Exact request JSON', request));
  const response = el('section', null, 'response-section'); response.append(el('h2', 'Current response'), renderCallBody(current)); parent.append(response);
}
