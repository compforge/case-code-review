import test from 'node:test';
import assert from 'node:assert/strict';
import { compareMessages, duplicateMessages } from './context.mjs';
const msg = (text, role = 'user') => ({ role, content: [{ type: 'text', text }] });
test('adjacent request comparison accounts for repeated messages independently', () => {
  const previous = [msg('system', 'system'), msg('same'), msg('same'), msg('removed')];
  const current = [msg('system', 'system'), msg('same'), msg('replacement'), msg('tool', 'tool')];
  assert.deepEqual(compareMessages(previous, current), { retained: [{previous:0,current:0},{previous:1,current:1}], added:[2,3], removed:[2,3] });
});
test('reordered messages retain their original positions, role changes are not retained', () => {
  assert.deepEqual(compareMessages([msg('a'),msg('b')],[msg('b'),msg('a','assistant')]), {retained:[{previous:1,current:0}],added:[1],removed:[0]});
});
test('exact duplication includes tool schema and content-part differences', () => {
  const a = msg('same'); const b = {...a, tool_call_id:'tool-1'};
  assert.deepEqual([...duplicateMessages([a,b,a])], [[2,0]]);
});

test('receipt lookup finds R1 evidence and preserves ambiguous native matches', async () => {
  const { findToolSessions } = await import('./session.mjs');
  const sessions=[{id:'r1',role:'R1',units:['u']},{id:'r2',role:'R2 Lane',units:['u']},{id:'other',units:['other']}];
  const opened=[];
  const result=await findToolSessions(sessions,'u','receipt',async id=>{opened.push(id);return {items:id==='r1'?[{tool_call_id:'receipt'}]:[]};});
  assert.deepEqual(result,[{id:'r1',role:'R1',count:1}]);
  assert.deepEqual(opened,['r1','r2']);
  const ambiguous=await findToolSessions(sessions,'u','same',async()=>({items:[{tool_call_id:'same'}]}));
  assert.equal(ambiguous.length,2);
});
