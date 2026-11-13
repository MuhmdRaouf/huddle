/* The rows behind the page: the live memory in the 15 minute range, the kept history in every other range. */
const EG={q:null,at:0,rows:[],total:0,red:[],err:false};
function loadErrors(){if(liveRange())return;const q=rangeQuery();if(EG.q===q&&Date.now()-EG.at<6e4)return;
 if(EG.q!==q){EG.rows=[];EG.red=[];EG.total=0;EG.err=false}EG.q=q;
 Promise.all([hget(HQ+'search&'+q+'&status=error&limit=500'),hget(HQ+'totals&by=reason&'+q)]).then(([d,r])=>{if(EG.q!==q)return;EG.rows=arr(d.rows).map(hrow);EG.red=arr(r&&r.totals);EG.total=n0(d.total);EG.at=Date.now();EG.err=false;paint()},()=>{if(EG.q!==q)return;EG.err=true;paint()})}
const errRows=()=>liveRange()?stats().done.filter(r=>r.status!==200):EG.rows;
const errSess=r=>r.sess||(r.s&&r.s.id)||null;
const causeName=(code,reason)=>reason==='quota'?'Quota used up':code===529?'Overloaded':code===429?'Throttled':code===502?'Upstream failed':code===499?'Cancelled':code===401||code===403?'Key refused':errDef(code,reason)[0];
/* what to do about one cause; a used-up quota says when it resets */
function fixText(g){
 if(g.reason==='quota'){const t=quotaState(provQuota(g.lane)).reset;return {head:t?`quota used up, resets ${resetAt(t)}`:'quota used up',tail:'Wait for the reset, or pick a model from another provider.'}}
 if(g.code===529)return {head:'',tail:'Anthropic is busy. Claude Code retries; nothing to fix.'};
 if(g.code===429)return {head:'',tail:'The provider is throttling. Claude Code backs off and retries.'};
 if(g.code===502)return {head:'',tail:'The router could not reach the provider. Check the network and the provider status page.'};
 if(g.code===401||g.code===403)return {head:'',tail:g.lane==='anthropic'?'Sign in to Claude Code again.':`The key was refused. Run /huddle:${g.lane}:setup to enter a new one.`};
 if(g.code===499)return {head:'',tail:'You stopped the stream. Not an error.'};
 return {head:'',tail:errDef(g.code,g.reason)[2]}}
/* one group per provider and reason */
function errGroups(rows){const o={};
 rows.forEach(r=>{const k=r.lane+'|'+(r.reason||r.status);(o[k]=o[k]||[]).push(r)});
 return Object.values(o).map(rs=>{const code=rs[0].status,reason=rs.find(r=>r.reason)?rs.find(r=>r.reason).reason:null,ts=rs.map(r=>r.t);
  return {lane:rs[0].lane,code,reason,n:rs.length,first:Math.min(...ts),last:Math.max(...ts),sessions:new Set(rs.map(errSess).filter(Boolean)).size,models:[...new Set(rs.map(r=>r.m))]}})}
/* a longer range groups the reason totals, which count every error; the loaded rows add the sessions hit */
function errGroupsHist(){const sess={};errGroups(EG.rows).forEach(g=>{sess[g.lane+'|'+(g.reason||g.code)]=g.sessions});
 return EG.red.map(r=>({lane:r.lane,code:r.status,reason:r.reason||null,n:n0(r.n),first:n0(r.first_ms),last:n0(r.last_ms),sessions:sess[r.lane+'|'+(r.reason||r.status)]??null,models:arr(r.models)}))}
const errGroupRows=()=>liveRange()?errGroups(errRows()):errGroupsHist();
/* error rate per provider over time, from the live buckets or the history buckets */
function errSeries(){
 const laneOf={};
 if(liveRange())MODELS.forEach(m=>{laneOf[m.id]=m.lane});
 else Object.values(HP.lane||{}).forEach(t=>arr(t.models).forEach(m=>{laneOf[m.model]=t.key}));
 const cols=[];
 if(liveRange())BK.forEach(k=>{const c={t:k.t,by:{}};Object.entries(k.byM).forEach(([m,o])=>{const l=laneOf[m];if(!l)return;const x=c.by[l]=c.by[l]||{n:0,err:0};x.n+=o.n;x.err+=o.err});cols.push(c)});
 else{const at={};arr(HP.bk&&HP.bk.buckets).forEach(b=>{const l=laneOf[b.model];if(!l)return;const c=at[b.start_ms]=at[b.start_ms]||{t:b.start_ms,by:{}};const x=c.by[l]=c.by[l]||{n:0,err:0};x.n+=n0(b.n);x.err+=n0(b.err)});
  Object.values(at).sort((a,b)=>a.t-b.t).forEach(c=>cols.push(c))}
 const tot={};cols.forEach(c=>Object.entries(c.by).forEach(([l,x])=>{const y=tot[l]=tot[l]||{n:0,err:0};y.n+=x.n;y.err+=x.err}));
 const lanes=Object.keys(tot).filter(l=>tot[l].n>0).sort();
 return {cols,lanes,tot,rate:l=>cols.map(c=>c.by[l]&&c.by[l].n?c.by[l].err/c.by[l].n:0)}}
