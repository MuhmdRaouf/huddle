/* ---------- overview ---------- */
function attnItems(){const it=[];
 const P=RT.path;if(P.state==='failing')it.push({k:'bad',tag:'Anthropic',t:`Anthropic is not answering: ${P.code||'error'} at ${P.phase||'?'} since ${hm(P.since_ms)} (${num(P.failed)||0} requests failed)`,m:`${P.hint||''}${P.peer?' (peer '+P.peer+')':''}`,act:cmd('huddle router log')});
 const open=s=>`<button class="btn sm" data-open="session:${esc(s.id)}">Open</button>`;
 SESS.filter(s=>sstate(s)==='waiting').sort((a,b)=>(a.waitingSince||0)-(b.waitingSince||0)).forEach(s=>it.push({k:'warn',tag:'Waiting '+since(s.waitingSince),t:s.name,m:[s.project,s.branch,s.sid,s.ctx!=null?'context '+Math.round(s.ctx)+'%':''].filter(Boolean).join(' · '),act:open(s)}));
 SESS.filter(s=>s.ctx!=null&&s.ctx>=80).forEach(s=>it.push({k:'bad',tag:`Context ${Math.round(s.ctx)}%`,t:`${s.name} is close to compaction`,m:'Claude Code compacts near the limit. Wrap up the task or start a fresh session.',act:open(s)}));
 const by429={};stats().done.filter(r=>r.status===429&&NOW-r.t<5*MIN).forEach(r=>{by429[r.lane]=(by429[r.lane]||0)+1});
 Object.entries(by429).forEach(([l,n])=>{const L=LANES.find(x=>x.id===l);it.push({k:'warn',tag:`${n} × 429`,t:`${L?L.name:l||'A provider'} is rate limiting requests`,m:'Last 5 min. Claude Code backed off and retried each one.',act:`<a class="btn sm" href="#errors">View</a>`})});
 JOBS.filter(j=>j.state==='awaiting_review').forEach(j=>it.push({k:'ok',tag:'Review',t:`Job ${j.id} is ready`,m:[j.h,j.repo,j.task].filter(Boolean).join(' · '),act:cmd(`/huddle:result ${j.id} --diff`)}));
 JOBS.filter(j=>j.state==='dead'||j.state==='failed').forEach(j=>it.push({k:'bad',tag:j.state==='dead'?'Dead':'Failed',t:`Job ${j.id}: ${j.task||j.h}`,m:j.why||'The job did not finish.',act:cmd(`/huddle:cancel ${j.id}`)}));
 if(WIRE.state==='bad')it.push({k:'bad',tag:'Wiring',t:'Claude Code is not pointed at the router',m:'The proxy setting or the local CA is missing.',act:cmd('/huddle:setup')});
 PL.checks.filter(c=>c.level==='fail'||c.level==='warn').forEach(c=>it.push({k:c.level==='fail'?'bad':'warn',tag:'Check',t:`${c.label||c.id}: ${c.value||''}`,m:c.detail||'',act:`<a class="btn sm" href="#router">View</a>`}));
 provRows().filter(x=>x.status[1]==='Needs a fix').forEach(x=>it.push({k:'bad',tag:'Needs a fix',t:`${x.name} needs a fix`,m:x.key&&!x.laneOn?'The key is saved but the lane is off.':'Its last request failed, or its lane is off.',act:cmd(x.next)}));
 const stale=WT.filter(w=>w.stale);if(stale.length)it.push({k:'off',tag:'Cleanup',t:`${stale.length} agent worktree${stale.length>1?'s have':' has'} had no session for days`,m:tilde(stale[0].path),act:`<a class="btn sm" href="#worktrees">Review</a>`});
 return it}
