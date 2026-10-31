// Pure geometry and encoding: no engine, filesystem or process dependencies.
export function urgency(text) {
  return ({'🔴':'red','🟡':'yellow','🟢':'green'})[Array.from(String(text ?? '').trimStart()).slice(0,1).join('')] ?? 'gray';
}

// layout splits a Director reply in the fixed layout into its sections:
// the marker line, Needs you, the middle section (What I did, or a grill round in its place) and the Routine box.
// It returns undefined for any text that is not exactly the layout, so the caller keeps today's single frame.
export function layout(text) {
  const lines=String(text??'').replace(/\r\n/g,'\n').trim().split('\n');
  const blank=i=>i<lines.length&&lines[i].trim()==='';
  if(urgency(lines[0])==='gray'||!blank(1))return undefined;
  const marker=lines[0];
  let i=1;while(blank(i))i++;
  let end=i;while(end<lines.length&&!blank(end))end++;
  const needsLines=lines.slice(i,end);
  const items=needsLines.slice(1);
  const listed=needsLines[0]==='**Needs you**'&&items.length>0&&(items.every(l=>/^- \S/.test(l))||(items.length===1&&items[0]==='Nothing needs you.'));
  if(!listed&&!(needsLines.length===1&&needsLines[0]==='Nothing needs you.'))return undefined;
  const needs=needsLines.join('\n');
  const rest=lines.slice(end);
  const fences=rest.flatMap((l,n)=>l.trimStart().startsWith('```')?[n]:[]);
  let middle=rest,routine,asOf;
  if(fences.length>0) {
    const [open,close]=fences;
    if(fences.length!==2||close!==rest.length-1||rest[open]!=='```text'||rest[close]!=='```')return undefined;
    const body=rest.slice(open+1,close);
    const seq=/^as of #(\d+)$/.exec(body.at(-1)??'');
    if(!seq||!/^\d+ unchanged · \/clerk:guild$/.test(body.at(-2)??''))return undefined;
    routine=body.slice(0,-1).join('\n');asOf=seq[1];middle=rest.slice(0,open);
  }
  const did=middle.join('\n').trim()||undefined;
  return {marker,needs,did,routine,asOf};
}

// routineEdge is the Routine box's top border, naming the box and the feed sequence it was read at.
export function routineEdge(width,asOf) {
  const left='┌─ routine ',right=` as of #${asOf} ─┐`;
  const fill=Math.max(1,width-Array.from(left+right).length);
  return left+'─'.repeat(fill)+right;
}

// routineBox draws the whole Routine box as text lines, because the engine's Box accepts only full borders:
// the top edge, each body line between single side borders and wrapped to fit, and the bottom edge.
export function routineBox(width,routine,asOf) {
  const top=routineEdge(width,asOf);
  const outer=Array.from(top).length,inner=outer-4;
  const body=String(routine??'').split('\n').flatMap(line=>{
    const glyphs=Array.from(line),rows=[];
    for(let i=0;i<Math.max(1,glyphs.length);i+=inner)rows.push(glyphs.slice(i,i+inner));
    return rows.map(row=>'│ '+row.join('')+' '.repeat(inner-row.length)+' │');
  });
  return [top,...body,'└'+'─'.repeat(outer-2)+'┘'].join('\n');
}

// rule is the thin line that sets a section off, filled to the width.
export function rule(width) {
  return '─'.repeat(Math.max(1,width));
}

export function smithy(columns, tick) {
  const width=Math.max(1,Math.min(512,Math.floor(columns)));
  const rows=Array.from({length:5},()=>Array(width).fill(' '));
  const x=Math.max(0,Math.floor((width-11)/2));
  const put=(r,c,text)=>{for(const ch of text){if(c>=0&&c<width)rows[r][c]=ch;c++}};
  put(2,x+2,'│'); put(3,x,'▁▟███▙▁');
  put(4,x+3,Math.floor(tick/4)%2===0?'▄':'▁');
  const phase=Math.floor(tick/2)%4;
  put(phase===0?1:0,x+2+phase,['○','○','◌','·'][phase]);
  put(4,x+8,'┳━┳');
  const spark=tick%16;
  if(spark===8)put(3,x+9,'·');
  if(spark===9)put(2,x+9,'·');
  if(spark===10)put(1,x+10,'·');
  return rows.map(row=>row.join(''));
}

function base64(bytes) {
  const alphabet='ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/';
  let out='';
  for(let i=0;i<bytes.length;i+=3) {
    const word=(bytes[i]<<16)|((bytes[i+1]??0)<<8)|(bytes[i+2]??0);
    out+=alphabet[(word>>>18)&63]+alphabet[(word>>>12)&63]+(i+1<bytes.length?alphabet[(word>>>6)&63]:'=')+(i+2<bytes.length?alphabet[word&63]:'=');
  }
  return out;
}

export function raster(frame,columns,theme) {
  const bytes=new Uint8Array(columns*frame.length*12);
  const view=new DataView(bytes.buffer);
  const ambient=typeof theme==='string'&&theme.startsWith('dark')?0x93a5ff:0x5769f7;
  for(let row=0;row<frame.length;row++) {
    const glyphs=Array.from(frame[row]);
    for(let col=0;col<columns;col++) {
      const ch=glyphs[col]??' ';const offset=(row*columns+col)*12;
      view.setUint32(offset,ch.codePointAt(0),true);
      const ember=(row===4&&(ch==='▄'||ch==='▁'))||(row>0&&ch==='·');
      view.setUint32(offset+4,ember?0xd77757:ambient,true);
      view.setUint32(offset+8,0x01000000,true);
    }
  }
  return {rows:frame.length,cells:base64(bytes)};
}
