/* ---------- overview ---------- */
function recovered(){const B=RT.path.last_recovery;return B&&RT.path.state!=='failing'&&num(B.at_ms)!=null&&NOW-B.at_ms<30*MIN?`<div class="attn-row quiet"><div class="what"><div class="m">Anthropic recovered at ${hm(B.at_ms)} after ${dur(B.after_ms)}</div></div></div>`:''}
function attnList(){const all=attnItems(),u=unsetProviders();
 const rows=all.slice(0,5).map(attnRow).join('');
 const quiet=u?`<div class="attn-row quiet"><div class="what"><div class="m">${u} provider${u>1?'s':''} not set up (optional)</div></div><a class="btn sm" href="#providers">Providers</a></div>`:'';
 const more=all.length>5?`<div class="attn-row quiet"><div class="what"><div class="m">${all.length-5} more</div></div><a class="btn sm" href="#needs-you">Show all</a></div>`:'';
 return (all.length?rows:`<div class="allclear">${ic('check')} Nothing needs you.</div>`)+more+recovered()+quiet}
const OV={cost:null,at:0};
function loadOvCost(){if(!liveRange()||page!=='overview'||Date.now()-OV.at<60000)return;OV.at=Date.now();
 hget(HQ+'totals&by=model&range=15m').then(d=>{OV.cost=d;refresh()},()=>{})}
const provOf=m=>{const k=MODELS.find(x=>x.id===m);if(k&&k.lane)return k.lane;
 const L=LANES.find(l=>l.id!=='anthropic'&&String(l.match).split(',').some(p=>{p=p.trim();return p.endsWith('*')&&m.startsWith(p.slice(0,-1))}));return L?L.id:'anthropic'};
const PORDER=['anthropic','zai','deepseek','kimi','minimax','qwen'];
const provCol=id=>`var(--c${(PORDER.indexOf(id)<0?PORDER.length:PORDER.indexOf(id))%6+1})`;
const swp=id=>`<span class="sw" data-css="background:${provCol(id)}"></span>`;
function planCard(name,h5,wk){const p=h5&&num(h5.used_pct)!=null?h5.used_pct:null,w=wk&&num(wk.used_pct)!=null?Math.round(wk.used_pct)+'%':'not reported';
 return {k:name,v:p!=null?Math.round(p):'—',small:'% · 5 h',meter:p,mk:p>=90?'max':p>=80?'hi':'',d:`week ${w} · resets ${h5&&num(h5.resets_ms)!=null?'in '+dur(h5.resets_ms-NOW):'—'}`}}
function planCards(){const ps=SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers:{},out=[];
 const ids=Object.keys(ps).filter(id=>{const q=quotaOf(id);return q&&q.kind==='plan'&&arr(q.windows).some(w=>num(w.used_pct)!=null)}).sort((a,b)=>(b==='anthropic')-(a==='anthropic'));
 ids.forEach(id=>{const w=arr(quotaOf(id).windows);out.push(planCard(provName(id)+' plan',w.find(x=>/5 h/.test(x.name)),w.find(x=>/7 d|week/i.test(x.name))))});
 if(PLAN){if(!ids.includes('anthropic'))out.unshift(planCard('Claude plan',{used_pct:PLAN.h5,resets_ms:PLAN.h5r},{used_pct:PLAN.wk}))}
 return out}
function errorSplit(rows){const by={};rows.forEach(r=>{if(r.err){const p=provOf(r.id);by[p]=(by[p]||0)+r.err}});
 return Object.entries(by).sort((a,b)=>b[1]-a[1]).slice(0,3).map(([p,n])=>`${esc(provName(p))} ${n}`).join(' · ')}