const unsetProviders=()=>provRows().filter(x=>x.status[1]==='Not set up').length;
function recovered(){const B=RT.path.last_recovery;return B&&RT.path.state!=='failing'&&num(B.at_ms)!=null&&NOW-B.at_ms<30*MIN?`<div class="attn-row quiet"><div class="what"><div class="m">Anthropic recovered at ${hm(B.at_ms)} after ${dur(B.after_ms)}</div></div></div>`:''}
function attnList(){const it=attnItems(),u=unsetProviders();
 const rows=it.map(a=>`<div class="attn-row"><span class="tag ${a.k}">${esc(a.tag)}</span><div class="what"><div class="t">${esc(a.t)}</div><div class="m">${esc(a.m)}</div></div>${a.act}</div>`).join('');
 const quiet=u?`<div class="attn-row quiet"><div class="what"><div class="m">${u} provider${u>1?'s':''} not set up (optional)</div></div><a class="btn sm" href="#providers">Providers</a></div>`:'';
 return (it.length?rows:`<div class="allclear">${ic('check')} Nothing needs you.</div>`)+recovered()+quiet}
function overviewKpis(){if(!liveRange())return histKpis();const b=BK;
 const live=SESS.filter(s=>sstate(s)!=='idle').length,waiting=SESS.filter(s=>sstate(s)==='waiting').length;
 const subs=AGENTS.filter(a=>a.type!=='main'),run=subs.filter(a=>a.state==='running').length,doneN=subs.filter(a=>a.state==='done'&&a.end!=null&&a.end>NOW-15*MIN).length;
 const last5=b.slice(20),prev5=b.slice(10,20);
 const rps=sum(last5.map(x=>x.n))/(5*MIN/SEC),tps=sum(last5.map(x=>x.tok))/(5*MIN/SEC);
 const reqs=sum(MODELS.map(m=>m.n)),errs=sum(MODELS.map(m=>m.err)),c499=stats().done.filter(r=>r.status===499).length;
 const p50=wavg(MODELS.filter(m=>m.p50!=null),m=>m.p50,m=>m.n),p95=wavg(MODELS.filter(m=>m.p95!=null),m=>m.p95,m=>m.n);
 const split=tokSplit(sum(MODELS.map(m=>m.tin)),sum(MODELS.map(m=>m.cr)),sum(MODELS.map(m=>m.cw))),tin=split.tin;
 const items=[
  {k:'Live sessions',v:live,small:`of ${SESS.length}`,d:waiting?`<span class="warnc">${waiting} waiting on you</span>`:'none waiting'},
  {k:'Agents running',v:run,small:`of ${subs.length}`,d:`${doneN} finished in 15 min`},
  {k:'Requests',v:rate(rps),small:'/s',delta:deltaH(sum(last5.map(x=>x.n)),sum(prev5.map(x=>x.n)),null),sp:b.map(x=>x.n)},
  {k:'Tokens',v:rate(tps),small:'tokens/s',delta:deltaH(sum(last5.map(x=>x.tok)),sum(prev5.map(x=>x.tok)),null),sp:b.map(x=>x.tok),color:'var(--teal)',d:tin?`${Math.round(split.hit*100)}% read from cache`:'—'},
  {k:'Time to first token',v:p50!=null?Math.round(p50):'—',small:'ms p50',sp:b.map(x=>x.p50),color:'var(--mauve)',d:p95!=null?`p95 ${Math.round(p95)} ms`:'—'},
  {k:'Error rate',v:reqs?(errs/reqs*100).toFixed(1):'—',small:reqs?'%':'',sp:b.map(x=>x.err),color:'var(--bad)',d:`${errs} errors · ${c499} cancelled (not errors)`}];
 if(PLAN){
  if(PLAN.h5!=null)items.push({k:'Claude plan · 5 h',v:Math.round(PLAN.h5),small:'%',meter:PLAN.h5,d:PLAN.h5r!=null?'resets in '+dur(PLAN.h5r-NOW):'—'});
  if(PLAN.wk!=null)items.push({k:'Claude plan · week',v:Math.round(PLAN.wk),small:'%',meter:PLAN.wk,d:PLAN.wkr!=null?'resets in '+dur(PLAN.wkr-NOW):'—'});
 }
 return `<div class="card kpis${items.length===6?' k6':''}">${kpiGrid(items)}</div>`}
