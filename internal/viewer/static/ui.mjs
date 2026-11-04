export const $ = id => document.getElementById(id);
export function el(tag, text, cls) {
  const node = document.createElement(tag);
  if (text != null) node.textContent = String(text);
  if (cls) node.className = cls;
  return node;
}
export function append(parent, ...children) { parent.append(...children.filter(Boolean)); return parent; }
export function button(text, fn, cls = '') { const b = el('button', text, cls); b.type = 'button'; b.onclick = fn; return b; }
export function link(text, changes, state) { const a = el('a', text); a.href = state.url(changes); return a; }
export function details(title, value, open = false) {
  const d = el('details'); d.open = open; d.append(el('summary', title));
  d.addEventListener('toggle', () => { if (d.open && d.children.length === 1) d.append(el('pre', typeof value === 'string' ? value : JSON.stringify(value, null, 2))); });
  if (open) d.append(el('pre', typeof value === 'string' ? value : JSON.stringify(value, null, 2)));
  return d;
}
export const number = value => value == null ? 'unknown' : Number(value).toLocaleString();
export const duration = ms => ms == null ? 'unknown' : ms < 1000 ? `${ms}ms` : ms < 60000 ? `${(ms / 1000).toFixed(1)}s` : `${(ms / 60000).toFixed(1)}m`;
export function badge(text) { return el('span', text || 'unknown', `badge ${/fail|error|timeout|cancel/.test(text) ? 'bad' : /unknown|incomplete|partial/.test(text) ? 'warn' : ''}`); }
export function notices(parent, issues) { if (issues?.length) parent.append(details(`Recording gaps · ${issues.length}`, issues.join('\n'))); }
export function empty(parent, title, text) { parent.append(el('h2', title), el('p', text, 'empty')); }
export function field(parent, label, value) {
  if (value == null || value === '') return;
  const row = el('div', null, 'field'); row.append(el('h4', label));
  if (Array.isArray(value)) { const ul = el('ul'); value.forEach(v => ul.append(el('li', typeof v === 'string' ? v : JSON.stringify(v)))); row.append(ul); }
  else row.append(el('p', value));
  parent.append(row);
}
export function flatten(nodes) { return (nodes || []).flatMap(n => [n, ...flatten(n.nodes)]); }
export function meta(node) { return `${node.status || 'unknown'} · ${duration(node.duration_ms)} · attempt ${node.ref?.attempt || '—'}`; }