/* a small rate keeps two decimals so 7 errors in 20,000 requests does not read 0.0% */
const errPctText=t=>{if(!t.n)return '0.0%';const p=t.err/t.n*100;return (p>0&&p<0.1?p.toFixed(2):p.toFixed(1))+'%'};
function errChart(){const S=errSeries();
 if(!S.lanes.length||S.cols.length<2)return `<div class="legendrow bare">${liveRange()||HP.bk||HP.err?`No requests in ${rangeText()}`:checking()}</div>`;
 const rates=S.lanes.map(l=>S.rate(l)),top=Math.max(.05,...rates.flat()),n=S.cols.length;
 const line=(v,i)=>`<path d="${v.map((x,j)=>(j?'L':'M')+(j/(n-1)*600).toFixed(1)+' '+(130-x/top*120).toFixed(1)).join(' ')}" fill="none" stroke="var(--c${i%6+1})" stroke-width="2" vector-effect="non-scaling-stroke" stroke-linejoin="round"/>`;
 const lg=S.lanes.map((l,i)=>{const t=S.tot[l];return `<span><span class="sw errsw${i%6+1}"></span>${esc(provName(l))} <b>${errPctText(t)}</b> <span class="faint">${t.err.toLocaleString('en-US')} of ${t.n.toLocaleString('en-US')}</span></span>`}).join('');
 return `<div class="errch"><div class="eyt"><span>${(top*100).toFixed(0)}%</span><span>0%</span></div><svg viewBox="0 0 600 134" preserveAspectRatio="none" role="img" aria-label="Error rate per provider, ${esc(rangeText())}"><path d="M0 130 L600 130" stroke="var(--line)" stroke-width="1" vector-effect="non-scaling-stroke"/>${rates.map(line).join('')}</svg></div><div class="legendrow"><span class="faint">Range average, ${esc(rangeText())}:</span>${lg}</div>`}
const errCols=[
 {k:'lane',label:'Provider',v:g=>g.lane,h:g=>`<b class="fw5">${esc(provName(g.lane))}</b><div class="cs">${g.models.map(esc).join(', ')}</div>`},
 {k:'cause',label:'Cause',v:g=>causeName(g.code,g.reason),h:g=>`<div class="ct">${esc(causeName(g.code,g.reason))}</div><div class="cs">${g.code===499?pill('off','499'):g.code===429?pill('warn','429'):pill('bad',String(g.code))}</div>`},
 {k:'n',label:'Count',num:1,v:g=>g.n,h:g=>`<b>${g.n.toLocaleString('en-US')}</b>`},
 {k:'first',label:'First seen',num:1,v:g=>-g.first,h:g=>`<span class="muted">${liveRange()?ago(g.first):when(g.first)}</span>`,sm:1},
 {k:'last',label:'Last seen',num:1,v:g=>-g.last,h:g=>`<span class="muted">${liveRange()?ago(g.last):when(g.last)}</span>`},
 {k:'sess',label:'Sessions hit',num:1,v:g=>g.sessions||0,h:g=>g.sessions==null?NA:g.sessions,sm:1},
 {k:'fix',label:'Fix',h:g=>{const f=fixText(g);return `${f.head?`<div class="warnc">${esc(f.head)}</div>`:''}<div class="cs cw fixt">${esc(f.tail)}</div>`}}
];
const errNote=()=>liveRange()?'':EG.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:EG.q&&EG.at?(EG.total>EG.rows.length?`<div class="legendrow">The counts cover all ${EG.total.toLocaleString('en-US')} errors in ${rangeText()}; sessions hit and the Recent list come from the latest ${EG.rows.length.toLocaleString('en-US')}.</div>`:''):`<div class="legendrow">${checking()}</div>`;
PAGES.errors={title:'Errors',after:()=>{loadProviders();loadErrors()},body:()=>head('Errors',`Failed requests in ${rangeText()}, grouped by provider and cause. A cancelled request means you stopped the stream; it is not an error.`)+
 `<section>${secH('By provider and cause')}<div class="card t">${reg('errg',()=>table('errg',errCols,errGroupRows(),{sort:{k:'n',d:-1},stack:1,empty:liveRange()||EG.at?`No errors in ${rangeText()}`:'Checking…'})+errNote())}</div></section>`+
 `<section>${secH('Error rate per provider')}<div class="card">${reg('errch',errChart)}</div></section>`+
 `<section>${secH('Recent')}<div class="card t">${reg('errr',()=>table('errr',REQ_COLS.filter(c=>['time','status','lane','model','who'].includes(c.k)),errRows().slice().sort((a,b)=>b.t-a.t),{row:r=>'request:'+r.id,limit:40,stack:1,empty:liveRange()||EG.at?`No errors in ${rangeText()}`:'Checking…'}))}</div></section>`+
 `<details class="fold"><summary>What each cause means</summary><div class="card list">${Object.entries(ERRDEF).map(([c,d])=>`<div class="li"><span class="mono w36">${c}</span><div class="grow"><div><b class="fw5">${d[0]}</b> <span class="mono faint">${d[1]}</span></div><div class="sm wrap">${d[2]}</div></div></div>`).join('')}</div></details>`};