const CH={metric:'req',focus:null,hover:null};
const CM={
 req:{label:'Requests',val:b=>b.n,agg:bs=>sum(bs.map(b=>b.n)),f:v=>fmt(v),up:null},
 tok:{label:'Tokens',val:b=>b.tok,agg:bs=>sum(bs.map(b=>b.tok)),f:v=>fmt(v),up:null},
 lat:{label:'Time to first token',val:b=>b.p50||0,agg:bs=>wavg(bs,b=>b.p50||0,b=>b.n)||0,f:v=>Math.round(v)+'<small>ms p50</small>',up:false},
 err:{label:'Errors',val:b=>b.err,agg:bs=>sum(bs.map(b=>b.err)),f:v=>fmt(v),up:false},
 cost:{label:'Est. cost',val:b=>b.cost||0,agg:bs=>sum(bs.map(b=>b.cost||0)),f:v=>usd(v),up:null}
};
function mtabs(){const b=ser().b,t=Math.floor(b.length/3);return `<div class="mtabs" role="tablist">${Object.entries(CM).filter(([k])=>k!=='cost'||!liveRange()).map(([k,c])=>`<button class="mtab" role="tab" data-metric="${k}" aria-selected="${k===CH.metric}"><div class="k">${c.label}</div><div class="v">${c.f(c.agg(b))}${deltaH(c.agg(b.slice(-t)),c.agg(b.slice(-2*t,-t)),c.up)}</div></button>`).join('')}</div>`}
function modelTotals(metric){const t={},S=ser();S.mn.forEach(m=>{const mb=S.b.map(x=>x.byM[m]||EMPTY);t[m]=metric==='lat'?(CM.lat.agg(mb)||0):sum(mb.map(x=>CM[metric].val(x)))});return t}
function bars(){const t=modelTotals(CH.metric),max=Math.max(1,...Object.values(t));
 return `<div class="k"><span>By model</span><span class="faint">${CH.focus?'click again for all':'click to focus'}</span></div><div class="bars">${ser().mn.length?ser().mn.slice().sort((a,b)=>t[b]-t[a]).map(m=>`<button class="bar${CH.focus&&CH.focus!==m?' off':''}${CH.focus===m?' on':''}" data-focus="${esc(m)}"><i data-css="width:${(t[m]/max*100).toFixed(1)}%"></i><span>${sw(m)}<span class="mono">${esc(m)}</span></span><span class="n">${CH.metric==='lat'?Math.round(t[m])+' ms':CH.metric==='cost'?usd(t[m]):fmt(t[m])}</span></button>`).join(''):'<div class="faint pad-s">No traffic yet</div>'}</div>`}