function overviewKpis(){const live=liveRange();if(!live&&!HIST.tot)return `<div class="card pad">${checking()}</div>`;
 let rows,reqs,tok,tin,cr,cw,span,sp,spT,deltaR,deltaT,c499=0;const b=ser().b;
 if(live){rows=MODELS;reqs=sum(rows.map(m=>m.n));const l5=BK.slice(20),p5=BK.slice(10,20);span=5*MIN/SEC;
  tin=sum(rows.map(m=>m.tin));cr=sum(rows.map(m=>m.cr));cw=sum(rows.map(m=>m.cw));tok=sum(l5.map(x=>x.tok));
  deltaR=deltaH(sum(l5.map(x=>x.n)),sum(p5.map(x=>x.n)),null);deltaT=deltaH(sum(l5.map(x=>x.tok)),sum(p5.map(x=>x.tok)),null);
  reqs=sum(l5.map(x=>x.n));c499=stats().done.filter(r=>r.status===499).length}
 else{rows=histRows();reqs=sum(rows.map(r=>r.n));tin=sum(rows.map(r=>r.tin));cr=sum(rows.map(r=>r.cr));cw=sum(rows.map(r=>r.cw));tok=tin+sum(rows.map(r=>r.out));
  span=Math.max(1,((num(HIST.bk&&HIST.bk.to_ms)||NOW)-(num(HIST.bk&&HIST.bk.from_ms)||NOW-rangeMin()*MIN))/SEC);deltaR=deltaT=''}
 const total=sum(rows.map(r=>r.n)),errs=sum(rows.map(r=>r.err)),split=tokSplit(tin,cr,cw);
 const wp=f=>wavg(rows.filter(r=>f(r)!=null),f,r=>r.n),p50=wp(r=>r.p50),p95=wp(r=>r.p95);
 const ct=live?OV.cost:HIST.tot,cRows=arr(ct&&ct.totals),cost=sum(cRows.map(t=>n0(t.cost_usd))),unpriced=cRows.filter(t=>num(t.cost_usd)==null&&n0(t.n)>0).length;
 const subs=AGENTS.filter(a=>a.type!=='main'),run=subs.filter(a=>a.state==='running').length;
 const needs=SESS.filter(s=>s.state==='needs_you').length,work=SESS.filter(s=>sstate(s)!=='idle').length;
 const cards=[
  {k:'Sessions',v:work,small:`of ${SESS.length}`,d:needs?`<span class="warnc">${needs} need${needs>1?'':'s'} you</span>`:'none need you'},
  {k:'Requests',v:rate(reqs/span),small:'/s',delta:deltaR,sp:b.map(x=>x.n),d:`${fmt(total)} in ${live?'15 min':rangeText()}`},
  {k:'Tokens',v:rate(tok/span),small:'tokens/s',delta:deltaT,sp:b.map(x=>x.tok),color:'var(--teal)',d:tin?`${Math.round(split.hit*100)}% read from cache`:'—'},
  {k:'Cost',v:ct?usd(cost):'—',d:ct?(unpriced?`API price · ${unpriced} model${unpriced>1?'s':''} unpriced`:`API price · ${live?'last 15 min':rangeText()}`):'Checking…'},
  {k:'Time to first token',v:p50!=null?Math.round(p50):'—',small:'ms p50',sp:b.map(x=>x.p50),color:'var(--mauve)',d:p95!=null?`p95 ${Math.round(p95)} ms`:'—'},
  {k:'Errors',v:errs,sp:b.map(x=>x.err),color:'var(--bad)',d:errs?errorSplit(rows):live?`none · ${c499} cancelled (not errors)`:'none'},
  ...planCards()];
 const fillers=[{k:'Agents running',v:run,small:`of ${subs.length}`,d:'subagents'},{k:'Jobs',v:JOBS.filter(j=>j.state==='running').length,small:'running',d:`${JOBS.filter(j=>j.state==='awaiting_review').length} to review`}];
 while(cards.length<8&&fillers.length)cards.push(fillers.shift());
 return `<div class="card kpis k8">${kpiGrid(cards.slice(0,8))}</div>`}
