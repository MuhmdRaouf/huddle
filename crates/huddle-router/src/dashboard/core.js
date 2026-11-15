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
/* tz:start */
/* every time on the page is read in the zone the header switch names (Local or UTC) */
const MON=['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'],p2=n=>String(n).padStart(2,'0');
const TZ={utc:false};
try{TZ.utc=localStorage.getItem('huddle-tz')==='utc'}catch(e){}
function tzSet(utc){TZ.utc=utc;try{localStorage.setItem('huddle-tz',utc?'utc':'local')}catch(e){}}
const tzOff=t=>TZ.utc?0:-new Date(t).getTimezoneOffset();
const tzSign=m=>`UTC${m<0?'-':'+'}${Math.floor(Math.abs(m)/60)}${Math.abs(m)%60?':'+p2(Math.abs(m)%60):''}`;
const tzLocal=()=>`Local (${tzSign(-new Date().getTimezoneOffset())})`;
const tzLabel=()=>TZ.utc?'UTC':tzLocal();
const tzQuery=()=>'tz_offset_min='+tzOff(Date.now());
/* a Date moved by the zone's offset, read with the getUTC* accessors */
const zd=t=>new Date(t+tzOff(t)*6e4);
/* the instant of a wall-clock time in the zone */
const zmake=(y,m,d,h,mi)=>{const w=Date.UTC(y,m,d,h||0,mi||0),g=w-tzOff(w)*6e4;return w-tzOff(g)*6e4};
const clock=t=>{if(num(t)==null)return '—';const d=zd(t);return `${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}:${p2(d.getUTCSeconds())}`};
const hm=t=>clock(t).slice(0,5);
const when=t=>{const d=zd(t);return `${d.getUTCDate()} ${MON[d.getUTCMonth()]} ${p2(d.getUTCHours())}:${p2(d.getUTCMinutes())}`};
const dayText=t=>{const d=zd(t);return `${d.getUTCDate()} ${MON[d.getUTCMonth()]} ${d.getUTCFullYear()}`};
const dtText=t=>dayText(t)+', '+hm(t);
const shortDay=t=>{const d=zd(t);return `${MON[d.getUTCMonth()]} ${d.getUTCDate()}`};
const startOfDay=t=>{const d=zd(t);return zmake(d.getUTCFullYear(),d.getUTCMonth(),d.getUTCDate())};
/* tz:end */
const msf=v=>num(v)==null?'—':v>=1e4?(v/1e3).toFixed(1)+' s':Math.round(v)+' ms';
const mb=b=>num(b)==null?'—':(b/1e6).toFixed(1)+' MB';

/* ---------- icons ---------- */
const ICONS={
 overview:'<rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/>',
 'needs-you':'<path d="M6 8a6 6 0 0 1 12 0c0 7 3 8 3 8H3s3-1 3-8"/><path d="M10.3 21a1.7 1.7 0 0 0 3.4 0"/>',
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
let SESS=[],AGENTS=[],REQS=[],MODELS=[],MN=[],COLOR={},LANES=[],JOBS=[],JOBS_DONE=[],WT=[],HARN=[],BK=[],PS=[],BW=3e4,PLAN=null,PL={state:'checking',rows:[],checks:[]},WIRE={state:'checking'},RT={},ST=null;
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
  const o={id:String(x.id||''),title:x.title||'',project:x.project||'',repo:x.repo||'',worktree:x.worktree||'',cwd:x.cwd||'',branch:x.branch||'',model:x.model||'',state:x.state||'idle',started:num(x.started_ms),last:num(x.last_ms),waitingSince:num(x.state_since_ms),openTasks:num(x.open_tasks),questionTool:x.question_tool||'',ctxTok:tok,ctxLim:lim,ctx:tok!=null&&lim?tok/lim*100:null,ttl:num(x.cache_ttl_ms),lastReq:num(x.last_request_ms)};
  o.sid=o.id.slice(0,8);o.name=o.title||o.project||o.sid||'Session';
  let ag=arr(x.agents).map(a=>({id:String(a.id||''),type:a.type||'',desc:a.description||'',model:a.model||'',state:a.state||'idle',start:num(a.started_ms),end:num(a.ended_ms),wt:a.worktree||null,s:o}));
  let m=ag.find(a=>a.id==='main');ag=ag.filter(a=>a!==m);
  if(!m)m={id:'main',type:'main',desc:'',model:o.model,state:'idle',start:o.started,end:null,wt:null,s:o};
  m.desc=o.name;if(!m.model)m.model=o.model;
  o.agents=[m].concat(ag);
  /* open background tasks are one row each session: the session log carries a count, not the tasks */
  o.bg=o.openTasks>0?[{id:'tasks',type:'background',desc:o.openTasks+(o.openTasks===1?' background task':' background tasks'),model:o.model,state:'running',start:null,end:null,wt:null,s:o}]:[];
  return o});
 AGENTS=SESS.flatMap(s=>s.agents.concat(s.bg));
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
/* what the server counted over the recent rows, present with or without the rows */
const RCNT=()=>SNAP.recent_counts&&typeof SNAP.recent_counts==='object'?SNAP.recent_counts:{};
/* a session's requests (finished and streaming), errors and tokens */
const sCount=s=>{const c=(RCNT().by_session||{})[s.id]||{};return {n:n0(c.requests)+sReq(s).filter(r=>r.status==null).length,err:n0(c.errors),tin:n0(c.tokens_in),cr:n0(c.cache_read),tout:n0(c.tokens_out)}};
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
 const jr=arr(SNAP.jobs).map(j=>({id:j.id||'',h:j.harness||'',task:j.title||'',repo:j.repo||'',state:j.state||'',started:num(j.started_ms),end:num(j.ended_ms),files:num(j.files),add:num(j.add),del:num(j.del),why:j.why||null}));
 JOBS=jr.filter(j=>!JOB_ENDED.includes(j.state));JOBS_DONE=jr.filter(j=>JOB_ENDED.includes(j.state));
 WT=arr(SNAP.worktrees).map(w=>({path:w.path||'',project:w.project||'',repo:w.repo||'',branch:w.branch||'',head:w.head||'',kind:w.kind||'checkout',by:w.used_by||null,changes:num(w.changes),commit:num(w.last_commit_ms)}));
 WT.forEach(w=>{w.stale=w.kind==='agent'&&!w.by&&w.commit!=null&&NOW-w.commit>=5*DAY});
 const pg=SNAP.plugins&&typeof SNAP.plugins==='object'?SNAP.plugins:{};
 PL={state:pg.state==='ready'?'ready':'checking',rows:arr(pg.rows),checks:arr(pg.checks)};
 const hs=SNAP.harnesses&&typeof SNAP.harnesses==='object'?SNAP.harnesses:{};
 HARN=arr(hs.rows).map(h=>({id:h.id||'',name:h.name||h.id||'',st:h.status||'missing',bin:h.bin||'',ver:h.version||'',flags:h.flags||'',report:h.report||'',why:h.why||'',access:arr(h.access),jobs:JOBS.filter(j=>j.h===h.id).length,ready:hs.state==='ready'}));
 HARN.state=hs.state==='ready'?'ready':'checking';HARN.checked=num(hs.checked_ms);
 WIRE=SNAP.wiring&&typeof SNAP.wiring==='object'?SNAP.wiring:{};
 const r=SNAP.router&&typeof SNAP.router==='object'?SNAP.router:{};
 RT={cpu:num(r.cpu_pct),rss:num(r.rss_bytes),viewers:num(r.viewers),conns:num(r.connections),events:arr(r.events),path:r.path&&typeof r.path==='object'?r.path:{},sup:r.supervision&&typeof r.supervision==='object'?r.supervision:null,phases:r.path&&r.path.by_phase&&typeof r.path.by_phase==='object'?r.path.by_phase:{}};
}
const JOB_ENDED=['completed','failed','accepted','cancelled'],ACTIVE_STATES=['needs_you','working','background'];
/* live jobs, plus finished ones that ended inside the global time range */
function jobsInRange(){const to=HIST.range==='custom'?HIST.to:NOW,from=HIST.range==='custom'?HIST.from:NOW-RANGE_MIN[HIST.range]*6e4;
 return JOBS.concat(JOBS_DONE.filter(j=>{const e=j.end||j.started||0;return e>=from&&e<=to}))}
