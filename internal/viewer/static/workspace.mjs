import { $, el, button, empty, notices, link } from './ui.mjs';
import { renderJob } from './job.mjs';
import { renderSession, findToolSessions } from './session.mjs';
import { renderContext } from './context.mjs';
import { renderGlobal } from './global.mjs';
import { workspaceParams, workspaceURL } from './navigation.mjs';

const review = JSON.parse($('review-data').textContent);
const base = location.pathname.replace(/\/$/, '');
const cache = new Map();
let active;
const state = {
  review,
  params: new URLSearchParams(location.search),
  url(changes) {
    return workspaceURL(base, state.params, changes);
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
    toolbar.append(el('h3', title), button('Expand', () => aside.classList.toggle('expanded')), button('Close', () => { aside.hidden = true; aside.classList.remove('expanded'); state.params.delete('context'); state.params.delete('node'); history.replaceState(null, '', state.url({})); }));
    aside.replaceChildren(toolbar, content);
  },
  isActive(signal) { return !signal.aborted; },
};

async function render() {
  active?.abort(); active = new AbortController();
  const signal = active.signal;
  state.params = workspaceParams(location.search);
  history.replaceState(null, '', `${base}?${state.params}`);
  const view = state.params.get('view');
  $('workspace').dataset.view = view;
  $('selection').hidden = view === 'global';
  document.querySelectorAll('[data-view]').forEach(a => { a.href = state.url({ view: a.dataset.view, unit: null, hypothesis: null, session: null, execution: null, node: null, tool: null, context: null, tab: null }); a.setAttribute('aria-current', a.dataset.view === view ? 'page' : 'false'); });
  $('selection').replaceChildren(); $('content').replaceChildren(); $('detail').hidden = true;
  $('detail').classList.remove('expanded');
  try {
    if (view === 'global') {
      $('content').append(el('p', 'Reading observed Session totals…', 'loading'));
      const pages = [];
      const sessions = [...new Map((review.Sessions || []).map(session => [session.id, session])).values()];
      for (let i = 0; i < sessions.length; i += 4) {
        pages.push(...await Promise.all(sessions.slice(i, i + 4).map(async session => {
          try { return await state.session(session.id); }
          catch (error) { if (signal.aborted) throw error; return { session, items: [], issues: [error.message] }; }
        })));
      }
      const measurements = await state.get('measurements').catch(error => { if (signal.aborted) throw error; return { status: 'error' }; });
      if (signal.aborted) return;
      $('content').replaceChildren();
      renderGlobal($('content'), review, pages, measurements);
    } else if (view === 'job') {
      const jobs = review.Jobs || [];
      $('selection').append(el('h3', `Unit Jobs · ${jobs.length}`));
      const input = el('input'); input.placeholder = 'Filter paths or Unit ID'; input.setAttribute('aria-label', 'Filter Units'); $('selection').append(input);
      const list = el('div'); $('selection').append(list);
      const selected = state.params.get('unit');
      const populate = () => { list.replaceChildren(); for (const job of jobs) {
        if (!`${job.id} ${job.paths}`.toLowerCase().includes(input.value.toLowerCase())) continue;
        const a = link(job.paths?.[0] || job.id, { view: 'job', unit: job.id, hypothesis: null, session: null, execution: null, node: null, tool: null, context: null, tab: null }, state); a.className = `nav-item ${job.id === selected ? 'selected' : ''}`;
        a.append(el('small', `${job.outcome || 'unknown'} · ${review.JobCounts?.[job.id]?.Hypotheses || 0} hypotheses · ${review.JobCounts?.[job.id]?.Findings || 0} findings`)); list.append(a);
      } }; populate(); input.oninput = populate;
      if (!selected) {
        empty($('content'), jobs.length ? 'Select a Job' : 'No Unit Jobs recorded', jobs.length ? 'Choose a Job from the list to inspect its business decisions.' : 'This Review has no recorded Unit agent work. A documentation-only review can complete normally without a Session.');
        return;
      }
      $('content').append(el('p', 'Loading business decisions…', 'loading'));
      const job = await state.get('job', { unit: selected });
      if (signal.aborted) return;
      $('content').replaceChildren(); renderJob($('content'), job, state);
      const ops = review.Operations || [];
      const summary = el('details', null, 'review-operations'); summary.append(el('summary', 'Review operations'));
      summary.ontoggle = () => { if (summary.open && summary.children.length === 1) for (const op of ops) summary.append(el('p', `${op.name} · ${op.status || 'unknown'}${op.error ? ` · ${op.error}` : ''}`)); };
      $('content').append(summary);
    } else {
      const sessions = review.Sessions || [];
      let id = state.params.get('session');
      $('selection').append(el('h3', `Sessions · ${sessions.length}`));
      const list = el('div'); $('selection').append(list);
      const populate = () => {
        list.replaceChildren();
        for (const session of sessions) {
          const a = link(`${session.role}${session.lane ? ` · ${session.lane}` : ''}`, { view, session: session.id, unit: null, hypothesis: null, node: null, execution: null, tool: null, context: null, tab: null }, state);
          a.className = `nav-item ${session.id === id ? 'selected' : ''}`;
          a.append(el('small', (review.Jobs || []).find(j => session.units.includes(j.id))?.paths?.[0] || session.id), el('small', `${session.executions?.length || 0} Executions · ${session.units?.length || 0} Units`)); list.append(a);
        }
      }; populate();
      const tool = state.params.get('tool');
      if (!id && tool) {
        $('content').append(el('p','Locating the recorded tool call across associated Sessions…','loading'));
        const matches = await findToolSessions(sessions,state.params.get('unit'),tool,id=>state.session(id));
        if(signal.aborted) return;
        $('content').replaceChildren();
        if(matches.length!==1) {
          empty($('content'),matches.length ? 'Multiple Session matches' : 'Tool receipt unavailable',matches.length ? 'The same tool ID occurs in multiple native Sessions. Choose the recorded source explicitly.' : 'No matching tool call was found in this Unit’s associated Sessions. Missing recordings remain possible.');
          for(const match of matches) $('content').append(link(`${match.role} · ${match.id} · ${match.count} matches`,{session:match.id},state));
          $('content').append(link('Browse Sessions →',{tool:null,session:null,node:null,execution:null,context:null,tab:null},state));
          return;
        }
        id=matches[0].id;
      }
      state.selectDefault('session',id); populate();
      if (!id) {
        empty($('content'), sessions.length ? 'Select a Session' : 'No native Session recorded', sessions.length ? 'Choose a Session from the list to inspect its overview or full recording.' : 'No agent work is recorded for this Review. Review completion and recording availability are independent.');
        return;
      }
      $('content').append(el('p', 'Reading the selected native Session…', 'loading'));
      const session = await state.session(id);
      if (signal.aborted) return;
      $('content').replaceChildren(); notices($('content'), session.issues);
      renderSession($('content'), session, state, signal);
    }
    if (view !== 'global' && state.params.get('context') === '1') await showContext(signal);
  } catch (error) {
    if (error.name !== 'AbortError' && !signal.aborted) { $('content').replaceChildren(el('h2', 'Evidence unavailable'), el('p', error.message, 'notice'), button('Retry', () => { cache.clear(); render(); })); }
  }
}

async function showContext(signal) {
  const body = el('div', null, 'context-detail');
  state.detail('Context · Model call', body);
  try {
    const id = state.params.get('session');
    if (!id || !state.params.get('node')) {
      body.append(el('p', 'Select a model call from Job or Session to inspect its context.', 'notice'));
      return;
    }
    const page = await state.session(id);
    if (!signal.aborted) await renderContext(body, page, state, signal);
  } catch (error) {
    if (!signal.aborted) body.append(el('p', error.message, 'notice'));
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