const CH={metric:'req',focus:null,hover:null};
const CM={
 req:{label:'Requests',val:b=>b.n,agg:bs=>sum(bs.map(b=>b.n)),f:v=>fmt(v),up:null,stack:1},
 tok:{label:'Tokens',val:b=>b.tok,agg:bs=>sum(bs.map(b=>b.tok)),f:v=>fmt(v),up:null,stack:1},
 lat:{label:'Time to first token',val:b=>b.p50||0,agg:bs=>wavg(bs,b=>b.p50||0,b=>b.n)||0,f:v=>Math.round(v)+'<small>ms p50</small>',up:false},
 err:{label:'Errors',val:b=>b.err,agg:bs=>sum(bs.map(b=>b.err)),f:v=>fmt(v),up:false,stack:1},
 cost:{label:'Est. cost',val:b=>b.cost||0,agg:bs=>sum(bs.map(b=>b.cost||0)),f:v=>usd(v),up:null,stack:1}
};
function mtabs(){const b=ser().b,t=Math.floor(b.length/3);return `<div class="mtabs" role="tablist">${Object.entries(CM).filter(([k])=>k!=='cost'||!liveRange()).map(([k,c])=>`<button class="mtab" role="tab" data-metric="${k}" aria-selected="${k===CH.metric}"><div class="k">${c.label}</div><div class="v">${c.f(c.agg(b))}${deltaH(c.agg(b.slice(-t)),c.agg(b.slice(-2*t,-t)),c.up)}</div></button>`).join('')}</div>`}
function provBucket(x,p){const o={n:0,tok:0,err:0,cost:0,w:0};ser().mn.forEach(m=>{if(provOf(m)!==p)return;const k=x.byM[m];if(!k)return;o.n+=k.n;o.tok+=k.tok;o.err+=k.err;o.cost+=k.cost||0;o.w+=(k.p50||0)*k.n});o.p50=o.n?o.w/o.n:0;return o}
const serProvs=()=>[...new Set(ser().mn.map(provOf))].sort((a,b)=>(PORDER.indexOf(a)<0?9:PORDER.indexOf(a))-(PORDER.indexOf(b)<0?9:PORDER.indexOf(b)));
function provTotals(metric){const t={},S=ser();serProvs().forEach(p=>{const mb=S.b.map(x=>provBucket(x,p));t[p]=metric==='lat'?(CM.lat.agg(mb)||0):sum(mb.map(x=>CM[metric].val(x)))});return t}
function bars(){const t=provTotals(CH.metric),max=Math.max(1,...Object.values(t)),ps=serProvs();
 return `<div class="k"><span>By provider</span><span class="faint">${CH.focus?'click again for all':'click to focus'}</span></div><div class="bars">${ps.length?ps.slice().sort((a,b)=>t[b]-t[a]).map(p=>`<button class="bar${CH.focus&&CH.focus!==p?' off':''}${CH.focus===p?' on':''}" data-focus="${esc(p)}"><i data-css="width:${(t[p]/max*100).toFixed(1)}%;background:color-mix(in srgb,${provCol(p)} 24%,transparent)"></i><span>${swp(p)}<span>${esc(provName(p))}</span></span><span class="n">${CH.metric==='lat'?Math.round(t[p])+' ms':CH.metric==='cost'?usd(t[p]):fmt(t[p])}</span></button>`).join(''):'<div class="faint pad-s">No traffic</div>'}</div>`}
