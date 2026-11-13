const $=s=>document.querySelector(s), $$=s=>[...document.querySelectorAll(s)];
/* Every string from the snapshot passes through esc before it reaches markup; setHtml is the one place markup is written. */
const esc=s=>String(s==null?'':s).replace(/[&<>"']/g,c=>({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
function setHtml(el,html){el.innerHTML=html;el.querySelectorAll('[data-css]').forEach(n=>{n.style.cssText=n.dataset.css})}
/* A region is written only when its markup changed; a focused control comes back by id or data key. */
const LASTHTML=new WeakMap(), FOCUS_KEYS=['data-copy','data-sort','data-open','data-seg','data-metric','data-focus','data-q','data-hf','data-hp','data-close'];
function focusSelector(el){if(el.id)return '#'+CSS.escape(el.id);for(const a of FOCUS_KEYS){const v=el.getAttribute(a);if(v!==null)return `[${a}="${CSS.escape(v)}"]`}return null}
function patch(el,html){if(LASTHTML.get(el)===html)return false;
 const a=document.activeElement,keep=a&&el.contains(a)&&a!==el?{sel:focusSelector(a),s:a.selectionStart,e:a.selectionEnd}:null;
 setHtml(el,html);LASTHTML.set(el,html);
 if(keep&&keep.sel){const n=el.querySelector(keep.sel);if(n){n.focus({preventScroll:true});if(keep.s!=null){try{n.setSelectionRange(keep.s,keep.e)}catch(e){}}}}
 return true}
const num=v=>typeof v==='number'&&isFinite(v)?v:null;
const arr=v=>Array.isArray(v)?v:[];
const n0=v=>num(v)||0;
/* The router's tokens_in already holds the cache tokens; every page splits it here. */
const tokSplit=(tin,cr,cw)=>{const t=n0(tin),r=n0(cr),w=n0(cw),fresh=Math.max(0,t-r-w);return {tin:t,fresh,cr:r,cw:w,hit:t?r/t:0}};
/* Rates read per second, one decimal under 10. */
const rate=n=>num(n)==null?'—':n<10?n.toFixed(1):fmt(Math.round(n));
const fmt=n=>num(n)==null?'—':n>=1e6?(n/1e6).toFixed(n>=1e7?0:1)+'M':n>=1e3?(n/1e3).toFixed(n>=1e4?0:1)+'k':String(Math.round(n));
const SEC=1e3,MIN=6e4,HR=36e5,DAY=24*HR;
const sum=a=>a.reduce((x,y)=>x+y,0);
const pct=(a,p)=>{if(!a.length)return null;const s=a.slice().sort((x,y)=>x-y);return s[Math.min(s.length-1,Math.floor(p*s.length))]};
const wavg=(rows,val,w)=>{const t=sum(rows.map(w));return t?sum(rows.map(r=>val(r)*w(r)))/t:null};
const tilde=p=>typeof p==='string'&&p?p.replace(/^\/(?:Users|home)\/[^\/]+/,'~'):'—';
let SNAP={},OFFSET=0,NOW=Date.now(),LOST=false;
function ago(t){if(num(t)==null)return '—';const d=Math.max(0,NOW-t);if(d<4e3)return 'now';if(d<MIN)return Math.floor(d/1e3)+'s ago';if(d<HR)return Math.floor(d/MIN)+'m ago';if(d<DAY)return Math.floor(d/HR)+'h '+Math.floor(d%HR/MIN)+'m ago';return Math.floor(d/DAY)+'d ago'}
function dur(d){if(num(d)==null)return '—';d=Math.max(0,d);if(d<MIN)return Math.floor(d/1e3)+'s';if(d<HR)return Math.floor(d/MIN)+'m '+String(Math.floor(d%MIN/1e3)).padStart(2,'0')+'s';if(d<DAY)return Math.floor(d/HR)+'h '+Math.floor(d%HR/MIN)+'m';return Math.floor(d/DAY)+'d '+Math.floor(d%DAY/HR)+'h'}
const since=t=>num(t)==null?'—':dur(NOW-t);
const clock=t=>num(t)==null?'—':new Date(t).toTimeString().slice(0,8);
const hm=t=>clock(t).slice(0,5);
const msf=v=>num(v)==null?'—':v>=1e4?(v/1e3).toFixed(1)+' s':Math.round(v)+' ms';
const mb=b=>num(b)==null?'—':(b/1e6).toFixed(1)+' MB';

/* ---------- icons ---------- */
const ICONS={
 overview:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
 sessions:'<rect x="3" y="4" width="18" height="16" rx="2.5"/><path d="m7 9 3 3-3 3M12.5 15H17"/>',
 agents:'<rect x="5" y="8" width="14" height="11" rx="3"/><path d="M12 4v4M9.5 13h.01M14.5 13h.01M2.5 12.5v3M21.5 12.5v3"/>',
 requests:'<path d="M3 12h4l3-8 4 16 3-8h4"/>',
 errors:'<path d="M10.3 3.9 2.4 18a2 2 0 0 0 1.7 3h15.8a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z"/><path d="M12 9v4M12 17h.01"/>',
 models:'<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4"/>',
 providers:'<circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="5" r="2.5"/><path d="M8.5 19H15a3.5 3.5 0 0 0 0-7H9a3.5 3.5 0 0 1 0-7h6.5"/>',
 usage:'<path d="m12 14 4-4"/><path d="M3.3 19a10 10 0 1 1 17.4 0"/>',
 projects:'<path d="M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z"/>',
 worktrees:'<circle cx="6" cy="5" r="2.5"/><circle cx="6" cy="19" r="2.5"/><circle cx="18" cy="7" r="2.5"/><path d="M6 7.5v9M18 9.5c0 5-7 4-11.2 7.2"/>',
 jobs:'<path d="M22 2 11 13M22 2l-7 20-4-9-9-4z"/>',
 harnesses:'<path d="m12 2 10 5-10 5L2 7z"/><path d="m2 17 10 5 10-5M2 12l10 5 10-5"/>',
 router:'<rect x="3" y="4" width="18" height="7" rx="2"/><rect x="3" y="13" width="18" height="7" rx="2"/><path d="M7 7.5h.01M7 16.5h.01"/>',
 check:'<path d="M20 6 9 17l-5-5"/>', x:'<path d="M18 6 6 18M6 6l12 12"/>', minus:'<path d="M5 12h14"/>',
 alert:'<path d="M12 8v5M12 16.5h.01"/><circle cx="12" cy="12" r="9"/>',
 copy:'<rect x="9" y="9" width="12" height="12" rx="2"/><path d="M5 15H4a1 1 0 0 1-1-1V4a1 1 0 0 1 1-1h10a1 1 0 0 1 1 1v1"/>',
 chev:'<path d="m9 6 6 6-6 6"/>', info:'<circle cx="12" cy="12" r="9"/><path d="M12 11v5M12 8h.01"/>',
 search:'<circle cx="11" cy="11" r="7"/><path d="m20 20-3.5-3.5"/>',
 sun:'<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4"/>',
 moon:'<path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z"/>', monitor:'<rect x="3" y="4" width="18" height="12" rx="2"/><path d="M8 20h8M12 16v4"/>'
};
const ic=(n,c)=>`<svg class="i${c?' '+c:''}" viewBox="0 0 24 24" aria-hidden="true">${ICONS[n]||''}</svg>`;

/* ---------- the world, read from the snapshot ---------- */
let SESS=[],AGENTS=[],REQS=[],MODELS=[],MN=[],COLOR={},LANES=[],JOBS=[],WT=[],HARN=[],BK=[],PS=[],BW=3e4,PLAN=null,PL={state:'checking',rows:[],checks:[]},WIRE={state:'checking'},RT={},ST=null;
const col=m=>COLOR[m]||HCOLOR[m]||'var(--faint)';
const sstate=s=>({needs_you:'waiting',your_turn:'waiting',working:'working',background:'working'})[s.state]||'idle';
const mainA=s=>s.agents[0];
const isErr=r=>r.status!=null&&r.status>=400&&r.status!==499;
const EMPTY={n:0,tok:0,err:0,p50:0,cost:0};
const ERRDEF={
 529:['Overloaded','overloaded_error','Anthropic was busy. Claude Code retried.',true],
 429:['Rate limited','rate_limit_error','The provider throttled the request. Claude Code backed off and retried.',true],
 502:['Upstream failed','upstream connect','The router could not reach the provider.',true],
 499:['Cancelled','client closed the stream','You pressed Esc, or the session ended mid-stream. Not counted as an error.',false]
};
const errDef=(c,reason)=>ERRDEF[c]||['Status '+c,reason||'',reason||'The provider answered with status '+c+'.',true];

function buildSessions(){
 SESS=arr(SNAP.sessions).map(x=>{
  const tok=num(x.context_tokens),lim=num(x.context_limit);
  const o={id:String(x.id||''),title:x.title||'',project:x.project||'',cwd:x.cwd||'',branch:x.branch||'',model:x.model||'',state:x.state||'idle',started:num(x.started_ms),last:num(x.last_ms),waitingSince:num(x.state_since_ms),openTasks:num(x.open_tasks),questionTool:x.question_tool||'',ctxTok:tok,ctxLim:lim,ctx:tok!=null&&lim?tok/lim*100:null,ttl:num(x.cache_ttl_ms),lastReq:num(x.last_request_ms)};
  o.sid=o.id.slice(0,8);o.name=o.title||o.project||o.sid||'Session';
  let ag=arr(x.agents).map(a=>({id:String(a.id||''),type:a.type||'',desc:a.description||'',model:a.model||'',state:a.state||'idle',start:num(a.started_ms),end:num(a.ended_ms),wt:a.worktree||null,s:o}));
  let m=ag.find(a=>a.id==='main');ag=ag.filter(a=>a!==m);
  if(!m)m={id:'main',type:'main',desc:'',model:o.model,state:'idle',start:o.started,end:null,wt:null,s:o};
  m.desc=o.name;if(!m.model)m.model=o.model;
  o.agents=[m].concat(ag);return o});
 AGENTS=SESS.flatMap(s=>s.agents);
}
function buildRequests(){
 const byId={};SESS.forEach(s=>{byId[s.id]=s});
 const mk=(id,x,t)=>({id,t,sess:x.session||null,agentId:x.agent||null,m:x.model||'',lane:x.lane||''});
 REQS=arr(SNAP.recent).map(x=>Object.assign(mk('r'+x.id,x,num(x.at_ms)),{status:num(x.status),reason:x.reason||null,ttft:num(x.ttft_ms),dur:num(x.dur_ms),rt:num(x.router_ms),in:num(x.tokens_in)==null?null:tokSplit(x.tokens_in,x.cache_read,x.cache_write).fresh,cr:num(x.cache_read),cw:num(x.cache_write),out:num(x.tokens_out),stop:x.stop||null,up:x.upstream_id||null}));
 REQS=REQS.filter(r=>r.t!=null);
 arr(SNAP.in_flight).forEach(x=>{const t=num(x.started_ms);if(t!=null)REQS.push(Object.assign(mk('f'+x.id,x,t),{status:null,reason:null,ttft:null,dur:null,rt:null,in:null,cr:null,cw:null,out:null,stop:null,up:null,lastByte:num(x.last_byte_ms)}))});
 REQS.sort((a,b)=>a.t-b.t);
 REQS.forEach(r=>{r.s=byId[r.sess]||null;r.a=r.s&&r.agentId?r.s.agents.find(a=>r.s.id+':'+a.id===r.agentId)||null:null});
}
const tokIn=r=>n0(r.in)+n0(r.cw)+n0(r.cr);
function stats(){
 if(ST)return ST;
 const t0=NOW-15*MIN,done=REQS.filter(r=>r.status!=null&&r.t>=t0);
 const group=(a,f)=>{const o={};a.forEach(r=>{const k=f(r);if(k!=null)(o[k]=o[k]||[]).push(r)});return o};
 ST={done,flights:REQS.filter(r=>r.status==null),bySess:group(REQS,r=>r.sess),byAgent:group(REQS,r=>r.s&&r.a?r.s.id+':'+r.a.id:null),byLane:group(done,r=>r.lane)};
 return ST;
}
const sReq=s=>stats().bySess[s.id]||[];
const aReq=a=>stats().byAgent[a.s.id+':'+a.id]||[];
const tokInOf=rs=>sum(rs.map(tokIn)), tokOutOf=rs=>sum(rs.map(r=>n0(r.out)));
const lastT=s=>Math.max(s.last||0,s.lastReq||0)||null;
const cacheLeft=s=>s.ttl!=null&&s.lastReq!=null?s.ttl-(NOW-s.lastReq):null;
function buildModels(){
 const rows=Array.isArray(SNAP.models)?SNAP.models:null;
 if(rows){MODELS=rows.map(x=>({id:x.model||'',lane:x.lane||'',n:n0(x.requests),err:n0(x.errors),p50:num(x.ttft_p50),p95:num(x.ttft_p95),speed:num(x.out_tps_p50),...tokSplit(x.tokens_in,x.cache_read,x.cache_write),out:n0(x.tokens_out),sess:num(x.sessions),last:num(x.last_ms)}))}
 else{const g={};stats().done.forEach(r=>{(g[r.m]=g[r.m]||[]).push(r)});
  MODELS=Object.entries(g).map(([id,rs])=>{const tt=rs.filter(r=>r.ttft!=null).map(r=>r.ttft);return {id,lane:rs[0].lane,n:rs.length,err:rs.filter(isErr).length,p50:pct(tt,.5),p95:pct(tt,.95),speed:null,tin:tokInOf(rs),fresh:sum(rs.map(r=>n0(r.in))),cw:sum(rs.map(r=>n0(r.cw))),cr:sum(rs.map(r=>n0(r.cr))),out:tokOutOf(rs),sess:new Set(rs.map(r=>r.sess).filter(Boolean)).size,last:rs[rs.length-1].t}})}
 const b=SNAP.buckets&&typeof SNAP.buckets==='object'?SNAP.buckets:{};
 BW=num(b.width_ms)||3e4;const end=num(b.end_ms)||NOW;
 BK=Array.from({length:30},(_,i)=>({n:0,tok:0,err:0,p50:0,w:0,byM:{},t:end-(30-i)*BW}));
 const names=new Set(MODELS.map(m=>m.id));
 for(const [m,list] of Object.entries(b.models&&typeof b.models==='object'?b.models:{})){
  names.add(m);const l=arr(list).slice(-30),off=30-l.length;
  l.forEach((x,j)=>{const k=BK[off+j],o={n:n0(x&&x.n),err:n0(x&&x.err),tok:n0(x&&x.tok_in)+n0(x&&x.tok_out),p50:n0(x&&x.ttft_p50)};k.byM[m]=o;k.n+=o.n;k.err+=o.err;k.tok+=o.tok;k.w+=o.p50*o.n});
 }
 BK.forEach(k=>{k.p50=k.n?k.w/k.n:0});
 MN=[...names].filter(Boolean).sort();COLOR={};MN.forEach((m,i)=>{COLOR[m]='var(--c'+(i%6+1)+')'});
}
function buildLanes(){
  LANES=arr(SNAP.lanes).map(l=>{const last=l.last&&num(l.last.at_ms)!=null?{status:num(l.last.status),t:l.last.at_ms}:null;
  return {id:l.id||'',name:l.name||l.id||'',match:l.models||'—',state:l.enabled?'live':'off',
   n:n0(l.requests),err:n0(l.errors),p50:num(l.ttft_p50!=null?l.ttft_p50:l.ttft_p50_ms),last,setup:l.setup||null,
   models:MODELS.filter(m=>m.lane===l.id).map(m=>m.id)}});
}
function build(s){
 SNAP=s&&typeof s==='object'?s:{};ST=null;NOW=Date.now()+OFFSET;
 PS=arr(SNAP.per_second).map(n0);buildSessions();buildRequests();buildModels();buildLanes();
 const p=SNAP.plan&&typeof SNAP.plan==='object'?SNAP.plan:null;
 PLAN=p&&(num(p.five_hour_pct)!=null||num(p.week_pct)!=null)?{h5:num(p.five_hour_pct),h5r:num(p.five_hour_reset_ms),wk:num(p.week_pct),wkr:num(p.week_reset_ms)}:null;
 JOBS=arr(SNAP.jobs).map(j=>({id:j.id||'',h:j.harness||'',task:j.title||'',repo:j.repo||'',state:j.state||'',started:num(j.started_ms),end:num(j.ended_ms),files:num(j.files),add:num(j.add),del:num(j.del),why:j.why||null}));
 WT=arr(SNAP.worktrees).map(w=>({path:w.path||'',project:w.project||'',branch:w.branch||'',head:w.head||'',kind:w.kind||'checkout',by:w.used_by||null,changes:num(w.changes),commit:num(w.last_commit_ms)}));
 WT.forEach(w=>{w.stale=w.kind==='agent'&&!w.by&&w.commit!=null&&NOW-w.commit>=5*DAY});
 const pg=SNAP.plugins&&typeof SNAP.plugins==='object'?SNAP.plugins:{};
 PL={state:pg.state==='ready'?'ready':'checking',rows:arr(pg.rows),checks:arr(pg.checks)};
 const hs=SNAP.harnesses&&typeof SNAP.harnesses==='object'?SNAP.harnesses:{};
 HARN=arr(hs.rows).map(h=>({id:h.id||'',name:h.name||h.id||'',st:h.status||'missing',bin:h.bin||'',ver:h.version||'',flags:h.flags||'',report:h.report||'',access:arr(h.access),jobs:JOBS.filter(j=>j.h===h.id).length,ready:hs.state==='ready'}));
 HARN.state=hs.state==='ready'?'ready':'checking';
 WIRE=SNAP.wiring&&typeof SNAP.wiring==='object'?SNAP.wiring:{};
 const r=SNAP.router&&typeof SNAP.router==='object'?SNAP.router:{};
 RT={cpu:num(r.cpu_pct),rss:num(r.rss_bytes),viewers:num(r.viewers),conns:num(r.connections),events:arr(r.events),path:r.path&&typeof r.path==='object'?r.path:{}};
}
function projects(){
 const names=[...new Set([...SESS.map(s=>s.project),...WT.map(w=>w.project),...JOBS.map(j=>j.repo)].filter(Boolean))];
 return names.map(n=>{const ss=SESS.filter(s=>s.project===n),rs=ss.flatMap(sReq),wts=WT.filter(w=>w.project===n),jobs=JOBS.filter(j=>j.repo===n);
  const byM={};rs.forEach(r=>{byM[r.m]=(byM[r.m]||0)+1});const top=Object.entries(byM).sort((a,b)=>b[1]-a[1])[0];
  const home=wts.find(w=>w.kind==='checkout')||wts[0];
  return {name:n,path:home?home.path:ss[0]?ss[0].cwd:'',sessions:ss,live:ss.filter(s=>sstate(s)!=='idle').length,
   agentsRun:ss.flatMap(s=>s.agents).filter(a=>a.type!=='main'&&a.state==='running').length,wts,jobs,reqs:rs,tin:tokInOf(rs),tout:tokOutOf(rs),top:top?top[0]:null,
   branches:[...new Set(wts.map(w=>w.branch).filter(Boolean))],last:Math.max(0,...ss.map(lastT),...jobs.map(j=>j.end||j.started||0))||null}});
}

/* ---------- components ---------- */
function pill(k,txt,short){const icon=k==='ok'?ic('check'):k==='bad'?ic('x'):k==='warn'?ic('alert'):k==='acc'?'<span class="dot acc pulse"></span>':k==='off'?ic('minus'):'';return `<span class="pill ${k}">${icon}${short?`<span class="stl">${esc(txt)}</span><span class="stword">${esc(short)}</span>`:esc(txt)}</span>`}
const dot=(k,p)=>`<span class="dot ${k}${p?' pulse':''}"></span>`;
const NA='<span class="faint">—</span>';
const sw=m=>`<span class="sw" data-css="background:${col(m)}"></span>`;
const modelTag=m=>m?`<span class="mt">${sw(m)}<span class="mono">${esc(m)}</span></span>`:NA;
const cmd=c=>`<button class="cmd" data-copy="${esc(c)}" title="Copy">${esc(c).replace(/:/g,':<wbr>')}${ic('copy')}</button>`;
const cid=t=>t?`<button class="cid" data-copy="${esc(t)}" title="Copy">${esc(t)}</button>`:NA;
const meter=(p,k)=>`<div class="meter"><i class="${k||''}" data-css="width:${Math.max(0,Math.min(100,num(p)||0)).toFixed(1)}%"></i></div>`;
function spark(v,color,cls){const n=v.length;if(n<2)return '';const max=Math.max(1e-9,...v);const d=v.map((x,i)=>(i?'L':'M')+(i/(n-1)*100).toFixed(2)+' '+(26-x/max*22).toFixed(2)).join(' ');
 return `<svg class="${cls||'sp'}" viewBox="0 0 100 28" preserveAspectRatio="none" aria-hidden="true"><path d="${d} L100 28 L0 28Z" fill="${color}" fill-opacity=".12"/><path d="${d}" fill="none" stroke="${color}" stroke-width="1.5" vector-effect="non-scaling-stroke" stroke-linejoin="round"/></svg>`}
function deltaH(cur,prev,goodUp){if(!prev&&!cur)return '';if(!prev)return `<span class="delta flat" title="last 5 min vs the 5 min before">new</span>`;const p=Math.round((cur-prev)/prev*100);if(Math.abs(p)<2)return `<span class="delta flat" title="last 5 min vs the 5 min before">±0%</span>`;const good=(p>0)===goodUp;return `<span class="delta ${goodUp==null?'flat':good?'up':'down'}" title="last 5 min vs the 5 min before">${p>0?'↑':'↓'} ${Math.abs(p)}%</span>`}
function statePill(s){const st=sstate(s);if(st==='waiting')return `<span class="pill warn"><span class="dot warn pulse"></span>${esc(['Waiting',since(s.waitingSince)].filter(x=>x!=='—').join(' '))}</span>`;if(st==='working')return pill('acc','Working');return `<span class="pill off">${ic('minus')}${esc(['Idle',since(lastT(s))].filter(x=>x!=='—').join(' '))}</span>`}
function agentPill(a){if(a.state==='running')return pill('acc','Running');if(a.state==='waiting')return `<span class="pill warn"><span class="dot warn"></span>Waiting on you</span>`;if(a.state==='done')return pill('ok','Done');return pill('off','Idle')}
function reqStatus(r){if(r.status==null)return pill('acc','Streaming');if(r.status===200)return pill('ok','200');if(r.status===499)return pill('off','499 cancelled');if(r.status===429)return pill('warn','429');return pill('bad',String(r.status))}
function ctxCell(s){if(s.ctx==null)return NA;const c=Math.round(s.ctx);return `<div class="fill"><span class="mono">${c}%</span>${meter(c,c>=90?'max':c>=80?'hi':'')}</div>`}
function cacheCell(s){const l=cacheLeft(s);if(l==null)return NA;if(l<=0)return pill('off','Cold');if(l<60e3)return pill('warn','Cools in '+dur(l));return pill('ok','Warm · '+dur(l))}
function agentsCell(s){const subs=s.agents.slice(1);if(!subs.length)return NA;const run=subs.filter(a=>a.state==='running').length;return `${subs.length}${run?` <span class="muted">· ${run} running</span>`:''}<span class="dots">${subs.map(a=>`<i class="${esc(a.state)}" title="${esc(a.desc)}"></i>`).join('')}</span>`}
const wtName=p=>p?`<span class="mono">${esc(String(p).split('/').pop())}</span>`:'<span class="faint">session checkout</span>';
const who=r=>r.s?`<div>${esc(r.s.project||r.s.name)} <span class="mono faint">${esc(r.s.sid)}</span></div><div class="cs sans">${r.a?esc(r.a.type==='main'?'main':r.a.desc||r.a.type):'—'}</div>`:NA;

/* table: one component for every list; columns are sortable when they carry a sort key */
const SORT={};
function table(id,cols,rows,opt){opt=opt||{};const st=SORT[id]||opt.sort;let rs=rows.slice();
 if(st){const c=cols.find(c=>c.k===st.k);if(c&&c.v)rs.sort((a,b)=>{const x=c.v(a),y=c.v(b);return (x>y?1:x<y?-1:0)*st.d})}
 if(opt.limit)rs=rs.slice(0,opt.limit);
 const th=cols.map(c=>{const on=st&&st.k===c.k,lab=`${c.label}${on?`<span class="caret">${st.d>0?'↑':'↓'}</span>`:''}`;return `<th class="${c.num?'num ':''}${c.c?'c ':''}${c.sm?'hide-sm ':''}${c.v?'sortable':''}"${c.v?` aria-sort="${on?(st.d>0?'ascending':'descending'):'none'}"`:''}${c.w?` data-css="width:${c.w}"`:''}>${c.v?`<button type="button" class="sortbtn" data-sort="${id}:${c.k}">${lab}</button>`:lab}</th>`}).join('');
 const tb=rs.length?rs.map(r=>{const o=opt.row?opt.row(r):null;const cls=[o?'clickable':'',opt.cls?opt.cls(r):'',o&&DR&&DR.key===o?'sel':''].join(' ').trim();
  return `<tr${cls?` class="${cls}"`:''}${o?` tabindex="0" data-open="${esc(o)}"`:''}>${cols.map(c=>`<td class="${c.num?'num ':''}${c.c?'c ':''}${c.sm?'hide-sm ':''}${c.cls||''}" data-label="${esc(c.label)}">${c.h(r)}</td>`).join('')}</tr>`}).join('')
  :`<tr><td colspan="${cols.length}" class="empty">${opt.empty||'Nothing here yet'}</td></tr>`;
 return `<div class="tbl"><table${opt.stack?' class="fold"':''}><thead><tr>${th}</tr></thead><tbody>${tb}</tbody></table></div>`}

/* regions: parts of a page that refresh on every snapshot without touching the rest */
let REG={};
function reg(name,fn){REG[name]=fn;return `<div class="reg" id="r-${name}">${fn()}</div>`}
function refresh(){for(const[k,fn]of Object.entries(REG)){const el=document.getElementById('r-'+k);if(!el)continue;const sc=[...el.querySelectorAll('.tbl')].map(t=>t.scrollLeft);if(patch(el,fn()))el.querySelectorAll('.tbl').forEach((t,i)=>{t.scrollLeft=sc[i]||0})}}
const head=(t,d,act)=>`<div class="ph"><div><h1>${t}</h1>${d?`<p>${d}</p>`:''}</div>${act?`<div class="act">${act}</div>`:''}</div>`;
const secH=(t,extra,more)=>`<div class="sh"><h2>${t}</h2>${extra||''}${more?`<a class="more" href="#${more[0]}">${more[1]}${ic('chev')}</a>`:''}</div>`;
const F={sess:'all',agents:'all',req:'all',lane:'all',harn:'all',wt:'all',q:{}};
const seg=(id,opts,cur)=>`<div class="seg" role="group">${opts.map(([k,l,n])=>`<button data-seg="${id}:${esc(k)}" aria-pressed="${k===cur}">${esc(l)}${n!=null?`<span class="n">${n}</span>`:''}</button>`).join('')}</div>`;
const search=(id,ph)=>`<label class="sbox">${ic('search')}<input class="search" data-q="${id}" placeholder="${ph}" value="${esc(F.q[id]||'')}" aria-label="${ph}"></label>`;
const qmatch=(id,txt)=>{const q=(F.q[id]||'').trim().toLowerCase();return !q||txt.toLowerCase().includes(q)};
function kpiGrid(items){return items.map(k=>`<div class="kpi"><div class="k">${k.k}</div><div class="v">${k.v}${k.small?`<small>${k.small}</small>`:''}${k.delta||''}</div>${k.meter!=null?meter(k.meter,k.mk):''}${k.sp?spark(k.sp,k.color||'var(--accent)'):''}${k.d?`<div class="d">${k.d}</div>`:''}</div>`).join('')}
const checking=(what)=>`<span class="muted">Checking…</span>`;

/* ---------- time ranges ---------- */
const RANGES=[['15m','15 min'],['24h','24 h'],['7d','7 d'],['30d','30 d']],RANGE_MIN={'15m':15,'24h':1440,'7d':10080,'30d':43200};
const FLAT='Claude plan usage is flat-rate; this is what the same tokens cost on the API';
const HIST={range:'15m',bk:null,tot:null,ser:null,err:false},HCOLOR={};
try{const r=localStorage.getItem('huddle-range');if(RANGES.some(x=>x[0]===r))HIST.range=r}catch(e){}
const liveRange=()=>HIST.range==='15m';
/* Only pages that read the router's live memory say a restart clears them; the rest read disk or settings. */
const readsMemory=()=>({overview:liveRange(),models:liveRange(),usage:liveRange(),sessions:true,agents:true,requests:HS.mode==='live',errors:HS.mode==='live'})[page]===true;
const rangeLabel=()=>RANGES.find(x=>x[0]===HIST.range)[1];
const rangeSeg=()=>seg('range',RANGES,HIST.range);
const usd=v=>num(v)==null?'—':'$'+(v>0&&v<0.01?v.toFixed(4):v.toFixed(2));
const apiq=()=>`<span class="faint" title="${FLAT}">API-equivalent</span>`;
const SER0={b:[0,1].map(i=>Object.assign({byM:{},t:i,w:0},EMPTY)),mn:[],bw:3e4};
const ser=()=>liveRange()?{b:BK,mn:MN,bw:BW}:(HIST.ser||SER0);
const rangeMin=()=>RANGE_MIN[HIST.range];
const tickLabel=t=>HIST.range==='7d'||HIST.range==='30d'?new Date(t).toLocaleDateString(undefined,{month:'short',day:'numeric'}):clock(t).slice(0,5);
function histSeries(d){const w=Math.max(1,num(d.width_ms)||3e5),s0=Math.floor(d.from_ms/w)*w,N=Math.max(2,Math.floor((d.to_ms-s0)/w)+1);
 const b=Array.from({length:N},(_,i)=>Object.assign({byM:{},t:s0+i*w,w:0},EMPTY)),names=new Set();
 arr(d.buckets).forEach(x=>{const k=b[Math.min(N-1,Math.max(0,Math.floor((x.start_ms-s0)/w)))],o={n:n0(x.n),err:n0(x.err),tok:n0(x.tok_in)+n0(x.tok_out),p50:n0(x.ttft_p50),cost:n0(x.cost_usd)};
  k.byM[x.model]=o;k.n+=o.n;k.err+=o.err;k.tok+=o.tok;k.cost+=o.cost;k.w+=o.p50*o.n;names.add(x.model)});
 b.forEach(k=>{k.p50=k.n?k.w/k.n:0});return {b,mn:[...names].filter(Boolean).sort(),bw:w}}
/* per-model rows for a history range: totals carry counts and cost, buckets carry latency */
function histRows(){const lat={};arr(HIST.bk&&HIST.bk.buckets).forEach(x=>{const l=lat[x.model]||(lat[x.model]={w:0,n:0,p95:null});if(num(x.ttft_p50)!=null){l.w+=x.ttft_p50*n0(x.n);l.n+=n0(x.n)}if(num(x.ttft_p95)!=null)l.p95=Math.max(l.p95||0,x.ttft_p95)});
 const ts=arr(HIST.tot&&HIST.tot.totals),all=Math.max(1,sum(ts.map(t=>n0(t.n))));
 return ts.map(t=>{const l=lat[t.key]||{n:0,p95:null};
  return {id:t.key,n:n0(t.n),err:n0(t.err),...tokSplit(t.tok_in,t.cache_read,t.cache_write),out:n0(t.tok_out),p50:l.n?l.w/l.n:null,p95:l.p95,cost:num(t.cost_usd),apiq:!!t.api_equivalent,share:n0(t.n)/all}})}
const RM=()=>liveRange()?MODELS:histRows();
const HIDE_IN_RANGE=['lane','trend','speed','sess','last'];
const COSTCOL={k:'cost',label:'Est. cost',num:1,v:x=>x.cost||0,h:x=>`<span class="mono">${usd(x.cost)}</span>${x.apiq?' '+apiq():''}`};
const rangeCols=cols=>liveRange()?cols:cols.filter(c=>!HIDE_IN_RANGE.includes(c.k)).concat([COSTCOL]);
const rangeRows=()=>liveRange()?modelRows():histRows();
const rangeEmpty=()=>HIST.bk||HIST.err||liveRange()?`No requests in the last ${rangeLabel()}`:'Checking…';
const rangeStatus=()=>liveRange()?'':HIST.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:HIST.bk?'':`<div class="legendrow">${checking()}</div>`;
function histKpis(){if(!HIST.tot)return `<div class="card pad">${checking()}</div>`;
 const ts=arr(HIST.tot.totals),reqs=sum(ts.map(t=>n0(t.n)));
 if(!reqs)return `<div class="card"><div class="allclear">${ic('info')} No requests in the last ${rangeLabel()}</div></div>`;
 const errs=sum(ts.map(t=>n0(t.err))),tin=sum(ts.map(t=>n0(t.tok_in))),cr=sum(ts.map(t=>n0(t.cache_read))),cw=sum(ts.map(t=>n0(t.cache_write))),out=sum(ts.map(t=>n0(t.tok_out))),cost=sum(ts.map(t=>n0(t.cost_usd))),aq=ts.some(t=>t.api_equivalent);
 return kpiCard([
  {k:'Requests',v:fmt(reqs),d:`in the last ${rangeLabel()}`},
  {k:'Errors',v:fmt(errs),d:`${(errs/reqs*100).toFixed(1)}% of requests`,color:'var(--bad)'},
  {k:'Tokens in',v:fmt(tin),d:'cache reads and writes included'},
  {k:'Tokens out',v:fmt(out),d:'generated'},
  {k:'Cache hit rate',v:tin?Math.round(tokSplit(tin,cr,cw).hit*100):'—',small:tin?'%':'',d:'read from cache / tokens in'},
  {k:'Estimated cost',v:usd(cost),d:aq?`includes Claude cost, ${apiq()}`:'from list prices'}],'k6')}
const hget=u=>fetch(u,{cache:'no-store'}).then(x=>{if(!x.ok)throw new Error(String(x.status));return x.json()});
function loadHistory(){if(liveRange())return;const r=HIST.range,get=hget;
 Promise.all([get('/huddle-router/history?view=buckets&range='+r),get('/huddle-router/history?view=totals&by=model&range='+r)]).then(([bk,tot])=>{if(HIST.range!==r)return;
  HIST.bk=bk;HIST.tot=tot;HIST.ser=histSeries(bk);HIST.err=false;HIST.ser.mn.forEach((m,i)=>{HCOLOR[m]='var(--c'+(i%6+1)+')'});paint()},()=>{if(HIST.range!==r)return;HIST.err=true;paint()})}
function setRange(k){if(!RANGES.some(r=>r[0]===k))return;HIST.range=k;HIST.bk=HIST.tot=HIST.ser=null;HIST.err=false;CH.focus=null;if(liveRange()&&CH.metric==='cost')CH.metric='req';
 try{localStorage.setItem('huddle-range',k)}catch(e){}
 render();loadHistory()}
const pricesLine=d=>d?`<div class="legendrow">Estimated from list prices. Prices retrieved ${esc(d.prices_retrieved)}; Claude prices retrieved ${esc(d.claude_prices_retrieved)}.</div>`:'';
const priceNote=()=>liveRange()?'':pricesLine(HIST.bk);

/* ---------- 30-day history of sessions and projects ---------- */
const HT={ses:null,prj:null,meta:null,trend:{},err:false},HZERO={n:0,err:0,tok_in:0,cache_read:0,cache_write:0,tok_out:0,cost_usd:0,models:[]};
const HQ='/huddle-router/history?view=';
const hmap=d=>{const m={};arr(d&&d.totals).forEach(t=>{m[t.key]=t});return m};
const hOf=(m,k)=>m?m[k]||HZERO:null;
const hCell=(t,f)=>t?f(t):HT.err?NA:checking();
const sheetKey=()=>DR&&(DR.k==='session'||DR.k==='project')?DR:null;
const wantTotals=()=>page==='sessions'||page==='projects'||!!sheetKey();
function loadTotals(){if(!wantTotals())return;const q=sheetKey(),key=q&&q.key;
 Promise.all([hget(HQ+'totals&by=session&range=30d'),hget(HQ+'totals&by=project&range=30d'),q?hget(HQ+'buckets&range=30d&'+q.k+'='+encodeURIComponent(q.id)):null]).then(([s,p,t])=>{
  HT.ses=hmap(s);HT.prj=hmap(p);HT.meta=s;if(t)HT.trend[key]=t;HT.err=false;paint()},()=>{HT.err=true;paint()})}
const costCell=t=>`<span class="mono">${usd(t.cost_usd)}</span>${t.api_equivalent?' '+apiq():''}`;
const histCols=get=>[
 {k:'hreq',label:'Requests (30 d)',num:1,sm:1,v:x=>n0((get(x)||HZERO).n),h:x=>hCell(get(x),t=>t.n?fmt(t.n):'<span class="faint">0</span>')},
 {k:'htok',label:'Tokens (30 d)',num:1,sm:1,v:x=>n0((get(x)||HZERO).tok_in)+n0((get(x)||HZERO).tok_out),h:x=>hCell(get(x),t=>t.n?`${fmt(t.tok_in)} <span class="faint">/</span> ${fmt(t.tok_out)}`:NA)},
 {k:'hcost',label:'Cost (30 d)',num:1,v:x=>n0((get(x)||HZERO).cost_usd),h:x=>hCell(get(x),t=>t.n?costCell(t):NA)}];
/* the History block of a session or project sheet: totals, a daily trend and the models used */
function histBlock(key,t){const tr=HT.trend[key],head='<h4>History <span class="faint">last 30 days</span></h4>';
 if(!t)return `<div>${head}<div class="card pad">${HT.err?'<span class="badc">Could not load history. Showing the last data.</span>':checking()}</div></div>`;
 if(!t.n)return `<div>${head}<div class="card"><div class="allclear">${ic('info')} No requests in the last 30 d</div></div></div>`;
 const tin=n0(t.tok_in),fact=(k,v)=>`<div class="fact"><div class="k">${k}</div><div class="v">${v}</div></div>`;
 const mcols=[
  {k:'model',label:'Model',v:m=>m.model,h:m=>modelTag(m.model)},
  {k:'n',label:'Requests',num:1,v:m=>n0(m.n),h:m=>fmt(m.n)},
  {k:'tok',label:'Tokens in / out',num:1,v:m=>n0(m.tok_in),h:m=>`${fmt(m.tok_in)} <span class="faint">/</span> ${fmt(m.tok_out)}`},
  {k:'cost',label:'Cost',num:1,v:m=>n0(m.cost_usd),h:m=>`<span class="mono">${usd(m.cost_usd)}</span>`}];
 return `<div>${head}<div class="facts">${fact('Requests',fmt(t.n)+' · '+fmt(t.err)+' errors')+fact('Tokens in',fmt(tin))+fact('Tokens out',fmt(t.tok_out))
  +fact('Cache hit rate',tin?Math.round(tokSplit(tin,t.cache_read,t.cache_write).hit*100)+'%':'—')+fact('Estimated cost',costCell(t))+fact('First seen',new Date(t.first_ms).toLocaleDateString()+' · '+ago(t.first_ms))+fact('Last seen',new Date(t.last_ms).toLocaleDateString()+' · '+ago(t.last_ms))}</div>
  <div class="card pad mt12"><div class="legendrow bare">Requests per day</div>${tr?spark(histSeries(tr).b.map(k=>k.n),'var(--c1)'):checking()}</div></div>
  <div><h4>Models used <span class="faint">${arr(t.models).length}</span></h4><div class="card t">${table('d-hm',mcols,arr(t.models),{empty:'No models'})}</div>${pricesLine(HT.meta)}</div>`}


const PAGES={};
/* ---------- shell: nav, crumbs, router card ---------- */
const NAV=[
 ['Monitor',[['overview','Overview'],['sessions','Sessions'],['agents','Agents'],['requests','Requests'],['errors','Errors']]],
 ['Usage',[['models','Models'],['providers','Providers'],['usage','Usage']]],
 ['Workspace',[['projects','Projects'],['worktrees','Worktrees'],['jobs','Jobs']]],
 ['System',[['harnesses','Harnesses'],['router','Router']]]
];
const bdg=(txt,title,cls)=>`<span class="badge${cls?' '+cls:''}" title="${title}">${txt}</span>`;
function badge(k){switch(k){
 case 'sessions':return bdg(SESS.length,'sessions in the last 24 hours');
 case 'agents':return bdg(AGENTS.length,'main agents and subagents','acc');
 case 'requests':return bdg(rate(sum(PS.slice(-10))/10)+'/s','requests per second, last 10 seconds');
 case 'errors':{const e=stats().done.filter(r=>r.status!==200).length;return e?bdg(e,'failed and cancelled requests in the last 15 minutes','bad'):''}
 case 'models':return bdg(MODELS.length,'models used in the last 15 minutes');
 case 'providers':{const n=PL.rows.filter(r=>r.set_up===true).length;return PL.rows.length?bdg(`${n?`<span class="good">${n}</span>`:n}/${PL.rows.length}`,'providers set up of all providers'):''}
 case 'projects':return bdg(projects().length,'projects');
 case 'worktrees':return bdg(WT.length,'worktrees and checkouts');
 case 'jobs':return bdg(JOBS.length,'delegated jobs');
 case 'harnesses':{const n=HARN.filter(h=>h.st==='ready').length;return HARN.length?bdg(`${n?`<span class="good">${n}</span>`:n}/${HARN.length}`,'harnesses ready of all harnesses'):''}
 default:return ''}}
function navHtml(){return NAV.map(([g,items])=>`<div><div class="gl">${g}</div>${items.map(([k,l])=>`<a href="#${k}"${k===page?' aria-current="page"':''}>${ic(k)}${l}${badge(k)}</a>`).join('')}</div>`).join('')}
const dashHost=()=>arr(SNAP.hosts)[0]||location.host||'huddle router';
const dashUrl=()=>arr(SNAP.hosts)[0]||location.host;
function rcard(){return `<div class="r1">${dot(LOST?'bad':'ok',!LOST)}Router ${LOST?'down':'up'}${location.port?`<span class="chip">:${esc(location.port)}</span>`:''}</div><div class="r2"><div><span>Uptime</span><b>${upCell().replace(/ \d+s$/,'')}</b></div><div><span>CPU</span><b>${cpuCell()}</b></div><div><span>Memory</span><b>${mb(RT.rss)}</b></div></div>`}
function shell(){patch($('#nav'),navHtml());patch($('#rcard'),rcard());$('#clock').textContent=clock(NOW);
 $('#vchip').textContent=SNAP.version?'v'+SNAP.version:'';
 const hb=$('#hostbtn');hb.dataset.copy=dashUrl();patch(hb,`${dot(LOST?'bad':'ok',!LOST)}<span><span class="t blk">${esc(dashHost())}</span><span class="s">${location.host?'→ '+esc(location.host):'router'}</span></span>`);
 $('#livew').textContent=LOST?'Reconnecting':'Live';
 const lp=$('#livedot');lp.className='dot '+(LOST?'bad':'ok pulse')}

/* ---------- routing ---------- */
let page='overview';
function render(){REG={};let h=(location.hash||'#overview').slice(1).split('?')[0];h=h==='plugins'?'providers':h;page=PAGES[h]?h:'overview';const p=PAGES[page];
 if(page==='requests'||page==='errors')hload(page);else HS.mode='live';
 const grp=NAV.find(([,its])=>its.some(([k])=>k===page))[0];
 patch($('#crumbs'),`<span class="c0">Huddle</span>${ic('chev')}<span>${grp}</span>${ic('chev')}<b>${p.title}</b>`);
 setHtml($('#content'),p.body()+(readsMemory()?`<div class="foot">${dot('ok',true)} Memory only. Restarting the router clears this page.</div>`:''));
 if(p.after)p.after();shell();closeSidebar();window.scrollTo(0,0);loadTotals()}
window.addEventListener('hashchange',render);

/* ---------- interactions ---------- */
function toast(t){const el=$('#toast');setHtml(el,ic('check')+esc(t));el.classList.add('on');clearTimeout(toast.h);toast.h=setTimeout(()=>el.classList.remove('on'),1400)}
function copy(t){const done=()=>toast('Copied '+(t.length>36?t.slice(0,34)+'…':t));try{navigator.clipboard.writeText(t).then(done,()=>toast('Select and copy: '+t))}catch(e){toast('Select and copy: '+t)}}
document.addEventListener('click',e=>{
 const c=e.target.closest('[data-copy]');if(c){e.stopPropagation();copy(c.dataset.copy);return}
 if(e.target.closest('[data-close]')){closeDrawer();return}
 const so=e.target.closest('[data-sort]');if(so){const[id,k]=so.dataset.sort.split(':');const cur=SORT[id];SORT[id]={k,d:cur&&cur.k===k?-cur.d:(so.closest('th').classList.contains('num')?-1:1)};refresh();if(DR)renderDrawer(true);return}
 const sg=e.target.closest('[data-seg]');if(sg){const[id,k]=sg.dataset.seg.split(':');if(id==='range'){setRange(k);return}if(id==='hmode'){location.hash='#'+page+(k==='history'?'?mode=history':'');return}F[id]=k;$$('[data-seg]').filter(b=>b.dataset.seg.startsWith(id+':')).forEach(b=>b.setAttribute('aria-pressed',String(b===sg)));refresh();return}
 const hp=e.target.closest('[data-hp]');if(hp){HS.off=Math.max(0,HS.off+(hp.dataset.hp==='next'?HPAGE:-HPAGE));hsearch();return}
 const mt=e.target.closest('[data-metric]');if(mt){CH.metric=mt.dataset.metric;refresh();drawChart();return}
 const fo=e.target.closest('[data-focus]');if(fo){CH.focus=CH.focus===fo.dataset.focus?null:fo.dataset.focus;refresh();drawChart();return}
 const op=e.target.closest('[data-open]');if(op){openDrawer(op.dataset.open);return}
 const th=e.target.closest('[data-theme-set]');if(th){setTheme(th.dataset.themeSet);return}
});
document.addEventListener('input',e=>{const q=e.target.closest('[data-q]');if(q){F.q[q.dataset.q]=q.value;refresh()}
 const hf=e.target.closest('[data-hf]');if(hf){const k=hf.dataset.hf;if(k==='range')HS.range=hf.value;else HS.f[k]=hf.value;HS.off=0;clearTimeout(HS.t);HS.t=setTimeout(hsearch,300)}});
$('#scrim').onclick=()=>{closeDrawer();closeSidebar()};
$('#menu').onclick=()=>{$('#sb').classList.add('on');$('#scrim').classList.add('on')};
function closeSidebar(){$('#sb').classList.remove('on');if(!DR)$('#scrim').classList.remove('on')}
function setTheme(t){if(t==='system')document.documentElement.removeAttribute('data-theme');else document.documentElement.setAttribute('data-theme',t);$$('[data-theme-set]').forEach(b=>b.setAttribute('aria-pressed',String(b.dataset.themeSet===t)));try{localStorage.setItem('huddle-admin-theme',t)}catch(e){}}

/* command palette */
const PAL={i:0,items:[]};
function palItems(q){const L=[];NAV.forEach(([g,its])=>its.forEach(([k,l])=>L.push({g:'Pages',l,ic:k,ty:g,go:()=>{location.hash=k}})));
 SESS.forEach(s=>L.push({g:'Sessions',l:s.name,ic:'sessions',ty:s.project+' · '+s.sid,go:()=>{if(page!=='sessions')location.hash='sessions';openDrawer('session:'+s.id)}}));
 projects().forEach(p=>L.push({g:'Projects',l:p.name,ic:'projects',ty:tilde(p.path),go:()=>{if(page!=='projects')location.hash='projects';openDrawer('project:'+p.name)}}));
 MODELS.forEach(m=>L.push({g:'Models',l:m.id,ic:'models',ty:m.lane,go:()=>{location.hash='models'}}));
 [['system','Theme: match the system','monitor'],['light','Theme: light','sun'],['dark','Theme: dark','moon']].forEach(([k,l,i])=>L.push({g:'Actions',l,ic:i,ty:'',go:()=>setTheme(k)}));
 L.push({g:'Actions',l:'Copy the dashboard address',ic:'copy',ty:dashHost(),go:()=>copy(dashUrl())});
 q=q.trim().toLowerCase();return q?L.filter(x=>(x.l+' '+x.ty+' '+x.g).toLowerCase().includes(q)):L}
function palRender(){const q=$('#palq').value;PAL.items=palItems(q);if(PAL.i>=PAL.items.length)PAL.i=0;let g='',h='';
 PAL.items.forEach((x,i)=>{if(x.g!==g){g=x.g;h+=`<div class="pgl">${esc(g)}</div>`}h+=`<div class="pi" role="option" data-pi="${i}" aria-selected="${i===PAL.i}">${ic(x.ic)}<span class="l">${esc(x.l)}</span><span class="ty">${esc(x.ty)}</span></div>`});
 setHtml($('#pallist'),h||'<div class="pi faint">No matches</div>');const sel=$('#pallist [aria-selected="true"]');if(sel)sel.scrollIntoView({block:'nearest'})}
function palOpen(){$('#pal').classList.add('on');$('#palq').value='';PAL.i=0;palRender();setTimeout(()=>$('#palq').focus(),0)}
function palClose(){$('#pal').classList.remove('on')}
function palGo(i){const x=PAL.items[i];palClose();if(x)x.go()}
$('#openPal').onclick=palOpen;$('#openPal2').onclick=palOpen;
$('#palq').addEventListener('input',()=>{PAL.i=0;palRender()});
$('#pallist').addEventListener('click',e=>{const p=e.target.closest('[data-pi]');if(p)palGo(+p.dataset.pi)});
$('#pallist').addEventListener('mousemove',e=>{const p=e.target.closest('[data-pi]');if(p&&+p.dataset.pi!==PAL.i){PAL.i=+p.dataset.pi;$$('#pallist [data-pi]').forEach(x=>x.setAttribute('aria-selected',String(+x.dataset.pi===PAL.i)))}});
$('#pal').addEventListener('click',e=>{if(e.target.id==='pal')palClose()});
document.addEventListener('keydown',e=>{
 const palOn=$('#pal').classList.contains('on');
 if((e.metaKey||e.ctrlKey)&&e.key.toLowerCase()==='k'){e.preventDefault();palOn?palClose():palOpen();return}
 if(palOn){if(e.key==='Escape'){palClose();return}if(e.key==='ArrowDown'){e.preventDefault();PAL.i=Math.min(PAL.items.length-1,PAL.i+1);palRender();return}if(e.key==='ArrowUp'){e.preventDefault();PAL.i=Math.max(0,PAL.i-1);palRender();return}if(e.key==='Enter'){e.preventDefault();palGo(PAL.i);return}return}
 if(e.key==='Escape'){if(DR)closeDrawer();closeSidebar();return}
 if(DR&&e.key==='Tab'){trapTab(e);return}
 if((e.key==='Enter'||e.key===' ')&&e.target.matches&&e.target.matches('tr[data-open]')){e.preventDefault();openDrawer(e.target.dataset.open);return}
 if(e.key==='/'&&!/input|textarea/i.test(document.activeElement.tagName)){e.preventDefault();palOpen()}
});
window.addEventListener('resize',()=>{if(page==='overview')drawChart()});

/* ---------- live: a feed while it holds, polling while it does not ---------- */
function paint(){refresh();shell();if(page==='overview')drawChart();if(DR)renderDrawer(true)}
let first=true;
function update(s){build(s);if(first){first=false;render()}else paint()}
function onSnapshot(s){const n=num(s&&s.now_ms);if(n!=null)OFFSET=n-Date.now();LOST=false;update(s)}
let poll=null;
function getState(){fetch('/huddle-router/state',{cache:'no-store'}).then(r=>r.json()).then(onSnapshot,()=>{LOST=true;update(SNAP)})}
function startPoll(){if(poll===null){getState();poll=setInterval(getState,2000)}}
function stopPoll(){if(poll!==null){clearInterval(poll);poll=null}}
