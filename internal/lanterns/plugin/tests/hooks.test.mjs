import {test} from 'node:test';
import assert from 'node:assert/strict';
import {register} from '../hooks/register.js';

function engine(flag='1', preference='off\n') {
 const handlers=[]; let writes=0, writtenPath; const calls=[];
 const on=(event,filter,handler)=>{if(typeof filter==='function'){handler=filter;filter={}}; handlers.push({event,filter,handler});return {catch(){}}};
 register(on);
 const element=kind=>props=>({kind,...props});
 const $={env:{get:async key=>key==='CLAUDE_CODE_ENABLE_FUNCTION_HOOKS'?flag:key==='GUILDHALL_COPY'?'/copy':undefined},plugin:{root:'/workspace/plugin'},
 fs:{exists:async()=>true,read:async()=>preference,write:async path=>{writes++;writtenPath=path}},
 command:{register:async e=>calls.push(e)},clock:{every:(ms,fn)=>{calls.push({ms,fn});return {cancel(){}}}},
 config:{list:async()=>[{key:'theme',value:'dark'}]},
 ui:{resolve:()=>({Box:element('Box'),Text:element('Text'),Raster:element('Raster')}),invalidate:()=>{},toast:text=>calls.push(text),blit:async()=>({deny:true})}};
 const run=async(event,filter={},extra={})=>{
  const h=handlers.find(h=>h.event===event&&Object.entries(filter).every(([k,v])=>h.filter[k]===v));
  assert.ok(h,`${event} handler`);return h.handler($,{...extra},async()=>({kind:'original'}));
 };
 return {$,run,calls,get writes(){return writes},get writtenPath(){return writtenPath}};
}
test('without exact flag all function hooks are complete no-ops',async()=>{
 const e=engine('0','on\n'); e.$.fs.exists=()=>{throw Error('unexpected read')};
 await e.run('session.start');
 assert.deepEqual(await e.run('ui.render',{component:'ToolUse'}),{kind:'original'});
 assert.deepEqual(await e.run('ui.render',{component:'Spinner'}),{kind:'original'});
 assert.deepEqual(await e.run('command.run',{command:'calm'}),{kind:'original'});
 assert.deepEqual(e.calls,[]);assert.equal(e.writes,0);
});
test('off by default; persisted toggle, tool hiding, original frame contents and restoration',async()=>{
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
test('saved preference loads before restored rows; failed save preserves current choice',async()=>{
 const e=engine('1','on\n');
 assert.equal((await e.run('ui.render',{component:'ToolResult'})).display,'none');
 e.$.fs.write=async()=>{throw Error('read-only')};
 await e.run('command.run',{command:'calm'});
 assert.equal((await e.run('ui.render',{component:'ToolResult'})).display,'none');
 assert.ok(e.calls.some(c=>typeof c==='string'&&c.includes('unchanged')));
});