function smooth(p){if(p.length<2)return '';let d=`M${p[0][0].toFixed(1)},${p[0][1].toFixed(1)}`;for(let i=0;i<p.length-1;i++){const[x0,y0]=p[i],[x1,y1]=p[i+1],cx=((x0+x1)/2).toFixed(1);d+=` C${cx},${y0.toFixed(1)} ${cx},${y1.toFixed(1)} ${x1.toFixed(1)},${y1.toFixed(1)}`}return d}
function niceMax(v){if(v<=0)return 1;const p=Math.pow(10,Math.floor(Math.log10(v))),n=v/p;return (n<=1?1:n<=2?2:n<=2.5?2.5:n<=5?5:10)*p}
function drawChart(){const svg=$('#chart');if(!svg)return;const W=svg.clientWidth;if(!W)return;const H=240,pl=46,pr=14,pt=10,pb=26;
 svg.setAttribute('viewBox',`0 0 ${W} ${H}`);const S=ser(),b=S.b,c=CM[CH.metric],N=b.length;
 if(!c){CH.metric='req';return drawChart()}
 const ps=serProvs();if(CH.focus&&ps.indexOf(CH.focus)<0)CH.focus=null;
 const val=(x,p)=>c.val(p?provBucket(x,p):x),data=b.map(x=>val(x,CH.focus));
 const stacked=!!c.stack&&!CH.focus,layers=stacked?ps.map(p=>({id:p,v:b.map(x=>val(x,p))})):[{id:CH.focus,v:data}];
 const mx=niceMax(Math.max(1,...data)/4)*4;const x=i=>pl+i*(W-pl-pr)/(N-1),y=v=>pt+(H-pt-pb)*(1-v/mx);
 const color=CH.focus?provCol(CH.focus):'var(--accent)';
 let g=`<defs><linearGradient id="cg" x1="0" y1="0" x2="0" y2="1"><stop offset="0" stop-color="${color}" stop-opacity=".22"/><stop offset="1" stop-color="${color}" stop-opacity="0"/></linearGradient></defs>`;
 for(let k=0;k<=4;k++){const v=mx*k/4;g+=`<line class="gl" x1="${pl}" x2="${W-pr}" y1="${y(v).toFixed(1)}" y2="${y(v).toFixed(1)}"${k?' stroke-dasharray="2 4"':''}/><text class="ax" x="${pl-8}" y="${(y(v)+4).toFixed(1)}" text-anchor="end">${CH.metric==='lat'?Math.round(v):CH.metric==='cost'?usd(v):fmt(v)}</text>`}
 [0,Math.floor(N/3),Math.floor(2*N/3),N-1].forEach((i,j)=>{g+=`<text class="ax" x="${x(i).toFixed(1)}" y="${H-6}" text-anchor="${j===0?'start':j===3?'end':'middle'}">${j===3?'now':tickLabel(b[i].t)}</text>`});
 if(stacked){let base=b.map(()=>0);layers.forEach(L=>{const top=L.v.map((v,i)=>base[i]+v),f=top.map((v,i)=>`${x(i).toFixed(1)},${y(v).toFixed(1)}`),r=base.map((v,i)=>`${x(i).toFixed(1)},${y(v).toFixed(1)}`).reverse();
  g+=`<path d="M${f.join(' L')} L${r.join(' L')}Z" fill="${provCol(L.id)}" fill-opacity=".55"/><path d="M${f.join(' L')}" fill="none" stroke="${provCol(L.id)}" stroke-width="1.5" stroke-linejoin="round"/>`;base=top})}
 else{const pts=data.map((v,i)=>[x(i),y(v)]),line=smooth(pts);
  g+=`<path d="${line} L${x(N-1).toFixed(1)},${y(0).toFixed(1)} L${x(0).toFixed(1)},${y(0).toFixed(1)} Z" fill="url(#cg)"/><path d="${line}" fill="none" stroke="${color}" stroke-width="2" stroke-linecap="round"/>`;
  g+=`<circle cx="${x(N-1).toFixed(1)}" cy="${y(data[N-1]).toFixed(1)}" r="3.5" fill="${color}" stroke="var(--surface)" stroke-width="2"/>`}
 const tip=$('#tip');
 if(CH.hover!=null){const i=CH.hover;g+=`<line x1="${x(i).toFixed(1)}" x2="${x(i).toFixed(1)}" y1="${pt}" y2="${H-pb}" stroke="var(--faint)" stroke-dasharray="3 3"/><circle cx="${x(i).toFixed(1)}" cy="${y(data[i]).toFixed(1)}" r="4" fill="var(--surface)" stroke="${color}" stroke-width="2"/>`;
  const bk=b[i],rows=ps.map(p=>({p,v:val(bk,p)})).filter(r=>r.v>0&&(!CH.focus||r.p===CH.focus)).sort((a,b)=>b.v-a.v);
  const f=v=>CH.metric==='lat'?Math.round(v)+' ms':CH.metric==='cost'?usd(v):fmt(v);
  setHtml(tip,`<div class="h">${longBuckets()?tickLabel(bk.t):clock(bk.t)+'–'+clock(bk.t+S.bw).slice(3)}</div>${rows.map(r=>`<div class="r"><span>${swp(r.p)}${esc(provName(r.p))}</span><span>${f(r.v)}</span></div>`).join('')||'<div class="r"><span>No requests</span><span></span></div>'}<div class="r tot"><span>${CH.focus?esc(provName(CH.focus)):'All providers'}</span><span>${f(data[i])}</span></div>`);
  const rect=svg.getBoundingClientRect(),left=x(i)*rect.width/W;tip.classList.add('on');const tw=tip.offsetWidth;tip.style.left=Math.max(0,left>rect.width/2?left-tw-14:left+14)+'px'}
 else tip.classList.remove('on');
 setHtml(svg,g)}
