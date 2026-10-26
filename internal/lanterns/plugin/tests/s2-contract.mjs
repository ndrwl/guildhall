import assert from 'node:assert/strict';

// Independent contract table: coordinates are relative to the eleven-cell origin.
export function assertS2(packed, width, tick, theme='dark') {
 const cells=Buffer.from(packed.cells,'base64');
 assert.equal(packed.rows,5); assert.equal(cells.length,width*5*12);
 const ambient=theme.startsWith('dark')?0x93a5ff:0x5769f7;
 const expected=Array.from({length:5},()=>Array.from({length:width},()=>[' ',ambient]));
 const x=Math.max(0,Math.floor((width-11)/2));
 const put=(r,c,text,color=ambient)=>{for(const ch of text){if(x+c<width)expected[r][x+c]=[ch,color];c++}};
 put(2,2,'│');put(3,0,'▁▟███▙▁');put(4,8,'┳━┳');
 put(4,3,tick%8<4?'▄':'▁',0xd77757);
 const smoke=[[1,2,'○'],[1,2,'○'],[0,3,'○'],[0,3,'○'],[0,4,'◌'],[0,4,'◌'],[0,5,'·'],[0,5,'·']][tick%8];
 put(...smoke);
 const spark={8:[3,9],9:[2,9],10:[1,10]}[tick%16];
 if(spark)put(...spark,'·',0xd77757);
 for(let r=0;r<5;r++)for(let c=0;c<width;c++) {
  const offset=(r*width+c)*12;
  assert.equal(cells.readUInt32LE(offset),expected[r][c][0].codePointAt(0),`glyph tick=${tick} (${r},${c})`);
  assert.equal(cells.readUInt32LE(offset+4),expected[r][c][1],`color tick=${tick} (${r},${c})`);
  assert.equal(cells.readUInt32LE(offset+8),0x01000000);
 }
}
