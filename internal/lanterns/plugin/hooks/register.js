import {urgency,layout,linkTickets,routineBox,rule,smithy,raster} from '../lib/presentation.mjs';

// Engine operations are kept here. Geometry and urgency classification are pure.
// Each module lifetime owns its state; the preference survives Director sessions.
let activated,loading,preference,links,calm=false,ticker,tick=0,theme='light';
  const sites=new Map();
  const key='guildhall-lanterns-smithy';
  const active=$=>activated??= $.env.get('CLAUDE_CODE_ENABLE_FUNCTION_HOOKS').then(v=>v==='1',()=>false);
  function timer($) {
    if(!calm||ticker)return;
    ticker=$.clock.every(220,async()=>{
      if(!calm||sites.size===0)return;
      tick++;
      for(const [requestId,columns] of sites) {
        try {
          const packed=raster(smithy(columns,tick),columns,theme);
          const result=await $.ui.blit({requestId,key,columns,...packed});
          if(result.deny!==undefined)sites.delete(requestId);
        } catch {sites.delete(requestId)}
      }
    });
  }
  async function load($) {
    if(!loading)loading=(async()=>{
      // The session's calm.json, rendered beside the plugin, names the copy's preference; without it Calm is unavailable.
      try {
        const file=$.plugin.root+'/calm.json';
        const saved=await $.fs.exists(file)&&JSON.parse(await $.fs.read(file)).preference;
        preference=typeof saved==='string'&&saved?saved:undefined;
      }catch {preference=undefined}
      // The session's links.json, rendered beside the plugin when the copy serves task pages, says where ticket tokens link;
      // without it tokens are drawn as they are.
      try {
        const file=$.plugin.root+'/links.json';
        const saved=await $.fs.exists(file)&&JSON.parse(await $.fs.read(file));
        links=saved&&typeof saved.url==='string'&&Array.isArray(saved.repos)?{url:saved.url,repos:saved.repos}:undefined;
      }catch {links=undefined}
      try {calm=!!preference&&(!await $.fs.exists(preference)||(await $.fs.read(preference)).trim()==='on')}catch {calm=false}
      try {theme=(await $.config.list()).find(r=>r.key==='theme')?.value??'light'}catch {}
      timer($);
    })();
    await loading;
  }
export function register(on) {
  activated=undefined;loading=undefined;preference=undefined;links=undefined;calm=false;ticker=undefined;tick=0;theme='light';sites.clear();
  on('session.start',async($,e,next)=>{
    if(!await active($))return next(e);
    await load($);
    if(preference)await $.command.register({name:'calm',description:'Toggle Lanterns Calm tool presentation and smithy chimney.'});
    return next(e);
  });
  on('command.run',{command:'calm'},async($,e,next)=>{
    if(!await active($))return next(e);
    await load($);
    if(!preference)return next(e);
    try {await $.fs.write(preference,calm?'off\n':'on\n')}catch {
      $.ui.toast('Calm unchanged: could not save the Director preference.');return {};
    }
    calm=!calm;
    if(!calm){sites.clear();ticker?.cancel();ticker=undefined}else timer($);
    $.ui.invalidate('ui.render');$.ui.toast(calm?'Calm on':'Calm off');return {};
  });
  on('config.set',{key:'theme'},async($,e,next)=>{
    if(!await active($))return next(e);
    const result=await next(e);
    if(result.deny===undefined){theme=result.value;$.ui.invalidate('ui.render')}
    return result;
  });
  on('ui.render',{component:'AssistantMessage'},async($,e,next)=>{
    if(!await active($)||!e.props?.text)return next(e);
    await load($);
    const color=urgency(e.props.text);
    const frame=children=>({borderStyle:'round',borderColor:color,flexDirection:'column',paddingX:1,children});
    // Ticket tokens are drawn as task-page links; the transcript and the model's context keep the token.
    const linked=text=>{try {return linkTickets(text,links)}catch {return text}};
    // A reply in the Director's layout is drawn section by section, each with Claude's own drawing;
    // anything else, and any failure, keeps the single frame around the original drawing.
    try {
      // The layout is split from the original text; each section's tokens are linked as it is drawn.
      const parts=layout(e.props.text);
      if(parts) {
        const {Box,Text}=$.ui.resolve(e);
        const draw=text=>next({...e,props:{...e.props,text:linked(text)}});
        // The frame's border and padding take four columns.
        // The engine's Box takes only full borders, so the rules and the Routine box are drawn as text.
        const width=Math.max(1,Math.min(512,Math.floor(e.viewport?.columns??80)-4));
        const line=()=>Text({color,children:rule(width)});
        const sections=[await draw(parts.marker),await draw(parts.needs)];
        if(parts.did)sections.push(line(),await draw(parts.did));
        if(parts.routine!==undefined)sections.push(line(),Text({color,children:routineBox(width,parts.routine,parts.asOf)}));
        return Box(frame(sections));
      }
    }catch {}
    const text=linked(e.props.text);
    const original=await next(text===e.props.text?e:{...e,props:{...e.props,text}});
    try {
      const {Box}=$.ui.resolve(e);
      return Box(frame(original));
    }catch {return original}
  });
  for(const component of ['ToolUse','ToolResult','ToolGroup'])on('ui.render',{component},async($,e,next)=>{
    if(!await active($))return next(e);
    await load($);
    if(!calm)return next(e);
    const {Box}=$.ui.resolve(e);return Box({display:'none'});
  });
  on('ui.render',{component:'Spinner'},async($,e,next)=>{
    if(!await active($))return next(e);
    await load($);
    if(!calm||e.surface!=='terminal'){sites.delete(e.requestId);return next(e)}
    const columns=Math.max(1,Math.min(512,Math.floor(e.viewport?.columns??80)-2));
    try {
      const {Box,Raster}=$.ui.resolve(e);
      const packed=raster(smithy(columns,tick),columns,theme);
      sites.set(e.requestId,columns);
      return Box({flexDirection:'column',children:Raster({key,columns,...packed})});
    }catch {sites.delete(e.requestId);return next(e)}
  });
}
