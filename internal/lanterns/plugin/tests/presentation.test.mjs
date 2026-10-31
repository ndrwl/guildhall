import {test} from 'node:test';
import assert from 'node:assert/strict';
import {urgency, smithy, raster, layout, routineEdge, routineBox, rule} from '../lib/presentation.mjs';
import {assertS2} from './s2-contract.mjs';
import {twoAsks, nothing, grill} from './layout-fixtures.mjs';

test('only opening markers select urgency; unknown is neutral',()=>{
 for (const [text,want] of [['🔴 act','red'],['🟡 decide','yellow'],['🟢 done','green'],['unmarked','gray'],['later 🔴','gray'],['','gray']]) assert.equal(urgency(text),want);
});
test('S2 exact loop, per-cell colors and bounded narrow fallback',()=>{
 for(const width of [1,3,7,10,11,18,30,58,512])for(let tick=0;tick<16;tick++) {
  const frame=smithy(width,tick);
  assert.equal(frame.length,5);
  assert.ok(frame.every(row=>Array.from(row).length===width));
  for(const theme of ['dark','dark-dimmed','light'])assertS2(raster(frame,width,theme),width,tick,theme);
  assert.deepEqual(frame,smithy(width,tick+16));
 }
});
test('S2 holds smoke for two ticks and retains the hearth beat',()=>{
 const frames=Array.from({length:8},(_,i)=>smithy(30,i));
 assert.ok(frames[0].join('\n').includes('▁▟███▙▁'));
 assert.ok(frames.some(f=>f.join('').includes('▄')));
 for(const puff of ['○','◌','·']) assert.ok(frames.some(f=>f.join('').includes(puff)));
 assert.deepEqual(frames[0],frames[1]);
 assert.notDeepEqual(frames[1],frames[2]);
 assert.notDeepEqual(frames[0],frames[4]);
 for(const width of [1,3,7,30,512]) {
  const frame=smithy(width,3); assert.ok(frame.every(row=>Array.from(row).length===width));
  const packed=raster(frame,width,'dark');
  assert.equal(Buffer.from(packed.cells,'base64').length,width*frame.length*12);
 }
});
test('the reply layout splits into marker line, Needs you, What I did and the Routine box',()=>{
 assert.deepEqual(layout(twoAsks),{
  marker:'🟡 Guildmaster, two things need you.',
  needs:'**Needs you**\n- d96 · approve the plan · accept-1\n- d104 · grill round 1 · grill-1',
  did:'**What I did**\nRaised d95 to p0 as you asked; it is second in the merge queue now.',
  routine:'d93 · merged · #228\nd95 · merge queue 2/3 · #229\nd97 · build running · no PR\nd98 · review · #231\nd99 · validate · #232\n7 unchanged · /clerk:guild',
  asOf:'5812',
 });
 assert.deepEqual(layout(nothing),{marker:'🟢 Guildmaster, nothing needs you.',needs:'Nothing needs you.',did:undefined,routine:'d93 · merged · #228\n9 unchanged · /clerk:guild',asOf:'5813'});
 // A grill round sits in What I did's place, with no Routine box.
 assert.deepEqual(layout(grill),{
  marker:'🟡 Guildmaster, one thing needs you.',needs:'**Needs you**\n- d104 · grill round 1 · grill-1',
  did:'Round 1 for d104, three questions.\n\n1. Should the export include hidden columns?\n   I recommend no.',routine:undefined,asOf:undefined,
 });
 assert.deepEqual(layout('\n'+nothing+'\n'),layout(nothing));
});
test('text that is not the layout, or is in doubt, has no sections',()=>{
 const cases={
  'plain reply':'🟡 Guildmaster, d96 waits for your approval.',
  'unmarked':twoAsks.replace('🟡 ',''),
  'heading before the marker':'# Status\n\n'+twoAsks,
  'two-line marker paragraph':twoAsks.replace('need you.','need you.\nAnd more.'),
  'missing Needs you label':twoAsks.replace('**Needs you**\n',''),
  'empty Needs you':nothing.replace('Nothing needs you.','**Needs you**'),
  'Needs you line that is not a list item':twoAsks.replace('- d104','d104'),
  'unclosed fence':twoAsks.replace(/\n```$/,''),
  'fence that is not last':twoAsks+'\n\nOne more thing.',
  'second fence':twoAsks.replace('**What I did**','```sh\nguildhall task list\n```\n\n**What I did**'),
  'not a text fence':twoAsks.replace('```text','```'),
  'Routine box without its as of line':twoAsks.replace('\nas of #5812',''),
  'Routine box without its unchanged line':twoAsks.replace('\n7 unchanged · /clerk:guild',''),
 };
 for(const [name,text] of Object.entries(cases))assert.equal(layout(text),undefined,name);
 for(const text of ['',undefined,null])assert.equal(layout(text),undefined);
});
test('the Routine box top edge carries routine and its as of, filled to the width',()=>{
 const edge=routineEdge(40,'5812');
 assert.equal(Array.from(edge).length,40);
 assert.ok(edge.startsWith('┌─ routine ─'));assert.ok(edge.endsWith('─ as of #5812 ─┐'));
 assert.equal(routineEdge(3,'5812'),'┌─ routine ─ as of #5812 ─┐');
});
test('the Routine box is drawn whole in text, every line the width, long lines wrapped inside the sides',()=>{
 assert.equal(routineBox(30,'d93 · merged · #228\n9 unchanged · /clerk:guild','5813'),[
  '┌─ routine ──── as of #5813 ─┐',
  '│ d93 · merged · #228        │',
  '│ 9 unchanged · /clerk:guild │',
  '└────────────────────────────┘',
 ].join('\n'));
 const box=routineBox(28,'d95 · merge queue 2/3 · #229','5812').split('\n');
 for(const row of box)assert.equal(Array.from(row).length,28,row);
 assert.deepEqual(box.slice(1,-1),['│ d95 · merge queue 2/3 ·  │','│ #229                     │']);
 assert.equal(rule(5),'─────');assert.equal(rule(0),'─');
});
