// Pure geometry and encoding: no engine, filesystem or process dependencies.
export function urgency(text) {
  return ({'🔴':'red','🟡':'yellow','🟢':'green'})[Array.from(String(text ?? '').trimStart()).slice(0,1).join('')] ?? 'gray';
}

// layout splits a Director reply in the fixed layout into its sections:
// the marker line, Needs you, the middle section (What I did, or, with no task pages served, a grill round in its
// place) and the Routine box. A grill round on the task page is only a Needs you line with its link.
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

// An id segment of a ticket token: letters, digits, '.', '_' and '-', starting and ending with a letter or digit,
// so a sentence's closing period is not part of it.
const segment='[A-Za-z0-9](?:[A-Za-z0-9._-]*[A-Za-z0-9])?';
// A code span (left as is) or a ticket token, `ticket:<repo>/<id>[#<record>]` or `ticket:<id>[#<record>]`, at a word
// boundary and not already the text of a Markdown link.
const ticketOrCode=new RegExp('(`+).*?\\1|(?<![\\w./:#-])ticket:(?:('+segment+')/)?('+segment+')(?:#('+segment+'))?(?![\\w/#]|\\]\\()','g');

// linkTickets rewrites each ticket token in text to a Markdown link to its task page, `[ticket:…](<url>/<repo>/<id>#<record>)`,
// so the drawn text stays the short token. links is the session's links.json, {url, repos}. A bare id resolves only
// when links names exactly one repository; a token it cannot resolve, and any token in a code span or fenced block,
// is left as it is. Without links the text is unchanged.
export function linkTickets(text,links) {
  const url=typeof links?.url==='string'?links.url.replace(/\/+$/,''):'';
  const repos=Array.isArray(links?.repos)?links.repos.filter(r=>typeof r==='string'):[];
  if(typeof text!=='string'||!url||repos.length===0)return text;
  let fence;
  return text.split('\n').map(line=>{
    const open=/^\s*(`{3,}|~{3,})/.exec(line);
    if(fence) {
      if(open&&open[1][0]===fence[0]&&open[1].length>=fence.length&&line.trim()===open[1])fence=undefined;
      return line;
    }
    if(open){fence=open[1];return line}
    return line.replace(ticketOrCode,(token,code,repo,id,record)=>{
      if(code)return token;
      const r=repo??(repos.length===1?repos[0]:undefined);
      if(!r||!repos.includes(r))return token;
      return `[${token}](${url}/${r}/${id}${record?'#'+record:''})`;
    });
  }).join('\n');
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
