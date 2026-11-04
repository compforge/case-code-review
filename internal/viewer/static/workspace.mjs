import { $, el, button, empty, notices, link } from './ui.mjs';
import { renderJob } from './job.mjs';
import { renderSession, findToolSessions } from './session.mjs';
import { renderContext } from './context.mjs';

const review = JSON.parse($('review-data').textContent);
const base = location.pathname.replace(/\/$/, '');
const cache = new Map();
let active;
const state = {
  review,
  params: new URLSearchParams(location.search),
  url(changes) {
    const p = new URLSearchParams(state.params);
    for (const [key, value] of Object.entries(changes)) value ? p.set(key, value) : p.delete(key);
    return `${base}?${p}`;
  },
  selectDefault(key,value) {
    if(value && !state.params.has(key)) {state.params.set(key,value);history.replaceState(null,'',`${base}?${state.params}`);}
  },
  async get(endpoint, values = {}) {
    const url = `${base}/${endpoint}?${new URLSearchParams(values)}`;
    // Keep a small bounded cache; bodies may contain hundreds of messages.
    if (cache.has(url)) return cache.get(url);
    const signal = active.signal;
    const response = await fetch(url, { signal });
    if (!response.ok) throw new Error(await response.text());
    const result = await response.json();
    if (signal.aborted) throw new DOMException('View changed', 'AbortError');
    if (cache.size >= 6) cache.delete(cache.keys().next().value);
    cache.set(url, result); return result;
  },
  async session(id) { return state.get('session', { session: id }); },
  async call(session, node) { return state.get('session', { session, node }); },
  detail(title, content) {
    const aside = $('detail'); aside.hidden = false;
    const toolbar = el('div', null, 'detail-toolbar');
    toolbar.append(el('h3', title), button('Expand', () => aside.classList.toggle('expanded')), button('Close', () => { aside.hidden = true; aside.classList.remove('expanded'); }));
    aside.replaceChildren(toolbar, content);
  },
  isActive(signal) { return !signal.aborted; },
};

async function render() {
  active?.abort(); active = new AbortController();
  const signal = active.signal;
  state.params = new URLSearchParams(location.search);
  const view = ['job', 'session', 'context'].includes(state.params.get('view')) ? state.params.get('view') : 'job';
  document.querySelectorAll('[data-view]').forEach(a => { a.href = state.url({ view: a.dataset.view }); a.setAttribute('aria-current', a.dataset.view === view ? 'page' : 'false'); });
  $('navigation').replaceChildren(); $('content').replaceChildren(); $('detail').hidden = true;
  $('detail').classList.remove('expanded');
  try {
    if (view === 'job') {
      const jobs = review.Jobs || [];
      $('navigation').append(el('h3', `Unit Jobs · ${jobs.length}`));
      const input = el('input'); input.placeholder = 'Filter paths or Unit ID'; input.setAttribute('aria-label', 'Filter Units'); $('navigation').append(input);
      const list = el('div'); $('navigation').append(list);
      const selected = state.params.get('unit') || jobs[0]?.id;
      state.selectDefault('view',view);state.selectDefault('unit',selected);
      const populate = () => { list.replaceChildren(); for (const job of jobs) {
        if (!`${job.id} ${job.paths}`.toLowerCase().includes(input.value.toLowerCase())) continue;
        const a = link(job.paths?.[0] || job.id, { view: 'job', unit: job.id, hypothesis: null }, state); a.className = `nav-item ${job.id === selected ? 'selected' : ''}`;
        a.append(el('small', `${job.outcome || 'unknown'} · ${review.JobCounts?.[job.id]?.Hypotheses || 0} hypotheses · ${review.JobCounts?.[job.id]?.Findings || 0} findings`)); list.append(a);
      } }; populate(); input.oninput = populate;
      if (!selected) {
        empty($('content'), 'No Unit Jobs recorded', 'This Review has no recorded Unit agent work. A documentation-only review can complete normally without a Session.');
      } else {
        $('content').append(el('p', 'Loading business decisions…', 'loading'));
        const job = await state.get('job', { unit: selected });
        if (signal.aborted) return;
        $('content').replaceChildren(); renderJob($('content'), job, state);
      }
      const ops = review.Operations || [];
      const summary = el('details', null, 'review-operations'); summary.append(el('summary', 'Review operations'));
      summary.ontoggle = () => { if (summary.open && summary.children.length === 1) for (const op of ops) summary.append(el('p', `${op.name} · ${op.status || 'unknown'}${op.error ? ` · ${op.error}` : ''}`)); };
      $('content').append(summary);
    } else {
      const sessions = review.Sessions || [];
      let id = state.params.get('session');
      const tool = state.params.get('tool');
      if (!id && tool) {
        $('content').append(el('p','Locating the recorded tool call across associated Sessions…','loading'));
        const matches = await findToolSessions(sessions,state.params.get('unit'),tool,id=>state.session(id));
        if(signal.aborted) return;
        $('content').replaceChildren();
        if(matches.length!==1) {
          empty($('content'),matches.length ? 'Multiple Session matches' : 'Tool receipt unavailable',matches.length ? 'The same tool ID occurs in multiple native Sessions. Choose the recorded source explicitly.' : 'No matching tool call was found in this Unit’s associated Sessions. Missing recordings remain possible.');
          for(const match of matches) $('content').append(link(`${match.role} · ${match.id} · ${match.count} matches`,{session:match.id},state));
          $('content').append(link('Browse Sessions →',{tool:null,session:sessions[0]?.id},state));
          return;
        }
        id=matches[0].id;
      }
      id ||= sessions[0]?.id;
      state.selectDefault('view',view);state.selectDefault('session',id);
      $('navigation').append(el('h3', `Sessions · ${sessions.length}`));
      for (const session of sessions) {
        const a = link(`${session.role}${session.lane ? ` · ${session.lane}` : ''}`, { view, session: session.id, node: null, execution: null, tool:null }, state);
        a.className = `nav-item ${session.id === id ? 'selected' : ''}`;
        a.append(el('small', (review.Jobs || []).find(j => session.units.includes(j.id))?.paths?.[0] || session.id), el('small', `${session.executions?.length || 0} Executions · ${session.units?.length || 0} Units`)); $('navigation').append(a);
      }
      if (!id) { empty($('content'), 'No native Session recorded', 'No agent work is recorded for this Review. Review completion and recording availability are independent.'); return; }
      $('content').append(el('p', 'Reading the selected native Session…', 'loading'));
      const session = await state.session(id);
      if (signal.aborted) return;
      $('content').replaceChildren(); notices($('content'), session.issues);
      if (view === 'session') renderSession($('content'), session, state, signal);
      else await renderContext($('content'), session, state, signal);
    }
  } catch (error) {
    if (error.name !== 'AbortError' && !signal.aborted) { $('content').replaceChildren(el('h2', 'Evidence unavailable'), el('p', error.message, 'notice'), button('Retry', () => { cache.clear(); render(); })); }
  }
}

document.addEventListener('click', event => {
  const a = event.target.closest('a');
  if (!a || event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || a.target) return;
  const url = new URL(a.href, location.href);
  if (url.origin !== location.origin || url.pathname !== base || !url.searchParams.has('view')) return;
  event.preventDefault(); history.pushState(null, '', url); render();
});
window.addEventListener('popstate', render);
$('refresh').onclick = () => location.reload();
render();
