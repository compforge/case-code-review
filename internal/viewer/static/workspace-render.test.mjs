import test from 'node:test';
import assert from 'node:assert/strict';

// Exercise the workspace's actual render and navigation handlers without a browser dependency.
class Element {
  constructor(tag) {
    this.tagName = tag; this.children = []; this.dataset = {}; this.hidden = false;
    this.value = ''; this.textContent = ''; this.listeners = {};
    this.classList = { remove() {}, toggle() {} };
  }
  append(...items) { for (const item of items) { item.parent = this; this.children.push(item); } }
  prepend(...items) { for (const item of items) item.parent = this; this.children.unshift(...items); }
  replaceChildren(...items) { this.children = []; this.append(...items); }
  setAttribute(name, value) { (this.attributes ||= {})[name] = value; }
  addEventListener(name, fn) { this.listeners[name] = fn; }
  closest(tag) { return this.tagName === tag ? this : this.parent?.closest(tag); }
  remove() { this.parent.children = this.parent.children.filter(node => node !== this); }
}
const descendants = node => [node, ...node.children.flatMap(descendants)];
const settle = async () => { await new Promise(resolve => setImmediate(resolve)); };

/**
 * @case Model Context preserves native identity; closing it survives reload without losing Session scope
 * @see {@link component://docs/observability.md}
 */