/* one project per git repository: its sessions, worktrees and jobs roll up under the repo key */
function projects(){
 const P={},get=(k,n)=>P[k]||(P[k]={key:k,name:n,ss:[],wts:[],jobs:[]});
 SESS.forEach(s=>{if(s.project)get(s.repo||s.project,s.project).ss.push(s)});
 WT.forEach(w=>{if(w.project)get(w.repo||w.project,w.project).wts.push(w)});
 const byName={};Object.values(P).forEach(p=>{byName[p.name]=byName[p.name]||p});
 jobsInRange().forEach(j=>{if(j.repo)(byName[j.repo]||get(j.repo,j.repo)).jobs.push(j)});
 return Object.values(P).map(({key,name,ss,wts,jobs})=>{const cs=ss.map(sCount);
  const home=wts.find(w=>w.kind==='checkout')||wts[0];
  return {key,name,path:key.startsWith('/')?key:home?home.path:ss[0]?ss[0].cwd:'',sessions:ss,live:ss.filter(s=>ACTIVE_STATES.includes(s.state)).length,
   agentsRun:ss.flatMap(s=>s.agents).filter(a=>a.type!=='main'&&a.state==='running').length,wts,jobs,reqs:sum(cs.map(c=>c.n)),tin:sum(cs.map(c=>c.tin)),tout:sum(cs.map(c=>c.tout)),
   stale:wts.filter(w=>w.stale).length,
   branches:[...new Set(wts.map(w=>w.branch).filter(Boolean))],last:Math.max(0,...ss.map(lastT),...jobs.map(j=>j.end||j.started||0),...wts.map(w=>w.commit||0))||null}});
}