function smooth(p){if(p.length<2)return '';let d=`M${p[0][0].toFixed(1)},${p[0][1].toFixed(1)}`;for(let i=0;i<p.length-1;i++){const[x0,y0]=p[i],[x1,y1]=p[i+1],cx=((x0+x1)/2).toFixed(1);d+=` C${cx},${y0.toFixed(1)} ${cx},${y1.toFixed(1)} ${x1.toFixed(1)},${y1.toFixed(1)}`}return d}
function niceMax(v){if(v<=0)return 1;const p=Math.pow(10,Math.floor(Math.log10(v))),n=v/p;return (n<=1?1:n<=2?2:n<=2.5?2.5:n<=5?5:10)*p}
function drawChart(){const svg=$('#chart');if(!svg)return;const W=svg.clientWidth;if(!W)return;const H=240,pl=46,pr=14,pt=10,pb=26;
 svg.setAttribute('viewBox',`0 0 ${W} ${H}`);const S=ser(),b=S.b,c=CM[CH.metric],N=b.length;
 if(!c){CH.metric='req';return drawChart()}
 if(CH.focus&&S.mn.indexOf(CH.focus)<0)CH.focus=null;
 const data=b.map(x=>c.val(CH.focus?(x.byM[CH.focus]||EMPTY):x));const color=CH.focus?col(CH.focus):'var(--accent)';
 const mx=niceMax(Math.max(1,...data)/4)*4;const x=i=>pl+i*(W-pl-pr)/(N-1),y=v=>pt+(H-pt-pb)*(1-v/mx);
 let g=`<defs><linearGradient id="cg" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity=".22"/><stop offset="1" stop-color="${color}" stop-opacity="0"/></linearGradient></defs>`;
 for(let k=0;k<=4;k++){const v=mx*k/4;g+=`<line class="gl" x1="${pl}" x2="${W-pr}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"${k?' stroke-dasharray="2 4"':''}/><text class="ax" x="${pl-8}" y="${(y(v)+4).toFixed(1)}" text-anchor="end">${CH.metric==='lat'?Math.round(v):CH.metric==='cost'?usd(v):fmt(v)}</text>`}
 [0,Math.floor(N/3),Math.floor(2*N/3),N-1].forEach((i,j)=>{g+=`<text class="ax" x="${x(i).toFixed(1)}" y="${H-6}" text-anchor="${j===0?'start':j===3?'end':'middle'}">${j===3?'now':tickLabel(b[i].t)}</text>`});
 const pts=data.map((v,i)=>[x(i),y(v)]),line=smooth(pts);
 g+=`<path d="${line} L${x(N-1).toFixed(1)},${y(0).toFixed(1)} L${x(0).toFixed(1)},${y(0).toFixed(1)} Z" fill="url(#cg)"/><path d="${line}" fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round"/>`;
 g+=`<circle cx="${x(N-1).toFixed(1)}" cy="${y(data[N-1]).toFixed(1)}" r="3.5" fill="${color}" stroke="var(--surface)" stroke-width="2"/>`;
 const tip=$('#tip');
 if(CH.hover!=null){const i=CH.hover;g+=`<line x1="${x(i).toFixed(1)}" x2="${x(i).toFixed(1)}" y1="${pt}" y2="${H-pb}" stroke="var(--faint)" stroke-dasharray="3 3"/><circle cx="${x(i).toFixed(1)}" cy="${y(data[i]).toFixed(1)}" r="4" fill="var(--surface)" stroke="${color}" stroke-width="2"/>`;
  const bk=b[i],rows=S.mn.map(m=>({m,v:c.val(bk.byM[m]||EMPTY)})).filter(r=>r.v>0&&(!CH.focus||r.m===CH.focus)).sort((a,b)=>b.v-a.v);
  const f=v=>CH.metric==='lat'?Math.round(v)+' ms':CH.metric==='cost'?usd(v):fmt(v);
  setHtml(tip,`<div class="h">${longBuckets()?tickLabel(bk.t):clock(bk.t)+'–'+clock(bk.t+S.bw).slice(3)}</div>${rows.map(r=>`<div class="r"><span>${sw(r.m)}${esc(r.m)}</span><span>${f(r.v)}</span></div>`).join('')||'<div class="r"><span>No requests</span><span></span></div>'}<div class="r tot"><span>${CH.focus?esc(CH.focus):'All models'}</span><span>${f(data[i])}</span></div>`);
  const rect=svg.getBoundingClientRect(),left=x(i)*rect.width/W;tip.classList.add('on');const tw=tip.offsetWidth;tip.style.left=Math.max(0,left>rect.width/2?left-tw-14:left+14)+'px'}
 else tip.classList.remove('on');
 setHtml(svg,g)}
function bindChart(){const svg=$('#chart');if(!svg)return;
 svg.onpointermove=e=>{const r=svg.getBoundingClientRect(),W=svg.clientWidth,px=(e.clientX-r.left)*W/r.width;const N=ser().b.length,i=Math.max(0,Math.min(N-1,Math.round((px-46)/((W-60)/(N-1)))));if(i!==CH.hover){CH.hover=i;drawChart()}};
 svg.onpointerleave=()=>{CH.hover=null;drawChart()}}
