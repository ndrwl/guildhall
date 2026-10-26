import {test} from 'node:test';
import assert from 'node:assert/strict';
import {register} from '../hooks/register.js';
import {assertS2} from './s2-contract.mjs';

function engine(flag='1', preference='off\n', copy='/copy') {
 const handlers=[]; let writes=0, writtenPath, writtenValue; const calls=[];
 const on=(event,filter,handler)=>{if(typeof filter==='function'){handler=filter;filter={}}; handlers.push({event,filter,handler});return {catch(){}}};
 register(on);
 const element=kind=>props=>({kind,...props});
 const $={env:{get:async key=>key==='CLAUDE_CODE_ENABLE_FUNCTION_HOOKS'?flag:key==='GUILDHALL_COPY'?copy:undefined},plugin:{root:'/workspace/plugin'},
 fs:{exists:async()=>preference!==null,read:async()=>preference,write:async(path,value)=>{writes++;writtenPath=path;writtenValue=value}},
 command:{register:async e=>calls.push(e)},clock:{every:(ms,fn)=>{calls.push({ms,fn});return {cancel(){calls.push('canceled')}}}},
 config:{list:async()=>[{key:'theme',value:'dark'}]},
 ui:{resolve:()=>({Box:element('Box'),Text:element('Text'),Raster:element('Raster')}),invalidate:()=>{},toast:text=>calls.push(text),blit:async()=>({deny:true})}};
 const run=async(event,filter={},extra={})=>{
  const h=handlers.find(h=>h.event===event&&Object.entries(filter).every(([k,v])=>h.filter[k]===v));
  assert.ok(h,`${event} handler`);return h.handler($,{...extra},async()=>({kind:'original'}));
 };
 return {$,run,calls,get writes(){return writes},get writtenPath(){return writtenPath},get writtenValue(){return writtenValue}};
}
test('without exact flag all function hooks are complete no-ops',async()=>{
 const e=engine('0','on\n'); e.$.fs.exists=()=>{throw Error('unexpected read')};
 await e.run('session.start');
 assert.deepEqual(await e.run('ui.render',{component:'ToolUse'}),{kind:'original'});
 assert.deepEqual(await e.run('ui.render',{component:'Spinner'}),{kind:'original'});
 assert.deepEqual(await e.run('command.run',{command:'calm'}),{kind:'original'});
 assert.deepEqual(e.calls,[]);assert.equal(e.writes,0);
});
test('persisted off; toggle, tool hiding, original frame contents and restoration',async()=>{
 const e=engine(); await e.run('session.start');
 assert.ok(e.calls.some(c=>c.name==='calm'));
 assert.deepEqual(await e.run('ui.render',{component:'ToolUse'}),{kind:'original'});
 await e.run('command.run',{command:'calm'}); assert.equal(e.writes,1);
 assert.equal(e.writtenPath,'/copy/director/lanterns-calm');
 assert.equal((await e.run('ui.render',{component:'ToolUse'})).display,'none');
 const frame=await e.run('ui.render',{component:'AssistantMessage'},{props:{text:'unmarked'}});
 assert.equal(frame.borderColor,'gray');assert.equal(frame.children.kind,'original');
 const spinner=await e.run('ui.render',{component:'Spinner'},{surface:'terminal',requestId:'a',viewport:{columns:30}});
 assert.equal(spinner.children.kind,'Raster');
 assert.ok(e.calls.some(c=>c.ms===220));
 await e.run('command.run',{command:'calm'});
 assert.deepEqual(await e.run('ui.render',{component:'Spinner'}),{kind:'original'});
});
test('without a Director copy Calm has no persistence or presentation mode',async()=>{
 const e=engine('1','on\n',null);
 e.$.fs.exists=()=>{throw Error('unexpected preference read')};
 await e.run('session.start');
 assert.deepEqual(await e.run('command.run',{command:'calm'}),{kind:'original'});
 for(const component of ['ToolUse','ToolResult','ToolGroup','Spinner'])assert.deepEqual(await e.run('ui.render',{component}),{kind:'original'});
 assert.deepEqual(e.calls,[]);assert.equal(e.writes,0);
});
test('absent preference defaults on; off persists across a fresh session',async()=>{
 const e=engine('1',null); await e.run('session.start');
 assert.equal(e.writes,0);
 for(const component of ['ToolUse','ToolResult','ToolGroup'])assert.equal((await e.run('ui.render',{component})).display,'none');
 await e.run('command.run',{command:'calm'});
 assert.equal(e.writtenValue,'off\n');
 assert.deepEqual(await e.run('ui.render',{component:'ToolUse'}),{kind:'original'});
 const restored=engine('1',e.writtenValue);await restored.run('session.start');
 assert.deepEqual(await restored.run('ui.render',{component:'Spinner'},{surface:'terminal'}),{kind:'original'});
 assert.ok(!restored.calls.some(c=>c.ms));
 await restored.run('command.run',{command:'calm'});assert.equal(restored.writtenValue,'on\n');
});
test('fake clock paints one exact S2 loop at 220 ms; clips, unmounts and restores',async()=>{
 for(const viewport of [1,3,9,13,20,60,514,900,undefined]) {
  const e=engine('1','on\n');await e.run('session.start');
  const width=Math.max(1,Math.min(512,(viewport??80)-2));
  const frame=await e.run('ui.render',{component:'Spinner'},{surface:'terminal',requestId:'sprite',viewport:{columns:viewport}});
  assert.equal(frame.children.columns,width);assertS2(frame.children,width,0);
  const clocks=e.calls.filter(c=>c.ms);assert.equal(clocks.length,1);assert.equal(clocks[0].ms,220);
  let blits=0;
  e.$.ui.blit=async packed=>{blits++;assert.equal(packed.requestId,'sprite');assert.equal(packed.columns,width);assertS2(packed,width,blits);return {}};
  for(let tick=1;tick<=16;tick++)await clocks[0].fn();
  assert.equal(blits,16);
  e.$.ui.blit=async()=>{blits++;return {deny:true}};
  await clocks[0].fn();await clocks[0].fn();assert.equal(blits,17);
  await e.run('ui.render',{component:'Spinner'},{surface:'terminal',requestId:'sprite',viewport:{columns:viewport}});
  e.$.ui.blit=async()=>{blits++;throw Error('unmounted')};
  await clocks[0].fn();await clocks[0].fn();assert.equal(blits,18);
  await e.run('ui.render',{component:'Spinner'},{surface:'terminal',requestId:'sprite'});
  await e.run('command.run',{command:'calm'});
  await clocks[0].fn();assert.equal(blits,18);assert.ok(e.calls.includes('canceled'));
  assert.deepEqual(await e.run('ui.render',{component:'Spinner'},{surface:'terminal'}),{kind:'original'});
  assert.deepEqual(await e.run('ui.render',{component:'ToolUse'}),{kind:'original'});
 }
});
test('saved preference loads before restored rows; failed save preserves current choice',async()=>{
 const e=engine('1','on\n');
 assert.equal((await e.run('ui.render',{component:'ToolResult'})).display,'none');
 e.$.fs.write=async()=>{throw Error('read-only')};
 await e.run('command.run',{command:'calm'});
 assert.equal((await e.run('ui.render',{component:'ToolResult'})).display,'none');
 assert.ok(e.calls.some(c=>typeof c==='string'&&c.includes('unchanged')));
});
