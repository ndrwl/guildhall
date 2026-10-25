// Pure geometry and encoding: no engine, filesystem or process dependencies.
export function urgency(text) {
  return ({'🔴':'red','🟡':'yellow','🟢':'green'})[Array.from(String(text ?? '').trimStart()).slice(0,1).join('')] ?? 'gray';
}

export function smithy(columns, tick) {
  const width=Math.max(1,Math.min(512,Math.floor(columns)));
  const rows=Array.from({length:5},()=>Array(width).fill(' '));
  const x=Math.max(0,Math.floor((width-7)/2));
  const put=(r,c,text)=>{for(const ch of text){if(c>=0&&c<width)rows[r][c]=ch;c++}};
  put(2,x+2,'│'); put(3,x,'▁▟███▙▁');
  put(4,x+3,Math.floor(tick/4)%2===0?'▄':'▁');
  const phase=tick%4;
  put(phase===0?1:0,x+2+phase,['○','○','◌','·'][phase]);
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
      view.setUint32(offset+4,row===4?0xd77757:ambient,true);
      view.setUint32(offset+8,0x01000000,true);
    }
  }
  return {rows:frame.length,cells:base64(bytes)};
}