const FAIL_RATE=0.2;
/* Live facts decide the status, in this order: quota, error rate, wiring. A provider with traffic is never Not set up. */
function provStatus(x){
 if(x.quotaOut)return ['bad','Out of quota'];
 if(x.id==='anthropic'&&RT.path.state==='failing')return ['bad','Failing'];
 if(x.n&&x.err/x.n>FAIL_RATE)return ['bad','Failing'];
 if(!x.built&&(x.n||x.laneOn)&&!x.key)return ['bad','Needs a fix'];
 if(!x.built&&x.key&&!x.laneOn)return ['bad','Needs a fix'];
 return x.built||x.laneOn?['ok','Working']:['off','Not set up'];
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
const RANGES=[['15m','15 min','last 15 minutes'],['1h','1 hour','last hour'],['24h','24 hours','last 24 hours'],['7d','7 days','last 7 days'],['30d','30 days','last 30 days'],['90d','90 days','last 90 days']],RANGE_MIN={'15m':15,'1h':60,'24h':1440,'7d':10080,'30d':43200,'90d':129600};
const FLAT='Claude plan usage is flat-rate; this is what the same tokens cost on the API';
const HIST={range:'15m',from:0,to:0,keep:90,picking:false,bk:null,tot:null,ser:null,err:false},HCOLOR={};
try{const r=localStorage.getItem('huddle-range');if(r==='custom'){const c=JSON.parse(localStorage.getItem('huddle-range-custom')||'null');if(c&&c.from<c.to){HIST.range='custom';HIST.from=c.from;HIST.to=c.to}}else if(RANGES.some(x=>x[0]===r))HIST.range=r}catch(e){}
const liveRange=()=>HIST.range==='15m';
/* Only pages that read the router's live memory say a restart clears them; the rest read disk or settings. */
const readsMemory=()=>({overview:liveRange(),models:liveRange(),usage:liveRange(),sessions:true,agents:true,requests:HS.mode==='live',errors:liveRange()})[page]===true;
const rangeShort=()=>HIST.range==='custom'?'custom':RANGES.find(x=>x[0]===HIST.range)[1];
const rangeText=()=>HIST.range==='custom'?when(HIST.from)+' to '+when(HIST.to):RANGES.find(x=>x[0]===HIST.range)[2];
function rangeQuery(){return (HIST.range==='custom'?'from='+HIST.from+'&to='+HIST.to:'range='+HIST.range)+'&'+tzQuery()}
const usd=v=>num(v)==null?'—':'$'+(v>0&&v<0.01?v.toFixed(4):v.toFixed(2));
const noPrice='<span class="faint">no price</span>';
const costText=(cost,priced)=>priced===false?noPrice:`<span class="mono">${usd(cost)}</span>`;
const SER0={b:[0,1].map(i=>Object.assign({byM:{},t:i,w:0},EMPTY)),mn:[],bw:3e4};
const ser=()=>liveRange()?{b:BK,mn:MN,bw:BW}:(HIST.ser||SER0);
const rangeMin=()=>HIST.range==='custom'?Math.max(1,(HIST.to-HIST.from)/6e4):RANGE_MIN[HIST.range];
const longBuckets=()=>ser().bw>=72e5;
const tickLabel=t=>longBuckets()?shortDay(t):clock(t).slice(0,5);
function histSeries(d){const w=Math.max(1,num(d.width_ms)||3e5),s0=Math.floor(d.from_ms/w)*w,N=Math.max(2,Math.floor((d.to_ms-s0)/w)+1);
 const b=Array.from({length:N},(_,i)=>Object.assign({byM:{},t:s0+i*w,w:0},EMPTY)),names=new Set();
 arr(d.buckets).forEach(x=>{const k=b[Math.min(N-1,Math.max(0,Math.floor((x.start_ms-s0)/w)))],o={n:n0(x.n),err:n0(x.err),tok:n0(x.tok_in)+n0(x.tok_out),p50:n0(x.ttft_p50),cost:n0(x.cost_usd)};
  k.byM[x.model]=o;k.n+=o.n;k.err+=o.err;k.tok+=o.tok;k.cost+=o.cost;k.w+=o.p50*o.n;names.add(x.model)});
 b.forEach(k=>{k.p50=k.n?k.w/k.n:0});return {b,mn:[...names].filter(Boolean).sort(),bw:w}}
/* per-model rows for a history range: totals carry counts, cost and the latency of their own rows */
function histRows(){const ts=arr(HIST.tot&&HIST.tot.totals),all=Math.max(1,sum(ts.map(t=>n0(t.n))));
 return ts.map(t=>({id:t.key,n:n0(t.n),err:n0(t.err),...tokSplit(t.tok_in,t.cache_read,t.cache_write),out:n0(t.tok_out),p50:num(t.ttft_p50),p95:num(t.ttft_p95),cost:num(t.cost_usd),priced:t.priced!==false,share:n0(t.n)/all}))}
const RM=()=>liveRange()?MODELS:histRows();
const HIDE_IN_RANGE=['lane','trend','speed','sess','last'];
const COSTCOL={k:'cost',label:'API price',num:1,v:x=>x.cost||0,h:x=>costText(x.cost,x.priced)};
const rangeCols=cols=>liveRange()?cols:cols.filter(c=>!HIDE_IN_RANGE.includes(c.k)).concat([COSTCOL]);
const rangeRows=()=>liveRange()?modelRows():histRows();
const rangeEmpty=()=>HIST.bk||HIST.err||liveRange()?`No requests in ${rangeText()}`:'Checking…';
const clampNote=d=>d&&d.clamped_from_ms?`<div class="legendrow">Showing from ${when(d.clamped_from_ms)}: huddle keeps ${HIST.keep} days of history.</div>`:'';
const rangeStatus=()=>liveRange()?'':HIST.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:HIST.bk?clampNote(HIST.bk):`<div class="legendrow">${checking()}</div>`;
/* one request per endpoint in flight: a second ask for the same URL shares the first answer */
const HFLY=new Map();
const hget=u=>{if(HFLY.has(u))return HFLY.get(u);const p=fetch(u,{cache:'no-store'}).then(x=>{if(!x.ok)throw new Error(String(x.status));return x.json()}).finally(()=>HFLY.delete(u));HFLY.set(u,p);return p};
function loadHistory(){if(liveRange())return;const get=hget;
 const q=rangeQuery();
 Promise.all([get('/huddle-router/history?view=buckets&'+rangeQuery()),get('/huddle-router/history?view=totals&by=model&'+rangeQuery())]).then(([bk,tot])=>{if(rangeQuery()!==q)return;
  HIST.bk=bk;HIST.tot=tot;HIST.ser=histSeries(bk);HIST.err=false;HIST.ser.mn.forEach((m,i)=>{HCOLOR[m]='var(--c'+(i%6+1)+')'});paint()},()=>{if(rangeQuery()!==q)return;HIST.err=true;paint()})}
/* ---------- the page's own menu: a button and a listbox popover ---------- */
const MENUS={};let MENU=null,MTYPE={s:'',t:0};
const CHEVRON='<svg class="mchev" viewBox="0 0 16 16" aria-hidden="true"><path d="M4 6l4 4 4-4" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"/></svg>';
const menuCur=id=>{const m=MENUS[id],o=m.opts(),k=m.cur();return o.find(x=>x.k===k)||o[0]};
function menuHtml(id,cls){const m=MENUS[id];
 return `<div class="pmenu${cls?' '+cls:''}" data-menu="${id}"><button type="button" class="mbtn" id="mb-${id}" data-mbtn="${id}" aria-haspopup="listbox" aria-expanded="false" aria-label="${esc(m.label)}"><span class="mval">${esc(menuCur(id).t)}</span>${CHEVRON}</button></div>`}
function menuLabel(id){const b=$('#mb-'+id);if(b)b.querySelector('.mval').textContent=menuCur(id).t}
function menuDrop(){if(!MENU)return;const l=MENU.list;MENU=null;l.classList.remove('on');const b=$('#mb-'+l.dataset.for);if(b)b.setAttribute('aria-expanded','false');setTimeout(()=>l.remove(),REDUCED()?0:140)}
const REDUCED=()=>window.matchMedia&&matchMedia('(prefers-reduced-motion: reduce)').matches;
function closeMenu(focus){const id=MENU&&MENU.id;menuDrop();if(focus&&id){const b=$('#mb-'+id);if(b)b.focus()}}
function menuActive(i){if(!MENU)return;const rows=MENU.rows;let n=i;while(rows[n]&&rows[n].dataset.off)n+=i>=MENU.i?1:-1;if(!rows[n])return;
 rows.forEach((r,j)=>r.classList.toggle('act',j===n));MENU.i=n;MENU.list.setAttribute('aria-activedescendant',rows[n].id);rows[n].scrollIntoView({block:'nearest'})}
function openMenu(id){if(MENU){const same=MENU.id===id;closeMenu(false);if(same)return}
 const btn=$('#mb-'+id);if(!btn)return;const m=MENUS[id],cur=m.cur(),box=btn.parentNode;
 const list=document.createElement('ul');list.className='mlist';list.setAttribute('role','listbox');list.setAttribute('tabindex','-1');list.setAttribute('aria-label',m.label);list.dataset.for=id;
 setHtml(list,m.opts().map((o,i)=>`<li role="option" id="mo-${id}-${i}" data-mk="${esc(o.k)}" aria-selected="${o.k===cur}"${o.sep?' class="sep"':''}${o.off?' aria-disabled="true" data-off="1"':''}>${esc(o.t)}<svg class="mtick" viewBox="0 0 16 16" aria-hidden="true"><path d="M3.5 8.5l3 3 6-7" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round"/></svg></li>`).join(''));
 box.appendChild(list);btn.setAttribute('aria-expanded','true');
 const rows=[...list.children];MENU={id,list,rows,i:0};
 const r=list.getBoundingClientRect();
 if(r.bottom>innerHeight-8&&btn.getBoundingClientRect().top>r.height+16)list.classList.add('up');
 if(r.right>innerWidth-8)list.classList.add('left');
 void list.offsetWidth;list.classList.add('on');
 menuActive(Math.max(0,m.opts().findIndex(o=>o.k===cur)));list.focus({preventScroll:true})}
function menuPick(i){const m=MENU;if(!m)return;const row=m.rows[i];if(!row||row.dataset.off)return;const id=m.id,k=row.dataset.mk;closeMenu(true);MENUS[id].pick(k);menuLabel(id)}
/* typing a letter jumps to the next option starting with it */
function menuType(ch){const now=Date.now();MTYPE.s=now-MTYPE.t>700?ch:MTYPE.s+ch;MTYPE.t=now;const rows=MENU.rows,n=rows.length;
 for(let j=1;j<=n;j++){const i=(MENU.i+(MTYPE.s.length>1?0:j))%n;if(!rows[i].dataset.off&&rows[i].textContent.toLowerCase().startsWith(MTYPE.s)){menuActive(i);return}}}
function menuKeys(e){
 const b=e.target.closest&&e.target.closest('[data-mbtn]');
 if(b&&!MENU&&(e.key==='ArrowDown'||e.key==='ArrowUp')){e.preventDefault();e.stopPropagation();openMenu(b.dataset.mbtn);return}
 if(!MENU)return;const k=e.key,last=MENU.rows.length-1;
 const eat=()=>{e.preventDefault();e.stopPropagation()};
 if(k==='ArrowDown'){eat();menuActive(Math.min(last,MENU.i+1))}
 else if(k==='ArrowUp'){eat();menuActive(Math.max(0,MENU.i-1))}
 else if(k==='Home'){eat();menuActive(0)}
 else if(k==='End'){eat();menuActive(last)}
 else if(k==='Enter'||k===' '){eat();menuPick(MENU.i)}
 else if(k==='Escape'){eat();closeMenu(true)}
 else if(k==='Tab'){menuDrop()}
 else if(k.length===1&&!e.metaKey&&!e.ctrlKey&&!e.altKey){eat();menuType(k.toLowerCase())}}
document.addEventListener('keydown',menuKeys,true);
document.addEventListener('mousemove',e=>{const o=MENU&&e.target.closest&&e.target.closest('[data-mk]');if(o&&MENU.list.contains(o)){const i=MENU.rows.indexOf(o);if(i!==MENU.i&&!o.dataset.off)menuActive(i)}});
/* the one range control: a preset or a custom from-to, kept in the top bar */
const earliest=()=>Date.now()-HIST.keep*864e5;
MENUS.range={label:'Time range',
 opts:()=>RANGES.map(([k,s])=>({k,t:s,off:RANGE_MIN[k]/1440>HIST.keep})).concat([{k:'custom',t:'Custom',sep:1}]),
 cur:()=>HIST.picking?'custom':HIST.range,
 pick:k=>{if(k==='custom'){HIST.picking=true;initPick();renderRangeBar();const n=$('#rfrom');if(n)n.focus()}else{HIST.picking=false;if(k===HIST.range)renderRangeBar();else setRange(k)}}};
/* the own date and time picker for the Custom range: two fields, one month grid, hour and minute menus */
const DT={from:0,to:0,act:'from',y:0,m:0},MONTHS=['January','February','March','April','May','June','July','August','September','October','November','December'];
function dtView(){const d=zd(DT[DT.act]);DT.y=d.getUTCFullYear();DT.m=d.getUTCMonth()}
function initPick(){const f=HIST.range==='custom'?HIST.from:Date.now()-864e5,t=HIST.range==='custom'?HIST.to:Date.now();DT.from=Math.max(f,earliest());DT.to=t;DT.act='from';dtView()}
const dtSet=(f,fn)=>{const d=zd(DT[f]);fn(d);DT[f]=zmake(d.getUTCFullYear(),d.getUTCMonth(),d.getUTCDate(),d.getUTCHours(),d.getUTCMinutes())};
MENUS.dth={label:'Hour',opts:()=>Array.from({length:24},(_,i)=>({k:String(i),t:p2(i)})),cur:()=>String(zd(DT[DT.act]).getUTCHours()),pick:k=>{dtSet(DT.act,d=>d.setUTCHours(+k));redrawPick('#mb-dth')}};
MENUS.dtm={label:'Minute',opts:()=>Array.from({length:60},(_,i)=>({k:String(i),t:p2(i)})),cur:()=>String(zd(DT[DT.act]).getUTCMinutes()),pick:k=>{dtSet(DT.act,d=>d.setUTCMinutes(+k));redrawPick('#mb-dtm')}};
function calHtml(){const lead=(new Date(Date.UTC(DT.y,DT.m,1)).getUTCDay()+6)%7,days=new Date(Date.UTC(DT.y,DT.m+1,0)).getUTCDate(),cur=startOfDay(DT[DT.act]),lo=startOfDay(earliest()),hi=Date.now();
 let g='';for(let i=0;i<lead;i++)g+='<span></span>';
 for(let d=1;d<=days;d++){const s=zmake(DT.y,DT.m,d),off=s<lo||s>hi;g+=`<button type="button" class="day${s===cur?' sel':''}" data-day="${d}" aria-pressed="${s===cur}"${off?' disabled':''}>${d}</button>`}
 return `<div class="calhead"><button type="button" class="calnav" data-cal="-1" aria-label="Previous month">${ic('chev','flip')}</button><b aria-live="polite">${MONTHS[DT.m]} ${DT.y}</b><button type="button" class="calnav" data-cal="1" aria-label="Next month">${ic('chev')}</button></div><div class="calgrid">${['Mo','Tu','We','Th','Fr','Sa','Su'].map(x=>`<i>${x}</i>`).join('')}${g}</div>`}
const dtField=(f,id,l)=>`<button type="button" class="dtf" id="${id}" data-dtf="${f}" aria-pressed="${DT.act===f}"><span class="dtl">${l}</span><span>${esc(dtText(DT[f]))}</span></button>`;
function rangeBar(){
 const pop=HIST.picking?`<div class="rangepop" role="group" aria-label="Custom range"><div class="dtfs">${dtField('from','rfrom','From')}${dtField('to','rto','To')}</div><div class="cal">${calHtml()}</div><div class="dtime"><span>Time</span>${menuHtml('dth','sm')}<span>:</span>${menuHtml('dtm','sm')}</div><div class="rerr" id="rerr" role="alert"></div><div class="racts"><button id="rapply">Apply</button><button id="rcancel">Cancel</button></div></div>`:'';
 return menuHtml('range')+pop}
function redrawPick(focus){const err=($('#rerr')||{}).textContent;renderRangeBar();if(err)setHtml($('#rerr'),esc(err));const n=focus&&$(focus);if(n)n.focus()}
function pickClick(e){
 const f=e.target.closest('[data-dtf]');if(f){DT.act=f.dataset.dtf;dtView();redrawPick('#'+f.id);return true}
 const c=e.target.closest('[data-cal]');if(c){const d=new Date(Date.UTC(DT.y,DT.m+ +c.dataset.cal,1));DT.y=d.getUTCFullYear();DT.m=d.getUTCMonth();redrawPick('[data-cal="'+c.dataset.cal+'"]');return true}
 const d=e.target.closest('[data-day]');if(d){dtSet(DT.act,x=>x.setUTCFullYear(DT.y,DT.m,+d.dataset.day));DT[DT.act]=Math.min(DT[DT.act],Date.now());redrawPick('[data-day="'+d.dataset.day+'"]');return true}
 return false}
/* arrows move across the month grid; Escape leaves the picker */
document.addEventListener('keydown',e=>{
 if(!HIST.picking||MENU)return;const day=e.target.closest&&e.target.closest('[data-day]');
 if(e.key==='Escape'&&e.target.closest&&e.target.closest('.rangepop')){e.preventDefault();e.stopPropagation();HIST.picking=false;renderRangeBar();const b=$('#mb-range');if(b)b.focus();return}
 const step={ArrowLeft:-1,ArrowRight:1,ArrowUp:-7,ArrowDown:7}[e.key];
 if(!day||!step)return;e.preventDefault();e.stopPropagation();
 const d=new Date(Date.UTC(DT.y,DT.m,+day.dataset.day+step));
 if(d.getUTCFullYear()!==DT.y||d.getUTCMonth()!==DT.m){DT.y=d.getUTCFullYear();DT.m=d.getUTCMonth();redrawPick();const n=$('[data-day="'+d.getUTCDate()+'"]');if(n&&!n.disabled)n.focus();return}
 const n=$('[data-day="'+d.getUTCDate()+'"]');if(n&&!n.disabled)n.focus()},true);
function renderRangeBar(){const el=$('#rangebar');if(el){menuDrop();setHtml(el,rangeBar())}}
/* the zone switch: Local (the browser's zone) or UTC; the choice reaches every time and every day bucket */
MENUS.tz={label:'Time zone',opts:()=>[{k:'local',t:tzLocal()},{k:'utc',t:'UTC'}],cur:()=>TZ.utc?'utc':'local',pick:k=>setTz(k==='utc')};
function renderTzBar(){const el=$('#tzbar');if(el){menuDrop();setHtml(el,menuHtml('tz'))}}
function setTz(utc){if(utc===TZ.utc)return;tzSet(utc);renderTzBar();applyRange();if(DR)renderDrawer(true)}
function applyRange(){HIST.bk=HIST.tot=HIST.ser=null;HIST.err=false;HIST.picking=false;CH.focus=null;if(liveRange()&&CH.metric==='cost')CH.metric='req';
 HT.ses=HT.prj=HT.meta=null;HT.trend={};HT.mod={};
 try{localStorage.setItem('huddle-range',HIST.range);if(HIST.range==='custom')localStorage.setItem('huddle-range-custom',JSON.stringify({from:HIST.from,to:HIST.to}))}catch(e){}
 renderRangeBar();render();loadHistory()}
function setRange(k){if(!RANGES.some(r=>r[0]===k))return;HIST.range=k;applyRange()}
function applyCustom(){const f=DT.from,t=Math.min(DT.to,Date.now()),err=m=>{setHtml($('#rerr'),esc(m))};
 if(!(f<t))return err('Pick a start that is before the end.');
 if(f<earliest())return err(`huddle keeps ${HIST.keep} days of history. Pick a start inside it.`);
 HIST.range='custom';HIST.from=f;HIST.to=t;applyRange()}
function loadMeta(){hget('/huddle-router/history?view=meta').then(d=>{const k=num(d.keep_days);if(k>0){HIST.keep=k;if(!HIST.picking)renderRangeBar()}},()=>{})}
const pricesLine=d=>d?`<div class="legendrow">Estimated from list prices. Prices retrieved ${esc(d.prices_retrieved)}; Claude prices retrieved ${esc(d.claude_prices_retrieved)}.</div>`:'';
const priceNote=()=>liveRange()?'':pricesLine(HIST.bk);

/* ---------- 30-day history of sessions and projects ---------- */
const HT={ses:null,prj:null,meta:null,trend:{},mod:{},err:false},HZERO={n:0,err:0,tok_in:0,cache_read:0,cache_write:0,tok_out:0,cost_usd:0,models:[]};
const HQ='/huddle-router/history?view=';
const hmap=d=>{const m={};arr(d&&d.totals).forEach(t=>{m[t.key]=t});return m};
const hOf=(m,k)=>m?m[k]||HZERO:null;
const hCell=(t,f)=>t?f(t):HT.err?NA:checking();
const sheetKey=()=>DR&&(DR.k==='session'||DR.k==='project')?DR:null;
const wantTotals=()=>page==='sessions'||page==='projects'||!!sheetKey();
function loadTotals(){if(!wantTotals())return;const q=sheetKey(),key=q&&q.key;
 Promise.all([hget(HQ+'totals&by=session&models=0&'+rangeQuery()),hget(HQ+'totals&by=project&'+rangeQuery()),q?hget(HQ+'buckets&'+rangeQuery()+'&'+q.k+'='+encodeURIComponent(q.id)):null,q&&q.k==='session'?hget(HQ+'totals&by=session&session='+encodeURIComponent(q.id)+'&'+rangeQuery()):null]).then(([s,p,t,m])=>{
  HT.ses=hmap(s);HT.prj=hmap(p);HT.meta=s;if(t)HT.trend[key]=t;if(m)HT.mod[key]=arr(arr(m.totals)[0]&&m.totals[0].models);HT.err=false;paint()},()=>{HT.err=true;paint()})}
const costCell=t=>costText(t.cost_usd,t.priced);
const histCols=get=>[
 {k:'hreq',get label(){return `Requests (${rangeShort()})`},num:1,sm:1,v:x=>n0((get(x)||HZERO).n),h:x=>hCell(get(x),t=>t.n?fmt(t.n):'<span class="faint">0</span>')},
 {k:'htok',get label(){return `Tokens (${rangeShort()})`},num:1,sm:1,v:x=>n0((get(x)||HZERO).tok_in)+n0((get(x)||HZERO).tok_out),h:x=>hCell(get(x),t=>t.n?`${fmt(t.tok_in)} <span class="faint">/</span> ${fmt(t.tok_out)}`:NA)},
 {k:'hcost',get label(){return `API price (${rangeShort()})`},num:1,v:x=>n0((get(x)||HZERO).cost_usd),h:x=>hCell(get(x),t=>t.n?costCell(t):NA)}];
/* the History block of a session or project sheet: totals, a daily trend and the models used */
function histBlock(key,t){const tr=HT.trend[key],models=key.startsWith('session:')?HT.mod[key]:t&&t.models,head=`<h4>History <span class="faint">${rangeText()}</span></h4>`;
 if(!t)return `<div>${head}<div class="card pad">${HT.err?'<span class="badc">Could not load history. Showing the last data.</span> <button type="button" class="cmd" data-retry>Retry</button>':checking()}</div></div>`;
 if(!t.n)return `<div>${head}<div class="card"><div class="allclear">${ic('info')} No requests in ${rangeText()}</div></div></div>`;
 const tin=n0(t.tok_in),fact=(k,v)=>`<div class="fact"><div class="k">${k}</div><div class="v">${v}</div></div>`;
 const mcols=[
  {k:'model',label:'Model',v:m=>m.model,h:m=>modelTag(m.model)},
  {k:'n',label:'Requests',num:1,v:m=>n0(m.n),h:m=>fmt(m.n)},
  {k:'tok',label:'Tokens in / out',num:1,v:m=>n0(m.tok_in),h:m=>`${fmt(m.tok_in)} <span class="faint">/</span> ${fmt(m.tok_out)}`},
  {k:'cost',label:'Cost',num:1,v:m=>n0(m.cost_usd),h:m=>`<span class="mono">${usd(m.cost_usd)}</span>`}];
 return `<div>${head}<div class="facts">${fact('Requests',fmt(t.n)+' · '+fmt(t.err)+' errors')+fact('Tokens in',fmt(tin))+fact('Tokens out',fmt(t.tok_out))
  +fact('Cache hit rate',tin?Math.round(tokSplit(tin,t.cache_read,t.cache_write).hit*100)+'%':'—')+fact('API price',costCell(t))+fact('First seen',dayText(t.first_ms)+' · '+ago(t.first_ms))+fact('Last seen',dayText(t.last_ms)+' · '+ago(t.last_ms))}</div>
  <div class="card pad mt12"><div class="legendrow bare">Requests over time</div>${tr?spark(histSeries(tr).b.map(k=>k.n),'var(--c1)'):checking()}</div></div>
  <div><h4>Models used <span class="faint">${models?models.length:''}</span></h4><div class="card t">${models?table('d-hm',mcols,models,{empty:'No models'}):'<div class="pad">'+checking()+'</div>'}</div>${pricesLine(HT.meta)}</div>`}


const PAGES={};
/* ---------- shell: nav, crumbs, router card ---------- */
const NAV=[
 ['Monitor',[['overview','Overview'],['needs-you','Needs you'],['sessions','Sessions'],['agents','Agents'],['requests','Requests'],['errors','Errors']]],
 ['Usage',[['models','Models'],['providers','Providers'],['usage','Usage']]],
 ['Workspace',[['projects','Projects'],['worktrees','Worktrees'],['jobs','Jobs']]],
 ['System',[['harnesses','Harnesses'],['router','Router']]]
];
const bdg=(txt,title,cls)=>`<span class="badge${cls?' '+cls:''}" title="${title}">${txt}</span>`;
function badge(k){switch(k){
 case 'needs-you':{const n=needsCount();return n?bdg(n,'things that need you','bad'):''}
 case 'sessions':return bdg(sessShown().length,'sessions in '+rangeText());
 case 'agents':return bdg(AGENTS.length,'main agents and subagents','acc');
 case 'requests':return bdg(rate(sum(PS.slice(-10))/10)+'/s','requests per second, last 10 seconds');
 case 'errors':{const e=n0(RCNT().failed_15m);return e?bdg(e,'failed and cancelled requests in the last 15 minutes','bad'):''}
 case 'models':return bdg(MODELS.length,'models used in the last 15 minutes');
 case 'providers':{const n=provSetUp();return PL.rows.length?bdg(`${n?`<span class="good">${n}</span>`:n}/${provTotal()}`,'providers set up of all providers'):''}
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
function render(){menuDrop();REG={};let h=(location.hash||'#overview').slice(1).split('?')[0];h=h==='plugins'?'providers':h;page=PAGES[h]?h:'overview';const p=PAGES[page];
 if(page==='requests')hload(page);else HS.mode='live';
 const grp=NAV.find(([,its])=>its.some(([k])=>k===page))[0];
 patch($('#crumbs'),`<span class="c0">Huddle</span>${ic('chev')}<span>${grp}</span>${ic('chev')}<b>${p.title}</b>`);
 setHtml($('#content'),p.body()+(readsMemory()?`<div class="foot">${dot('ok',true)} Memory only. Restarting the router clears this page.</div>`:''));
 if(p.after)p.after();shell();closeSidebar();window.scrollTo(0,0);loadTotals();needRecent()}
window.addEventListener('hashchange',render);

/* ---------- interactions ---------- */
function toast(t){const el=$('#toast');setHtml(el,ic('check')+esc(t));el.classList.add('on');clearTimeout(toast.h);toast.h=setTimeout(()=>el.classList.remove('on'),1400)}
function copy(t){const done=()=>toast('Copied '+(t.length>36?t.slice(0,34)+'…':t));try{navigator.clipboard.writeText(t).then(done,()=>toast('Select and copy: '+t))}catch(e){toast('Select and copy: '+t)}}
document.addEventListener('click',e=>{
 const mb=e.target.closest('[data-mbtn]'),mo=e.target.closest('[data-mk]');
 if(MENU&&!(mb||(mo&&MENU.list.contains(mo))))closeMenu(false);
 if(e.target.closest('#refnow')){if(RF.nowDisabled())return;const b=$('#refnow');b.classList.remove('spin');void b.offsetWidth;b.classList.add('spin');RF.now();return}
 if(mb){openMenu(mb.dataset.mbtn);return}
 if(mo&&MENU){menuPick(MENU.rows.indexOf(mo));return}
 if(pickClick(e))return;
 if(e.target.closest('#rapply')){applyCustom();return}
 if(e.target.closest('#rcancel')){HIST.picking=false;renderRangeBar();return}
 const c=e.target.closest('[data-copy]');if(c){e.stopPropagation();copy(c.dataset.copy);return}
 if(e.target.closest('[data-close]')){closeDrawer();return}
 const so=e.target.closest('[data-sort]');if(so){const[id,k]=so.dataset.sort.split(':');const cur=SORT[id];SORT[id]={k,d:cur&&cur.k===k?-cur.d:(so.closest('th').classList.contains('num')?-1:1)};refresh();if(DR)renderDrawer(true);return}
 const sg=e.target.closest('[data-seg]');if(sg){const[id,k]=sg.dataset.seg.split(':');if(id==='hmode'){location.hash='#'+page+(k==='history'?'?mode=history':'');return}F[id]=k;$$('[data-seg]').filter(b=>b.dataset.seg.startsWith(id+':')).forEach(b=>b.setAttribute('aria-pressed',String(b===sg)));refresh();return}
 const rt=e.target.closest('[data-retry]');if(rt){HT.err=false;paint();loadTotals();return}
 const hp=e.target.closest('[data-hp]');if(hp){HS.off=Math.max(0,HS.off+(hp.dataset.hp==='next'?HPAGE:-HPAGE));hsearch();return}
 const mt=e.target.closest('[data-metric]');if(mt){CH.metric=mt.dataset.metric;refresh();drawChart();return}
 const fo=e.target.closest('[data-focus]');if(fo){CH.focus=CH.focus===fo.dataset.focus?null:fo.dataset.focus;refresh();drawChart();return}
 const op=e.target.closest('[data-open]');if(op){openDrawer(op.dataset.open);return}
 const th=e.target.closest('[data-theme-set]');if(th){setTheme(th.dataset.themeSet);return}
});
document.addEventListener('input',e=>{const q=e.target.closest('[data-q]');if(q){F.q[q.dataset.q]=q.value;refresh()}
 const hf=e.target.closest('[data-hf]');if(hf){const k=hf.dataset.hf;HS.f[k]=hf.value;HS.off=0;clearTimeout(HS.t);HS.t=setTimeout(hsearch,300)}});
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

/* ---------- live: the snapshot is fetched on the refresh clock ---------- */
function paint(){refresh();shell();if(page==='overview')drawChart();if(DR)renderDrawer(true)}
let first=true;
function update(s){build(s);loadOvCost();if(first){first=false;render()}else paint()}
function onSnapshot(s){HAVE_RECENT=Array.isArray(s&&s.recent);const n=num(s&&s.now_ms);if(n!=null)OFFSET=n-Date.now();LOST=false;update(s)}
/* a page that shows the rows asks for them at once when the last answer had none */
function needRecent(){if(RF.refit(wantRecent()!==STREAM_FULL))return;if(wantRecent()&&!HAVE_RECENT)getState()}
/* the pages that list the recent rows, and the pages that count them per agent or per request, poll the full snapshot; an open sheet needs them too */
const RECENT_PAGES=['requests','errors','agents','router'];
const wantRecent=()=>RECENT_PAGES.includes(page)||!!DR;
function stateUrl(){return '/huddle-router/state'+(wantRecent()?'':'?recent=0')}
let HAVE_RECENT=false,STREAM_FULL=false;
function getState(){if(document.visibilityState==='hidden')return;return fetch(stateUrl(),{cache:'no-store'}).then(r=>r.json()).then(onSnapshot,()=>{LOST=true;update(SNAP)})}
/* ---------- refresh: one clock for the live snapshot and the history panels ---------- */
/* rf:start */
const RF_CHOICES=[['off','Off',0],['rt','Realtime',1e3],['1s','1s',1e3],['5s','5s',5e3],['10s','10s',1e4],['30s','30s',3e4],['1m','1m',6e4],['5m','5m',3e5],['15m','15m',9e5],['30m','30m',18e5],['1h','1h',36e5]].map(([k,t,ms])=>({k,t,ms}));
const RF_FLOOR=5000,RF_STORE='huddle-refresh',RF_DEFAULT='1s',RF_TRIES=3,RF_RETRY_MAX=1e4,RF_LONG=6e4;
/* The live snapshot is fetched at the chosen interval, history panels at the same interval but never faster than env.floor(): RF_FLOOR, or RF_LONG while the range is 7 days or more.
   Off sets no timer. A hidden tab clears the timers and fetches once when it shows again.
   Realtime holds one event stream instead of the state timer; history panels run every env.floor(). A dropped stream is retried
   with a delay that grows to RF_RETRY_MAX, and after RF_TRIES failed tries the 1 s poll runs until a try succeeds. */
function rfMake(env){
 let key=RF_DEFAULT,timers=[],since=0,busy=false,hidden=false,stream=null,retry=null,poll=null,fails=0,down=false;
 try{const v=env.store.getItem(RF_STORE);if(RF_CHOICES.some(c=>c.k===v))key=v}catch(e){}
 const choice=()=>RF_CHOICES.find(c=>c.k===key);
 const live=()=>key==='rt';
 const told=()=>{if(env.changed)env.changed()};
 const fetchState=()=>{if(busy)return;const p=env.state();if(p&&p.then){busy=true;const done=()=>{busy=false};p.then(done,done)}};
 const floor=()=>env.floor?env.floor():RF_FLOOR;
 /* a history tick that comes before the floor has passed since the last one waits for the next */
 const histTick=ms=>{since+=ms;if(since<floor())return;since=0;env.history(false)};
 const all=manual=>{fetchState();since=0;env.history(manual)};
 const closeStream=()=>{if(stream){const s=stream;stream=null;s.close()}};
 const stopLive=()=>{closeStream();if(retry){env.cancel(retry);retry=null}if(poll){env.cancel(poll);poll=null}fails=0;down=false};
 const connect=()=>{retry=null;closeStream();
  const h=env.open({
   ok(){if(stream!==h)return;fails=0;if(poll){env.cancel(poll);poll=null}if(down){down=false;told()}},
   fail(){if(stream!==h)return;stream=null;h.close();fails++;if(!down){down=true;told()}
    if(fails>=RF_TRIES&&!poll)poll=env.every(fetchState,1e3);
    retry=env.later(connect,Math.min(RF_RETRY_MAX,1e3*2**(fails-1)))}});
  stream=h};
 const disarm=()=>{timers.forEach(env.cancel);timers=[];stopLive()};
 const arm=()=>{disarm();if(hidden||key==='off')return;const ms=choice().ms;
  since=0;
  if(live()){connect();timers=[env.every(()=>histTick(RF_FLOOR),RF_FLOOR)]}
  else{const every=Math.max(ms,RF_FLOOR);timers=[env.every(fetchState,ms),env.every(()=>histTick(every),every)]}};
 return {choices:()=>RF_CHOICES,key:()=>key,start:arm,now(){if(!live())all(true)},
  nowDisabled:live,nowTitle:()=>live()?'Realtime is on':'Refresh now',
  label:()=>choice().t+(live()&&down?' (reconnecting)':''),
  histMs:()=>choice().ms?Math.max(choice().ms,floor()):Infinity,
  /* reopens the stream when the form it carries (slim or full) is not the one the open page needs */
  refit(differs){if(!differs||!live()||hidden||!stream)return false;connect();return true},
  set(k){if(!RF_CHOICES.some(c=>c.k===k))return;key=k;try{env.store.setItem(RF_STORE,k)}catch(e){}arm();told()},
  visibility(visible){if(!visible){hidden=true;disarm();return}if(!hidden)return;hidden=false;if(key!=='off')all(false);arm()}}}
/* rf:end */
/* HMODE: 0 a page opens or repaints, 1 the history timer, 2 refresh now. A panel loaded less than its own age ago is not loaded again. */
let HMODE=0;
const RF_SLACK=50;
/* a range of 7 days or more changes slowly and costs the most to read: its panels refresh every RF_LONG */
const RANGE_LONG_MS=7*864e5;
const histFloor=()=>(HIST.range==='custom'?HIST.to-HIST.from:RANGE_MIN[HIST.range]*6e4)>=RANGE_LONG_MS?RF_LONG:RF_FLOOR;
const rfFresh=at=>{const age=Date.now()-at;return HMODE===2?false:HMODE===1?age<histFloor()-RF_SLACK:age<RF.histMs()};
const pageLoaders=()=>({models:[loadModels],usage:[loadUsage],providers:[loadProviders],errors:[loadProviders,loadErrors],overview:[loadOvCost],requests:[loadReqCost,()=>{if(HS.mode==='history')hsearch()}]}[page]||[]);
function refreshHistory(manual){HMODE=manual?2:1;try{loadHistory();loadTotals();pageLoaders().forEach(f=>f())}finally{HMODE=0}}
/* the event stream carries the same form as /state: the slim one unless the open page shows the rows */
function openStream(cb){STREAM_FULL=wantRecent();const es=new EventSource('/huddle-router/live'+(STREAM_FULL?'':'?recent=0'));
 es.onopen=cb.ok;es.onerror=cb.fail;es.addEventListener('snapshot',e=>{try{onSnapshot(JSON.parse(e.data))}catch(x){}});return es}
const RF=rfMake({open:openStream,later:(f,ms)=>setTimeout(f,ms),changed:()=>{menuLabel('refresh');renderRefNow()},store:{getItem:k=>localStorage.getItem(k),setItem:(k,v)=>localStorage.setItem(k,v)},visible:()=>document.visibilityState!=='hidden',every:(f,ms)=>setInterval(f,ms),cancel:clearInterval,state:getState,history:refreshHistory,floor:histFloor});
MENUS.refresh={label:'Refresh interval',opts:()=>RF.choices().map(c=>({k:c.k,t:c.k===RF.key()?RF.label():c.t})),cur:()=>RF.key(),pick:k=>RF.set(k)};
function renderRefNow(){const b=$('#refnow');if(!b)return;const off=RF.nowDisabled();b.classList.toggle('off',off);if(off)b.setAttribute('aria-disabled','true');else b.removeAttribute('aria-disabled');b.title=RF.nowTitle()}
function renderRefBar(){const el=$('#refbar');if(el){menuDrop();setHtml(el,menuHtml('refresh'))}}
document.addEventListener('visibilitychange',()=>RF.visibility(document.visibilityState==='visible'));

/* ---------- provider plan and quota, read by Providers and Usage ---------- */
const PNAME={zai:'Z.ai GLM',kimi:'Kimi',deepseek:'DeepSeek',minimax:'MiniMax',qwen:'Qwen'};
/* a provider's display name: its lane's name, else the catalog's, else its id */
const provName=id=>{const l=LANES.find(x=>x.id===id);return (l&&l.name)||PNAME[id]||id||'—'};
const resetAt=t=>dayText(t)===dayText(NOW)?clock(t).slice(0,5):when(t);
const provConsole=id=>{const p=SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers[id]:null;return p&&typeof p.console_url==='string'?p.console_url:null};
const provQuota=id=>{const p=SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers[id]:null;return p&&p.quota&&typeof p.quota==='object'?p.quota:null};
/* a window is used up while its reset is still ahead; a balance at zero is used up */
function quotaState(q){
 if(!q)return {out:false,reset:null};
 const full=arr(q.windows).filter(w=>num(w.used_pct)!=null&&w.used_pct>=100&&(num(w.resets_ms)==null||w.resets_ms>NOW));
 const dry=!!q.balance&&num(q.balance.amount)!=null&&q.balance.amount<=0;
 const ts=full.map(w=>num(w.resets_ms)).filter(t=>t!=null);
 return {out:full.length>0||dry,reset:ts.length?Math.min(...ts):null}}
const quotaLine=w=>num(w.used_pct)==null?`<div class="qw"><span>${esc(w.name)}</span> <span class="faint">${esc(w.note||'not reported')}</span></div>`
 :`<div class="qw"><span>${esc(w.name)}</span> <b class="${w.used_pct>=90?'badc':w.used_pct>=80?'warnc':''}">${Math.round(w.used_pct)}%</b>${num(w.resets_ms)!=null?` <span class="faint">resets ${resetAt(w.resets_ms)}</span>`:''}</div>`;
function quotaBody(x,q){
 if(q.kind==='none')return x.console?`<a class="lnk" href="${esc(x.console)}" target="_blank" rel="noopener">Open its console</a>`:'<span class="faint">no quota API</span>';
 const un=q.unverified?' <span class="faint">(unverified)</span>':'';
 if(q.error&&!arr(q.windows).length&&!q.balance)return `<span class="faint">${esc(q.error)}</span>`;
 if(q.kind==='balance'&&q.balance)return `<span class="mono">${num(q.balance.amount)==null?'—':q.balance.amount.toFixed(2)} ${esc(q.balance.currency||'')}</span>${un}`;
 if(arr(q.windows).length)return arr(q.windows).map(quotaLine).join('')+un;
 return '<span class="faint">quota unknown</span>'}
/* the provider a model belongs to: the live row's lane, else the lane whose model pattern claims it */
const laneName=id=>id==='anthropic'?'Claude':(LANES.find(l=>l.id===id)||{}).name||PNAME[id]||(id==='other'?'Other':id);
function laneOfModel(id){const m=MODELS.find(x=>x.id===id);if(m&&m.lane)return m.lane;
 const l=LANES.find(l=>typeof l.match==='string'&&(l.match.endsWith('*')?id.startsWith(l.match.slice(0,-1)):l.match===id));
 return l?l.id:id.startsWith('claude-')?'anthropic':'other'}
