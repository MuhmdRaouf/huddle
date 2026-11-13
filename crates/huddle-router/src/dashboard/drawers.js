/* ---------- drawers ---------- */
let DR=null;
const closeBtn=()=>`<button class="x" data-close aria-label="Close">${ic('x')}</button>`;
const DRAWERS={
 session(id){const s=SESS.find(x=>x.id===id);if(!s)return '';const rs=sReq(s),cl=cacheLeft(s),c=s.ctx==null?null:Math.round(s.ctx);const t0=NOW-15*MIN;
  const lane=a=>aReq(a).filter(r=>r.t>=t0);
  const unmatched=rs.filter(r=>!r.a&&r.t>=t0);
  const bar=r=>{const x=Math.max(0,(r.t-t0)/(15*MIN)*100),w=Math.max(.5,(r.dur!=null?r.dur:NOW-r.t)/(15*MIN)*100);return `<i class="${r.status==null?'flight':''}" data-css="left:${x.toFixed(2)}%;width:${Math.min(w,100-x).toFixed(2)}%;background:${isErr(r)?'var(--bad)':r.status===499?'var(--faint)':col(r.m)}" title="${esc(r.m)} · ${r.status==null?'streaming':r.status}"></i>`};
  return `<div class="dh"><div class="grow">${sessPill(s)}<h3>${esc(s.name)}</h3><div class="s">${esc([s.project,s.branch].filter(Boolean).join(' · '))} · ${cid(s.sid)}</div></div>${closeBtn()}</div>
  <div class="db">
   <div class="gauges"><div class="gauge"><div class="top"><span>Context window</span><b>${c==null?'—':c+'%'}</b></div>${meter(c,c>=90?'max':c>=80?'hi':'')}<div class="n">${c!=null&&c>=80?'<span class="warnc">Compaction soon.</span> ':''}${s.ctxTok!=null&&s.ctxLim?fmt(s.ctxTok)+' of '+fmt(s.ctxLim)+' tokens':'—'}</div></div>
   <div class="gauge"><div class="top"><span>Prompt cache</span><b>${cl==null?'—':cl>0?dur(cl):'Cold'}</b></div>${meter(cl>0&&s.ttl?cl/s.ttl*100:0,cl!=null&&cl<60e3?'hi':'acc')}<div class="n">${s.ttl==null?'—':s.ttl>=HR?'1-hour':'5-minute'} cache · last hit ${ago(s.lastReq)}</div></div></div>
   <div><h4>Details</h4><div class="facts">
    <div class="fact"><div class="k">Session id</div><div class="v">${cid(s.id)}</div></div>
    <div class="fact"><div class="k">Path</div><div class="v">${cid(tilde(s.cwd))}</div></div>
    <div class="fact"><div class="k">Branch</div><div class="v mono">${esc(s.branch||'—')}</div></div>
    <div class="fact"><div class="k">Main model</div><div class="v">${modelTag(mainA(s).model)}</div></div>
    <div class="fact"><div class="k">Started</div><div class="v">${s.started==null?'—':clock(s.started).slice(0,5)+' · '+ago(s.started)}</div></div>
    <div class="fact"><div class="k">Last activity</div><div class="v">${ago(lastT(s))}</div></div>
    <div class="fact"><div class="k">Requests</div><div class="v">${rs.length} · ${rs.filter(isErr).length} errors</div></div>
    <div class="fact"><div class="k">Tokens in</div><div class="v">${fmt(tokInOf(rs))}</div></div>
    <div class="fact"><div class="k">Cache read</div><div class="v">${fmt(sum(rs.map(r=>n0(r.cr))))}</div></div>
    <div class="fact"><div class="k">Tokens out</div><div class="v">${fmt(tokOutOf(rs))}</div></div></div></div>
   ${histBlock('session:'+s.id,hOf(HT.ses,s.id))}
   <div><h4>Agents <span class="faint">${s.agents.length+s.bg.length}</span></h4><div class="card t">${table('d-ag',AG_COLS.filter(c=>['agent','model','state','dur','req','tok'].includes(c.k)),s.agents.concat(s.bg),{})}</div></div>
   <div><h4>Timeline <span class="faint">each bar is one request · last 15 min</span></h4><div class="swim">${s.agents.map(a=>`<div class="row"><div class="who">${a.type==='main'?'<b class="fw5">main</b>':esc(a.desc||a.type)}</div><div class="track">${lane(a).map(bar).join('')}</div></div>`).join('')}${unmatched.length?`<div class="row"><div class="who faint">not matched to an agent</div><div class="track">${unmatched.map(bar).join('')}</div></div>`:''}</div><div class="axis"><span>${clock(t0).slice(0,5)}</span><span>${clock(t0+5*MIN).slice(0,5)}</span><span>${clock(t0+10*MIN).slice(0,5)}</span><span>now</span></div></div>
   <div><h4>Recent requests</h4><div class="card t">${table('d-req',REQ_COLS.filter(c=>['time','status','model','ttft','in','out'].includes(c.k)).concat([{k:'reason',label:'Reason',v:r=>r.reason||'',h:r=>r.reason?`<span class="muted">${esc(r.reason)}</span>`:isErr(r)?`<span class="muted">${esc(errDef(r.status)[0])}</span>`:NA}]),rs.slice().reverse(),{row:r=>'request:'+r.id,limit:12,empty:'No requests kept for this session'})}</div></div>
   <div class="notebox">${ic('info')}<span>Read from Claude Code's session log for ${esc(s.sid)}. Prompt text and tool output are never read into this page.</span></div>
  </div>`},
 request(id){const r=REQS.find(x=>x.id===id)||HS.rows.find(x=>x.id===id)||EG.rows.find(x=>x.id===id);if(!r)return '';
  const total=r.dur!=null?r.dur:r.status==null?NOW-r.t:null,L=LANES.find(l=>l.id===r.lane);
  let wf='';
  if(total>0){const rt=n0(r.rt),tt=r.ttft!=null?r.ttft:r.status==null?Math.min(total,NOW-r.t):0;const s1=Math.min(100,rt/total*100),s2=Math.max(0,Math.min(100-s1,(tt-rt)/total*100)),s3=Math.max(0,100-s1-s2);
   wf=`<div><h4>Timing <span class="faint">${(total/1e3).toFixed(2)} s total</span></h4><div class="wf"><i data-css="width:${s1.toFixed(2)}%;background:var(--faint)"></i><i data-css="width:${s2.toFixed(2)}%;background:var(--warn)"></i><i data-css="width:${s3.toFixed(2)}%;background:${col(r.m)}"></i></div>
    <div class="wfl"><span><span class="sw" data-css="background:var(--faint)"></span>Router <b>${r.rt==null?'—':r.rt<1?'&lt;1 ms':r.rt+' ms'}</b></span><span><span class="sw" data-css="background:var(--warn)"></span>Time to first token <b>${msf(r.ttft)}</b></span><span><span class="sw" data-css="background:${col(r.m)}"></span>Streaming <b>${r.ttft!=null?((total-r.ttft)/1e3).toFixed(1)+' s':'—'}</b></span></div></div>`}
  return `<div class="dh"><div class="grow">${reqStatus(r)}<h3>${modelTag(r.m)} <span class="muted fw4 fs15">· ${clock(r.t)}</span></h3><div class="s">${cid(r.id.slice(1))}</div></div>${closeBtn()}</div>
  <div class="db">${wf}
   <div><h4>Tokens</h4><div class="facts">
    <div class="fact"><div class="k">Fresh input</div><div class="v">${fmt(r.in)}</div></div><div class="fact"><div class="k">Cache write</div><div class="v">${fmt(r.cw)}</div></div>
    <div class="fact"><div class="k">Cache read</div><div class="v">${fmt(r.cr)}</div></div><div class="fact"><div class="k">Output</div><div class="v">${r.status==null?'streaming…':fmt(r.out)}</div></div>
    <div class="fact"><div class="k">Output speed</div><div class="v">${speed(r)?Math.round(speed(r))+' tok/s':'—'}</div></div><div class="fact"><div class="k">Stop reason</div><div class="v mono">${esc(r.stop||'—')}</div></div></div></div>
   <div><h4>Route</h4><dl class="kv"><dt>Session</dt><dd>${r.s?r.h?`<span class="mono">${esc(r.s.sid)}</span>`:`<button class="cid" data-open="session:${esc(r.s.id)}">${esc(r.s.name)}</button> <span class="mono faint">${esc(r.s.sid)}</span>`:'—'}</dd><dt>Agent</dt><dd>${r.a?(r.a.type==='main'?'main':`<span class="mono">${esc(r.a.type)}</span> · ${esc(r.a.desc)}`):'—'}</dd>
    <dt>Lane</dt><dd>${L?`${esc(L.name)} <span class="mono faint">${esc(L.match)} → ${esc(L.id)}</span>`:esc(r.lane||'—')}</dd><dt>Upstream id</dt><dd>${cid(r.up)}</dd>
    ${r.status&&r.status!==200?`<dt>Cause</dt><dd>${esc(errDef(r.status,r.reason)[2])}</dd>`:''}</dl></div>
   <div class="notebox">${ic('info')}<span>The router keeps the model, timings, token counts and status. It never keeps the prompt, the answer or header values.</span></div>
  </div>`},
 provider(id){const x=provRows().find(p=>p.id===id);if(!x)return '';
  const chips=x.built?'':`<div><h4>Commands</h4><div class="chips">${cmd(`/huddle:${x.id}:setup`)}${cmd(`/huddle:${x.id}:remove`)}</div></div>`;
  return `<div class="dh"><div class="grow">${pill(x.status[0],x.status[1])}<h3>${esc(x.name)}</h3><div class="s">${x.built?'built in':esc(x.id)}</div></div>${closeBtn()}</div>
  <div class="db">
   <div class="facts">
    <div class="fact"><div class="k">Key</div><div class="v">${x.built?'Claude sign-in':x.key?'Found':'Not found'}</div></div>
    <div class="fact"><div class="k">Lane</div><div class="v">${x.laneOn?'On':'Off'}${x.pattern?` <span class="mono muted">${esc(x.pattern)}</span>`:''}</div></div>
    <div class="fact"><div class="k">Requests</div><div class="v">${x.n.toLocaleString('en-US')}</div></div>
    <div class="fact"><div class="k">Time to first token</div><div class="v">${x.n&&x.p50?msf(x.p50):'—'}</div></div>
    <div class="fact"><div class="k">Errors</div><div class="v">${x.err.toLocaleString('en-US')}</div></div>
    ${x.built?'':`<div class="fact"><div class="k">Agents</div><div class="v">${x.nAgents?x.nAgents+' agent'+(x.nAgents===1?'':'s'):'None'}</div></div>`}
   </div>
   ${x.built?`<div class="notebox">${ic('info')}<span>Requests for claude-* models go straight to Anthropic.</span></div>`:`<div><h4>/model entries <span class="faint">${x.models.length||x.nModels}</span></h4><div class="card list">${x.models.map(m=>`<div class="li"><div class="grow mono">${esc(m)}</div></div>`).join('')||`<div class="allclear">${ic('info')} ${x.nModels?x.nModels+' in the picker':'None in the picker'}</div>`}</div></div>`}
   ${chips}
  </div>`},
 project(name){const p=projects().find(x=>x.name===name);if(!p)return '';
  return `<div class="dh"><div class="grow"><span class="pill off">${ic('projects')}Project</span><h3>${esc(p.name)}</h3><div class="s">${cid(tilde(p.path))}</div></div>${closeBtn()}</div>
  <div class="db">
   <div class="facts"><div class="fact"><div class="k">Active sessions</div><div class="v">${p.live} of ${p.sessions.length}</div></div><div class="fact"><div class="k">Agents running</div><div class="v">${p.agentsRun}</div></div><div class="fact"><div class="k">Requests</div><div class="v">${p.reqs.length}</div></div><div class="fact"><div class="k">Tokens in / out</div><div class="v">${fmt(p.tin)} / ${fmt(p.tout)}</div></div></div>
   ${histBlock('project:'+p.name,hOf(HT.prj,p.name))}
   <div><h4>Branches <span class="faint">${p.branches.length}</span></h4><div class="card list">${p.branches.map(b=>`<div class="li"><div class="grow mono">${esc(b)}</div><span class="muted">${p.wts.filter(w=>w.branch===b).length} worktree${p.wts.filter(w=>w.branch===b).length===1?'':'s'}</span></div>`).join('')||`<div class="allclear">${ic('info')} No branches</div>`}</div></div>
   <div><h4>Sessions</h4><div class="card t">${table('d-ps',SESS_COLS.filter(c=>['state','title','model','ctx','last'].includes(c.k)),p.sessions,{row:s=>'session:'+s.id,empty:'No sessions'})}</div></div>
   <div><h4>Worktrees <span class="faint">${p.wts.length}</span></h4><div class="card list">${p.wts.map(w=>`<div class="li">${kindPill(w.kind)}<div class="grow"><div class="mono">${esc(w.branch||'(detached)')}</div><div class="sm mono">${esc(tilde(w.path))}</div></div>${changesCell(w)}</div>`).join('')||`<div class="allclear">${ic('info')} No worktrees</div>`}</div></div>
   ${p.jobs.length?`<div><h4>Jobs</h4><div class="card list">${p.jobs.map(j=>`<div class="li"><div class="grow"><div><span class="mono">${esc(j.id)}</span> <span class="muted">· ${esc(j.h)}</span></div><div class="sm">${esc(j.task)}</div></div>${jobPill(j)}</div>`).join('')}</div></div>`:''}
  </div>`}
};
let DRBACK=null;
function openDrawer(key){const[k,id]=key.split(/:(.*)/s);if(!DRAWERS[k])return;if(!DR)DRBACK=document.activeElement;DR={k,id,key};renderDrawer(false);$('#drawer').classList.add('on');$('#scrim').classList.add('on');refresh();$('#drawer').focus({preventScroll:true});loadTotals()}
function closeDrawer(){const key=DR&&DR.key;DR=null;$('#drawer').classList.remove('on');if(!$('#sb').classList.contains('on'))$('#scrim').classList.remove('on');refresh();
 const row=key&&document.querySelector(`[data-open="${CSS.escape(key)}"]`),back=row||(DRBACK&&DRBACK.isConnected?DRBACK:null);DRBACK=null;if(back)back.focus({preventScroll:true})}
function renderDrawer(keep){if(!DR)return;const d=$('#drawer'),body=$('#drawer .db'),top=keep&&body?body.scrollTop:0;patch(d,DRAWERS[DR.k](DR.id));const nb=$('#drawer .db');if(nb)nb.scrollTop=top}
function trapTab(e){const d=$('#drawer'),f=[...d.querySelectorAll('button,a[href],input,select,textarea,[tabindex="0"]')].filter(n=>!n.disabled&&n.offsetParent!==null);
 if(!f.length){e.preventDefault();d.focus();return}
 const first=f[0],last=f[f.length-1],a=document.activeElement;
 if(e.shiftKey&&(a===first||a===d)){e.preventDefault();last.focus()}else if(!e.shiftKey&&(a===last||!d.contains(a))){e.preventDefault();first.focus()}}
