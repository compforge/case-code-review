import test from 'node:test';
import assert from 'node:assert/strict';
import { callStatistics, renderMeasurements } from './measurements.mjs';

test('shared Lane calls count once with independent unknown duration and usage coverage', () => {
  const lane = { session: { id: 'lane' }, items: [
    { kind: 'model_call', usage: { input_tokens: 100, output_tokens: 10, cache_read_input_tokens: 50, cache_write_input_tokens: 5 }, duration_ms: 20 },
    { kind: 'model_call', duration_ms: 0 },
    { kind: 'tool_call', duration_ms: 8 },
    { kind: 'tool_call' },
  ] };
  const stats = callStatistics([lane, lane]);
  assert.equal(stats.models, 2); assert.equal(stats.tools, 2);
  assert.equal(stats.input, 105); assert.equal(stats.cacheRead, 50);
  assert.equal(stats.usageKnown, 1); assert.equal(stats.modelTimeKnown, 2);
  assert.equal(stats.toolTimeKnown, 1); assert.equal(stats.modelTime, 20);
  assert.equal(stats.toolTime, 8);
});

test('generic catalog rows render zero and unknown independently, and stale values stay hidden', () => {
  class Element {
    constructor(tag) { this.tag = tag; this.children = []; this.textContent = ''; }
    append(...nodes) { this.children.push(...nodes); }
    addEventListener() {}
  }
  globalThis.document = { createElement: tag => new Element(tag) };
  const text = node => [node.textContent, ...node.children.map(text)].join(' ');
  const values = [{ measurer_title: 'Custom', name: 'new_signal', value: 0, unit: 'count', aggregation: 'sum', status: 'measured', coverage: { measured: 1 } }, { name: 'unknown_signal', value: null, status: 'missing' }];
  const snapshot = { status: 'ready', views: { global: { values }, sessions: [{session: {id:'lane'}, values}], created_at: 'today', run_id: 'run' } };
  const global = new Element('div'); renderMeasurements(global, snapshot);
  assert.match(text(global), /new_signal/); assert.match(text(global), /0 count/);
  assert.match(text(global), /unknown_signal/); assert.match(text(global), /unknown/);
  const stale = new Element('div'); renderMeasurements(stale, {...snapshot, status:'stale'});
  assert.doesNotMatch(text(stale), /new_signal/); assert.match(text(stale), /out of date/);
});
