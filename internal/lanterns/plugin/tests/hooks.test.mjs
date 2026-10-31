import {test} from 'node:test';
import assert from 'node:assert/strict';
import {register} from '../hooks/register.js';
import {assertS2} from './s2-contract.mjs';
import {layout, routineBox, rule} from '../lib/presentation.mjs';
import {twoAsks, nothing, grill} from './layout-fixtures.mjs';

const calmPath='/workspace/plugin/calm.json';

function engine(flag='1', preference='off\n', copy='/copy') {
 const handlers=[]; let writes=0, writtenPath, writtenValue; const calls=[];
 const on=(event,filter,handler)=>{if(typeof filter==='function'){handler=filter;filter={}}; handlers.push({event,filter,handler});return {catch(){}}};
 register(on);
 const element=kind=>props=>({kind,...props});
 // The session renders calm.json beside the plugin, naming the copy's preference; a null copy has none.
 const calmFile=JSON.stringify({preference:copy+'/director/lanterns-calm'});
 const $={env:{get:async key=>key==='CLAUDE_CODE_ENABLE_FUNCTION_HOOKS'?flag:undefined},plugin:{root:'/workspace/plugin'},
 fs:{exists:async path=>path===calmPath?copy!==null:preference!==null,read:async path=>path===calmPath?calmFile:preference,write:async(path,value)=>{writes++;writtenPath=path;writtenValue=value}},
 command:{register:async e=>calls.push(e)},clock:{every:(ms,fn)=>{calls.push({ms,fn});return {cancel(){calls.push('canceled')}}}},
 config:{list:async()=>[{key:'theme',value:'dark'}]},
 ui:{resolve:()=>({Box:element('Box'),Text:element('Text'),Raster:element('Raster')}),invalidate:()=>{},toast:text=>calls.push(text),blit:async()=>({deny:true})}};
 const run=async(event,filter={},extra={},next=async()=>({kind:'original'}))=>{
  const h=handlers.find(h=>h.event===event&&Object.entries(filter).every(([k,v])=>h.filter[k]===v));
  assert.ok(h,`${event} handler`);return h.handler($,{...extra},next);
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
test('without calm.json Calm has no persistence or presentation mode',async()=>{
 const e=engine('1','on\n',null);
 e.$.fs.exists=async path=>{if(path!==calmPath)throw Error('unexpected preference read');return false};
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
test('a layout reply draws each section with its own drawing, two rules and a bordered Routine box',async()=>{
 const e=engine();const drawn=[];
 const next=async event=>{drawn.push(event.props.text);assert.equal(event.requestId,'reply');return {kind:'drawn',text:event.props.text}};
 const frame=await e.run('ui.render',{component:'AssistantMessage'},{requestId:'reply',viewport:{columns:60},props:{text:twoAsks}},next);
 const parts=layout(twoAsks);
 assert.deepEqual(drawn,[parts.marker,parts.needs,parts.did]);
 const line={kind:'Text',color:'yellow',children:rule(56)};
 const draw=text=>({kind:'drawn',text});
 assert.deepEqual(frame,{kind:'Box',borderStyle:'round',borderColor:'yellow',flexDirection:'column',paddingX:1,children:[
  draw(parts.marker),draw(parts.needs),line,draw(parts.did),line,
  {kind:'Text',color:'yellow',children:routineBox(56,parts.routine,'5812')},
 ]});
});
test('a layout reply without What I did or a Routine box draws no rule for them',async()=>{
 const e=engine();const next=async event=>({kind:'drawn',text:event.props.text});
 const quiet=await e.run('ui.render',{component:'AssistantMessage'},{props:{text:nothing}},next);
 assert.equal(quiet.borderColor,'green');
 assert.deepEqual(quiet.children.map(c=>c.kind),['drawn','drawn','Text','Text']);
 assert.equal(quiet.children[3].children,routineBox(76,'d93 · merged · #228\n9 unchanged · /clerk:guild','5813'));
 // A grill round has no What I did label; it is drawn in that place, set off by the same rule.
 const round=await e.run('ui.render',{component:'AssistantMessage'},{props:{text:grill}},next);
 assert.deepEqual(round.children.map(c=>c.kind),['drawn','drawn','Text','drawn']);
});
test('a non-layout reply, or a section drawing that throws, gives the single frame',async()=>{
 const e=engine();
 const single={kind:'Box',borderStyle:'round',borderColor:'yellow',flexDirection:'column',paddingX:1,children:{kind:'original'}};
 assert.deepEqual(await e.run('ui.render',{component:'AssistantMessage'},{props:{text:'🟡 Guildmaster, d96 waits for you.'}}),single);
 const next=async event=>{if(event.props.text!==twoAsks)throw Error('no sections');return {kind:'original'}};
 assert.deepEqual(await e.run('ui.render',{component:'AssistantMessage'},{props:{text:twoAsks}},next),single);
});
test('without the flag a layout reply is drawn natively',async()=>{
 const e=engine('0');const drawn=[];
 const next=async event=>{drawn.push(event.props.text);return {kind:'original'}};
 assert.deepEqual(await e.run('ui.render',{component:'AssistantMessage'},{props:{text:twoAsks}},next),{kind:'original'});
 assert.deepEqual(drawn,[twoAsks]);
});
