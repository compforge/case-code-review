import test from 'node:test';
import assert from 'node:assert/strict';
import { reviewOverview } from './global.mjs';
import { workspaceParams, workspaceURL } from './navigation.mjs';

/**
 * @case Global counts shared Sessions once and does not call unassessed Jobs completed
 * @see {@link component://docs/observability.md}
 */
test('overview preserves Unit outcomes and shared Session scope', () => {
  const result = reviewOverview({
    Jobs: [{id:'u1',outcome:'completed'}, {id:'u2',outcome:'timeout'}, {id:'u3'}, {id:'u4',outcome:'completed',unassessed_hypotheses:['h']}],
    Sessions: [{id:'lane',units:['u1','u2','u3','u4'],executions:[{id:'e1'},{id:'e2'}]}],
    JobCounts: {u1:{Hypotheses:2,Findings:1},u2:{Hypotheses:3,Findings:0}},
  });
  assert.equal(result.completed, 1);
  assert.equal(result.sessions.length, 1);
  assert.equal(result.executions, 2);
  assert.equal(result.hypotheses, 5);
  assert.equal(result.outcomes.get('unknown'), 1);
  assert.equal(result.jobs.length, 4);
});

test('empty review has no invented Jobs or Sessions', () => {
  const result = reviewOverview({});
  assert.equal(result.jobs.length, 0);
  assert.equal(result.sessions.length, 0);
  assert.equal(result.completed, 0);
  assert.equal(result.hypotheses, 0);
});

/**
 * @case Context is deep-linked to a specific model call and stays inside its Job or Session
 * @see {@link component://docs/observability.md}
 */
test('model context preserves Job scope and survives URL reload', () => {
  const params = workspaceParams('?view=job&unit=u&hypothesis=h');
  const node = 'call:["run","model_call","model",2]';
  const url = workspaceURL('/r/repo/review', params, {session:'lane',node,context:'1'});
  const restored = workspaceParams(new URL(url, 'http://localhost').search);
  assert.equal(restored.get('view'), 'job');
  assert.equal(restored.get('unit'), 'u');
  assert.equal(restored.get('hypothesis'), 'h');
  assert.equal(restored.get('node'), node);
  assert.equal(restored.get('context'), '1');
  const next = new URL(workspaceURL('/r/repo/review', restored, {view:'session',session:'other',node:null}), 'http://localhost');
  assert.equal(next.searchParams.has('context'), false);
  assert.equal(next.searchParams.has('node'), false);
});

test('Global is the default and existing Context links retain call identity', () => {
  assert.equal(workspaceParams('').get('view'), 'global');
  assert.equal(workspaceParams('?view=missing').get('view'), 'global');
  const params = workspaceParams('?view=context&session=lane&node=model-2');
  assert.equal(params.get('view'), 'session');
  assert.equal(params.get('context'), '1');
  assert.equal(params.get('node'), 'model-2');
  const global = new URL(workspaceURL('/r/repo/review', params, {view:'global'}), 'http://localhost');
  assert.equal(global.searchParams.has('context'), false);
});