function bindChart(){const svg=$('#chart');if(!svg)return;
 svg.onpointermove=e=>{const r=svg.getBoundingClientRect(),W=svg.clientWidth,px=(e.clientX-r.left)*W/r.width;const N=ser().b.length,i=Math.max(0,Math.min(N-1,Math.round((px-46)/((W-60)/(N-1)))));if(i!==CH.hover){CH.hover=i;drawChart()}};
 svg.onpointerleave=()=>{CH.hover=null;drawChart()}}
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
function quotaBit(id){const q=quotaOf(id);if(!q)return '';
 if(q.kind==='balance'&&q.balance&&num(q.balance.amount)!=null)return `${esc(q.balance.currency||'')} ${q.balance.amount.toFixed(2)} left`;
 const w=arr(q.windows).find(x=>num(x.used_pct)!=null);return w?`${Math.round(w.used_pct)}% of ${esc(w.name)}`:''}
function lanesMini(){const rows=provRows().filter(x=>!x.rest).map(x=>({...x,status:provStatus(x)}));if(!rows.length)return `<div class="allclear">${ic('info')} No providers yet</div>`;
 return rows.map(x=>`<div class="li"><div class="grow"><div>${esc(x.name)}</div><div class="sm mono">${esc(x.pattern||'—')}</div></div><span class="mono muted">${[x.n+' req',quotaBit(x.id)].filter(Boolean).join(' · ')}</span>${pill(x.status[0],x.status[1])}</div>`).join('')+(unsetProviders()?`<div class="li quiet"><div class="grow"><div class="muted">${unsetProviders()} not set up (optional)</div></div></div>`:'')}
function jobPill(j){return j.state==='running'?pill('acc','Running'):j.state==='awaiting_review'?pill('warn','Review'):j.state==='queued'?pill('off','Queued'):j.state==='dead'?pill('bad','Dead'):j.state==='failed'?pill('bad','Failed'):j.state==='accepted'?pill('ok','Accepted'):pill('off',j.state||'—')}
const upCell=()=>num(SNAP.started_ms)!=null?dur(NOW-SNAP.started_ms):'—';
const cpuCell=()=>RT.cpu!=null?RT.cpu.toFixed(1)+'%':'—';
PAGES.overview={title:'Overview',body:()=>
 head('Overview',liveRange()?'What huddle sees right now, kept in memory.':`Requests, tokens and cost for ${rangeText()}, from the history database. Sessions, jobs and providers below stay live.`)+
 reg('rstat',rangeStatus)+reg('kpis',overviewKpis)+
 `<section>${secH('Needs you',reg('attn-n',()=>`<span class="note">${needsCount()} items</span>`),['needs-you','Show all'])}<div class="card">${reg('attn',attnList)}</div></section>`+
 `<section>${secH('Traffic',reg('bw',()=>`<span class="note">${liveRange()?'30-second':dur(ser().bw)} buckets, by provider</span>`),['requests','All requests'])}<div class="card chart">${reg('mtabs',mtabs)}<div class="chart-body"><div class="plot"><svg id="chart" role="img" aria-label="Traffic by provider over the selected range"></svg><div class="tip" id="tip"></div></div><div class="split">${reg('bars',bars)}</div></div></div></section>`+
 `<div class="g2e"><section>${secH('Providers',null,['providers','Details'])}<div class="card list">${reg('lanesmini',lanesMini)}</div></section>`+
 `<section>${secH('Top sessions',reg('sess-n',()=>`<span class="note">${SESS.filter(s=>sstate(s)==='working').length} working</span>`),['sessions','All sessions'])}<div class="card t">${reg('sessmini',()=>table('sessmini',SESS_COLS.filter(c=>['title','ctx','last'].includes(c.k)),SESS.filter(s=>sstate(s)==='working').sort((a,b)=>(lastT(b)||0)-(lastT(a)||0)).slice(0,5),{row:s=>'session:'+s.id,stack:1,empty:'None working'}))}</div></section></div>`,
 after:()=>{bindChart();drawChart()}};