function feed(){const ev=[];
 SESS.forEach(s=>{
  if(sstate(s)==='waiting'&&s.waitingSince!=null)ev.push({t:s.waitingSince,k:'warn',i:'sessions',txt:`<b>${esc(s.name)}</b> is waiting on you`,m:`${s.project} · ${s.sid}`});
  if(s.started!=null)ev.push({t:s.started,k:'acc',i:'sessions',txt:`Session started: <b>${esc(s.name)}</b>`,m:`${s.project} · ${s.sid}`});
  s.agents.slice(1).forEach(a=>{const w=esc(a.desc||a.type);
   if(a.end!=null)ev.push({t:a.end,k:'ok',i:'agents',txt:`Subagent finished: <b>${w}</b>`,m:`${a.model} · ${dur(a.end-(a.start||a.end))}`});
   if(a.start!=null)ev.push({t:a.start,k:'acc',i:'agents',txt:`Subagent started: <b>${w}</b>`,m:`${a.type} · ${s.project}`})})});
 JOBS.forEach(j=>{if(j.started!=null)ev.push({t:j.started,k:'acc',i:'jobs',txt:`Job started: <b>${esc(j.id)}</b>`,m:`${j.h} · ${j.repo}`});if(j.end!=null)ev.push({t:j.end,k:j.state==='dead'||j.state==='failed'?'bad':'ok',i:'jobs',txt:`Job <b>${esc(j.id)}</b> ${j.state==='awaiting_review'?'is ready for review':j.state}`,m:`${j.h} · ${j.repo}`})});
 stats().done.filter(isErr).slice(-4).forEach(r=>ev.push({t:r.t,k:r.status===429?'warn':'bad',i:'errors',txt:`<b>${r.status} ${esc(errDef(r.status,r.reason)[0].toLowerCase())}</b> on ${esc(r.m)}`,m:r.s?`${r.s.project} · ${r.a?r.a.type:'—'}`:r.lane}));
 const out=ev.sort((a,b)=>b.t-a.t).slice(0,10);
 return out.length?out.map(e=>`<div class="ev"><span class="ic ${e.k}">${ic(e.i)}</span><div><div class="t">${e.txt}</div><div class="m">${esc(e.m)}</div></div><time>${ago(e.t)}</time></div>`).join(''):`<div class="allclear">${ic('info')} Nothing has happened yet</div>`}
const SESS_COLS=[
 {k:'state',label:'State',v:s=>({waiting:0,working:1,idle:2})[sstate(s)]*1e13-(lastT(s)||0),h:statePill},
 {k:'title',label:'Session',v:s=>s.name,h:s=>`<div class="ct">${esc(s.name)}</div><div class="cs">${esc([s.project,s.branch,s.sid].filter(Boolean).join(' · '))}</div>`},
 {k:'model',label:'Model',v:s=>mainA(s).model,h:s=>modelTag(mainA(s).model)},
 {k:'agents',label:'Subagents',v:s=>s.agents.length-1,h:agentsCell,sm:1},
 {k:'ctx',label:'Context',num:1,v:s=>s.ctx==null?-1:s.ctx,h:ctxCell},
 {k:'cache',label:'Prompt cache',v:s=>{const l=cacheLeft(s);return l==null?-1e12:l},h:cacheCell,sm:1},
 {k:'req',label:'Requests',num:1,v:s=>sReq(s).length,h:s=>`<div>${sReq(s).length}</div><div class="cs">${fmt(tokInOf(sReq(s)))} in · ${fmt(tokOutOf(sReq(s)))} out</div>`,sm:1},
 {k:'last',label:'Last activity',num:1,v:s=>-(lastT(s)||0),h:s=>`<span class="muted">${ago(lastT(s))}</span>`},
 ...histCols(s=>hOf(HT.ses,s.id))
];
function lanesMini(){if(!LANES.length)return `<div class="allclear">${ic('info')} No lanes yet</div>`;return LANES.map(l=>{const live=l.state==='live';
 return `<div class="li"><span class="dot ${live?(l.err/Math.max(1,l.n)>.08?'warn':'ok'):'off'}"></span><div class="grow"><div>${esc(l.name)}</div><div class="sm mono">${esc(l.match)}</div></div>${live?`<span class="mono muted">${l.n} req · ${msf(l.p50)}</span>`:pill('warn','Not set up')}</div>`}).join('')}
