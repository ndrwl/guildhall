import {test} from 'node:test';
import assert from 'node:assert/strict';
import {urgency, smithy, raster} from '../lib/presentation.mjs';
import {assertS2} from './s2-contract.mjs';

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