test('model call details preserve evidence and explicit navigation state', async t => {
  const elements = Object.fromEntries(['review-data', 'detail', 'workspace', 'navigation', 'selection', 'content', 'refresh'].map(id => [id, new Element('div')]));
  const item = { id: 'call-1', kind: 'model_call', name: 'Model call', status: 'completed', ref: { run_id: 'run-1', attempt: 1 }, operation_id: 'operation-1' };
  const session = { id: 's1', role: 'unit-review', units: ['u1'], executions: [] };
  const call = { kind: 'model_call', node: { id: item.id, status: 'completed', request: { input: { messages: [], tools: [] } }, response: { output: { content: [] } } } };
  const sessions = [session, { ...session, id: 's2', role: 'lane' }];
  const jobs = [{ id: 'u1', paths: ['first.go'] }, { id: 'u2', paths: ['second.go'] }];
  elements.workspace.append(elements.navigation, elements.selection, elements.content, elements.detail);
  const menu = ['global', 'job', 'session'].map(view => {
    const a = new Element('a'); a.dataset.view = view; a.textContent = view;
    elements.navigation.append(a); return a;
  });
  elements['review-data'].textContent = JSON.stringify({ Sessions: sessions, Jobs: jobs });
  const events = {}, documentEvents = {};
  const navigate = url => {
    const next = new URL(url, location.href);
    location.search = next.search; location.href = next.href;
  };
  globalThis.document = {
    getElementById: id => elements[id], createElement: tag => new Element(tag), querySelectorAll: () => menu,
    addEventListener: (name, fn) => { documentEvents[name] = fn; },
  };
  globalThis.location = { pathname: '/r/repo/review', search: '?view=session&session=s1&unit=u1&node=call-1&context=1', href: 'http://localhost/r/repo/review', origin: 'http://localhost' };
  globalThis.history = { replaceState: (_, __, url) => navigate(url), pushState: (_, __, url) => navigate(url) };
  globalThis.window = { addEventListener: (name, fn) => { events[name] = fn; } };
  const requests = [];
  globalThis.fetch = async url => {
    requests.push(url);
    const request = new URL(url, location.href);
    return { ok: true, json: async () => {
      if (request.pathname.endsWith('/job')) {
        const job = jobs.find(job => job.id === request.searchParams.get('unit'));
        return { attributes: { unit_id: job.id }, goal: { paths: job.paths }, nodes: [] };
      }
      if (request.searchParams.has('node')) return call;
      return { session: sessions.find(session => session.id === request.searchParams.get('session')), items: [item] };
    } };
  };
  await import('./workspace.mjs');
  await settle();
  const openURL = async url => { navigate(url); await events.popstate(); await settle(); };
  const assertIdentity = () => {
    const identity = descendants(elements.detail).find(node => node.tagName === 'details' && node.children[0]?.textContent === 'Native identity');
    assert.ok(identity, 'Native identity is visible in the selected call detail');
    identity.open = true; identity.listeners.toggle();
    assert.deepEqual(JSON.parse(identity.children[1].textContent), { session: session.id, ref: item.ref, operation: item.operation_id });
  };

  /**
   * @case Job and Session menus show independent lists; item clicks alone load the detail pane
   * @see {@link component://docs/observability.md}
   */
  await t.test('scope menus show lists and item selection loads right-hand details', async () => {
    const click = async a => {
      documentEvents.click({ target: a, button: 0, preventDefault() {} });
      await settle();
    };
    requests.length = 0;
    await click(menu.find(a => a.dataset.view === 'job'));
    assert.equal(elements.selection.hidden, false);
    assert.equal(new URLSearchParams(location.search).has('unit'), false);
    assert.equal(descendants(elements.content).some(node => node.textContent === 'Select a Job'), true);
    assert.equal(descendants(elements.selection).filter(node => node.tagName === 'a').length, 2);
    assert.equal(descendants(elements.navigation).filter(node => node.tagName === 'a').length, 3);
    assert.equal(elements.detail.hidden, true);
    assert.deepEqual(requests, []);
    await click(descendants(elements.selection).find(node => node.tagName === 'a' && node.textContent === 'second.go'));
    assert.equal(new URLSearchParams(location.search).get('unit'), 'u2');
    assert.equal(descendants(elements.content).some(node => node.textContent === 'second.go'), true);
    assert.equal(requests.filter(url => url.includes('/job?')).length, 1);
    await openURL(location.href);
    assert.equal(descendants(elements.content).some(node => node.textContent === 'second.go'), true);

    requests.length = 0;
    await click(menu.find(a => a.dataset.view === 'session'));
    const params = new URLSearchParams(location.search);
    for (const key of ['unit', 'session', 'node', 'execution', 'context']) assert.equal(params.has(key), false);
    assert.equal(descendants(elements.content).some(node => node.textContent === 'Select a Session'), true);
    assert.equal(descendants(elements.selection).filter(node => node.tagName === 'a').length, 2);
    assert.deepEqual(requests, []);
    await click(descendants(elements.selection).find(node => node.tagName === 'a' && node.textContent === 'lane'));
    assert.equal(new URLSearchParams(location.search).get('session'), 's2');
    assert.equal(new URLSearchParams(location.search).get('tab'), 'overview');
    assert.equal(descendants(elements.content).some(node => node.textContent === 'Overview'), true);
    assert.equal(descendants(elements.content).some(node => node.textContent === 'Full Session'), true);
    assert.equal(requests.filter(url => url.includes('/session?')).length, 1);
    await click(menu.find(a => a.dataset.view === 'session'));
    assert.equal(new URLSearchParams(location.search).has('session'), false);
    assert.equal(descendants(elements.content).some(node => node.textContent === 'Select a Session'), true);
    await openURL('?view=session&session=s1&unit=u1&node=call-1&context=1');
  });

  await t.test('Session tabs separate the overview and full list without fetching measurements', async () => {
    await openURL('?view=session&session=s1');
    const text = () => descendants(elements.content).map(node => node.textContent);
    assert.ok(text().includes('Overview'));
    assert.ok(text().includes('Full Session'));
    assert.ok(text().includes('Observed call statistics'));
    assert.equal(text().includes('Input tokens'), false);
    assert.equal(text().includes('Measurements'), false);
    assert.equal(descendants(elements.content).some(node => node.className === 'call-list'), false);
    const full = descendants(elements.content).find(node => node.tagName === 'a' && node.textContent === 'Full Session');
    documentEvents.click({ target: full, button: 0, preventDefault() {} });
    await settle();
    assert.equal(new URLSearchParams(location.search).get('tab'), 'full');
    assert.ok(descendants(elements.content).some(node => node.className === 'call-list'));
    assert.equal(text().includes('Observed call statistics'), false);
    assert.equal(requests.some(url => url.includes('/measurements')), false);
    const overview = descendants(elements.content).find(node => node.tagName === 'a' && node.textContent === 'Overview');
    await openURL('?view=session&session=s1&execution=e1&node=call-1&context=1');
    const back = descendants(elements.content).find(node => node.tagName === 'a' && node.textContent === 'Overview');
    documentEvents.click({ target: back, button: 0, preventDefault() {} });
    await settle();
    const params = new URLSearchParams(location.search);
    assert.equal(params.get('tab'), 'overview');
    for (const key of ['node', 'execution', 'context']) assert.equal(params.has(key), false);
    assert.equal(elements.detail.hidden, true);
    await openURL(full.href);
    assert.ok(descendants(elements.content).some(node => node.className === 'call-list'));
    await openURL(overview.href);
    assert.ok(text().includes('Observed call statistics'));
    await openURL('?view=session&session=s1&unit=u1&node=call-1&context=1');
  });

  await t.test('Context keeps native identity even without a recorded request', async () => {
    assertIdentity();
    const request = call.node.request;
    delete call.node.request;
    await openURL(location.href);
    assertIdentity();
    call.node.request = request;
  });

  await t.test('Close remains closed on reload and retains the parent Session and Unit', async () => {
    const close = descendants(elements.detail).find(node => node.tagName === 'button' && node.textContent === 'Close');
    close.onclick();
    assert.equal(elements.detail.hidden, true);
    const params = new URLSearchParams(location.search);
    assert.equal(params.get('view'), 'session');
    assert.equal(params.get('session'), session.id);
    assert.equal(params.get('unit'), 'u1');
    assert.equal(params.has('context'), false);
    assert.equal(params.has('node'), false);
    await openURL(location.href);
    assert.equal(elements.detail.hidden, true);
  });

  await t.test('plain Session call links open native detail without forcing Context', async () => {
    await openURL('?view=session&session=s1&node=call-1');
    assert.equal(new URLSearchParams(location.search).has('context'), false);
    assertIdentity();
    assert.equal(descendants(elements.detail).some(node => node.textContent === 'CONTEXT VIEW'), false);
  });

  await t.test('clicking a model row and legacy Context links still opens Context', async () => {
    const row = descendants(elements.content).find(node => node.tagName === 'a' && node.textContent === item.name);
    let prevented = false;
    documentEvents.click({ target: row, button: 0, preventDefault() { prevented = true; } });
    await settle();
    assert.equal(prevented, true);
    assert.equal(new URLSearchParams(location.search).get('context'), '1');
    assert.equal(descendants(elements.detail).some(node => node.textContent === 'CONTEXT VIEW'), true);
    await openURL('?view=context&session=s1&node=call-1');
    assert.equal(new URLSearchParams(location.search).get('view'), 'session');
    assert.equal(descendants(elements.detail).some(node => node.textContent === 'CONTEXT VIEW'), true);
    assertIdentity();
  });
});