function jobPill(j){return j.state==='running'?pill('acc','Running'):j.state==='awaiting_review'?pill('warn','Review'):j.state==='queued'?pill('off','Queued'):j.state==='dead'?pill('bad','Dead'):j.state==='failed'?pill('bad','Failed'):j.state==='accepted'?pill('ok','Accepted'):pill('off',j.state||'—')}
function jobsMini(){if(!JOBS.length)return `<div class="allclear">${ic('info')} No delegated jobs</div>`;return JOBS.slice(0,6).map(j=>`<div class="li"><div class="grow"><div><span class="mono">${esc(j.id)}</span> <span class="muted">· ${esc(j.h)}</span></div><div class="sm">${esc(j.task)}</div></div>${jobPill(j)}</div>`).join('')}
const upCell=()=>num(SNAP.started_ms)!=null?dur(NOW-SNAP.started_ms):'—';
const cpuCell=()=>RT.cpu!=null?RT.cpu.toFixed(1)+'%':'—';
function routerMini(){return `<dl class="kv padkv">
 <dt>Status</dt><dd>${LOST?pill('bad','Down'):pill('ok','Up')} <span class="mono muted">${SNAP.pid!=null?'pid '+esc(SNAP.pid):''}</span></dd>
 <dt>Uptime</dt><dd class="mono">${upCell()}</dd>
 <dt>CPU · memory</dt><dd class="mono">${cpuCell()} · ${mb(RT.rss)}</dd>
 <dt>In flight</dt><dd class="mono">${stats().flights.length} streams · ${RT.conns!=null?RT.conns:'—'} connections</dd>
 <dt>Live viewers</dt><dd class="mono">${RT.viewers!=null?RT.viewers+' page'+(RT.viewers===1?'':'s')+' open':'—'}</dd>
 <dt>Request buffer</dt><dd class="mono">${arr(SNAP.recent).length} requests kept</dd></dl>`}

const kpiCard=(items,cls)=>`<div class="card kpis${cls?' '+cls:''}">${kpiGrid(items)}</div>`;
PAGES.overview={title:'Overview',body:()=>
 head('Overview',liveRange()?'Everything huddle sees right now, kept in memory. A router restart starts it fresh.':`Requests, tokens and cost for ${rangeText()}, from the history database. Sessions, jobs and providers below stay live.`)+
 `<section>${secH('Needs you',reg('attn-n',()=>`<span class="note">${attnItems().length} items</span>`))}<div class="card">${reg('attn',attnList)}</div></section>`+
 reg('rstat',rangeStatus)+reg('kpis',overviewKpis)+
 `<section>${secH('Traffic',reg('bw',()=>`<span class="note">${liveRange()?'30-second':dur(ser().bw)} buckets</span>`),['requests','All requests'])}<div class="card chart">${reg('mtabs',mtabs)}<div class="chart-body"><div class="plot"><svg id="chart" role="img" aria-label="Traffic over the selected range"></svg><div class="tip" id="tip"></div></div><div class="split">${reg('bars',bars)}</div></div></div></section>`+
  `<section>${secH('Live sessions',reg('sess-n',()=>`<span class="note">${SESS.filter(s=>sstate(s)!=='idle').length} live · waiting first</span>`),['sessions','All sessions'])}<div class="card t">${reg('sessmini',()=>table('sessmini',SESS_COLS.filter(c=>c.k!=='req'),SESS,{row:s=>'session:'+s.id,sort:{k:'state',d:1},stack:1,empty:'No sessions in the last 24 hours'}))}</div></section>`+
 `<div class="g3"><section>${secH('Activity')}<div class="card">${reg('feed',feed)}</div></section><section>${secH('Providers',null,['providers','Details'])}<div class="card list">${reg('lanesmini',lanesMini)}</div></section><div class="fold"><section>${secH('Delegated jobs',null,['jobs','All jobs'])}<div class="card list">${reg('jobsmini',jobsMini)}</div></section><section>${secH('Router',null,['router','Details'])}<div class="card">${reg('routermini',routerMini)}</div></section></div></div>`,
 after:()=>{bindChart();drawChart()}};
