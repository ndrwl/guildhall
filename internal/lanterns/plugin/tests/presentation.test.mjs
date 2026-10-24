import {test} from 'node:test';
import assert from 'node:assert/strict';
import {urgency, smithy, raster} from '../lib/presentation.mjs';

test('only opening markers select urgency; unknown is neutral',()=>{
 for (const [text,want] of [['🔴 act','red'],['🟡 decide','yellow'],['🟢 done','green'],['unmarked','gray'],['later 🔴','gray'],['','gray']]) assert.equal(urgency(text),want);
});
test('N8 roof, chimney, glow and drifting fading smoke; bounded raster',()=>{
 const frames=Array.from({length:8},(_,i)=>smithy(30,i));
 assert.ok(frames[0].join('\n').includes('▁▟███▙▁'));
 assert.ok(frames.some(f=>f.join('').includes('▄')));
 for(const puff of ['○','◌','·']) assert.ok(frames.some(f=>f.join('').includes(puff)));
 assert.notDeepEqual(frames[0],frames[1]);
 assert.notDeepEqual(frames[0],frames[4]);
 for(const width of [1,3,7,30,512]) {
  const frame=smithy(width,3); assert.ok(frame.every(row=>Array.from(row).length===width));
  const packed=raster(frame,width,'dark');
  assert.equal(Buffer.from(packed.cells,'base64').length,width*frame.length*12);
 }
});
