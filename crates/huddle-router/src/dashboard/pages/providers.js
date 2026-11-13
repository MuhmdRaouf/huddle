const PNAME={zai:'Z.ai GLM',kimi:'Kimi',deepseek:'DeepSeek',minimax:'MiniMax',qwen:'Qwen'};
const FAIL_RATE=0.2;
const CAUSE={quota:'quota used up'};
/* totals by provider for the picked range, and the latency buckets that belong to them */
const HP={q:null,at:0,lane:null,bk:null,err:false};
function loadProviders(){const q=rangeQuery();if(HP.q===q&&HP.lane&&Date.now()-HP.at<6e4)return;
 if(HP.q!==q){HP.lane=HP.bk=null;HP.err=false}HP.q=q;
 Promise.all([hget(HQ+'totals&by=lane&'+q),liveRange()?null:hget(HQ+'buckets&'+q)]).then(([t,b])=>{if(HP.q!==q)return;HP.lane=hmap(t);HP.bk=b;HP.at=Date.now();HP.err=false;paint()},()=>{if(HP.q!==q)return;HP.err=true;paint()})}
/* Live facts decide the status, in this order: quota, error rate, wiring. A provider with traffic is never Not set up. */
function provStatus(x){
 if(x.quotaOut)return ['bad','Out of quota'];
 if(x.n&&x.err/x.n>FAIL_RATE)return ['bad','Failing'];
 if(!x.built&&(x.n||x.laneOn)&&!x.key)return ['bad','Needs a fix'];
 if(!x.built&&x.key&&!x.laneOn)return ['bad','Needs a fix'];
 return x.built||x.laneOn?['ok','Working']:['off','Not set up'];
}
const resetAt=t=>new Date(t).toDateString()===new Date(NOW).toDateString()?clock(t).slice(0,5):when(t);
const provConsole=id=>{const p=SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers[id]:null;return p&&typeof p.console_url==='string'?p.console_url:null};
const provQuota=id=>{const p=SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers[id]:null;return p&&p.quota&&typeof p.quota==='object'?p.quota:null};
/* a window is used up while its reset is still ahead; a balance at zero is used up */
function quotaState(q){
 if(!q)return {out:false,reset:null};
 const full=arr(q.windows).filter(w=>num(w.used_pct)!=null&&w.used_pct>=100&&(num(w.resets_ms)==null||w.resets_ms>NOW));
 const dry=!!q.balance&&num(q.balance.amount)!=null&&q.balance.amount<=0;
 const ts=full.map(w=>num(w.resets_ms)).filter(t=>t!=null);
 return {out:full.length>0||dry,reset:ts.length?Math.min(...ts):null}}
const lastIsQuota=id=>{const rs=stats().byLane[id]||[],l=rs[rs.length-1];return !!l&&l.status===429&&l.reason==='quota'};
/* the finished requests of a lane that failed, grouped by the router's reason */
function causes(id){const g={};(stats().byLane[id]||[]).filter(isErr).forEach(r=>{const k=r.reason||String(r.status);g[k]=(g[k]||0)+1});return Object.entries(g).sort((a,b)=>b[1]-a[1])}
function histLatency(t){const m={};arr(t.models).forEach(x=>{m[x.model]=1});let w=0,c=0,p95=null;
 arr(HP.bk&&HP.bk.buckets).forEach(b=>{if(!m[b.model]||num(b.ttft_p50)==null)return;w+=b.ttft_p50*n0(b.n);c+=n0(b.n);if(num(b.ttft_p95)!=null)p95=Math.max(p95||0,b.ttft_p95)});
 return {p50:c?w/c:null,p95}}
/* the live range reads the snapshot's lane; every other range reads history */
function laneNumbers(id){
 const t=HP.lane&&HP.lane[id],cost=t?{cost:num(t.cost_usd)}:{cost:null};
 if(liveRange()){const l=arr(SNAP.lanes).find(x=>x.id===id);
  return l?{n:n0(l.requests),err:n0(l.errors),p50:num(l.ttft_p50),p95:num(l.ttft_p95),tin:n0(l.tokens_in),tout:n0(l.tokens_out),...cost}:{n:0,err:0,p50:null,p95:null,tin:0,tout:0,...cost}}
 if(!t)return {n:0,err:0,p50:null,p95:null,tin:0,tout:0,cost:null};
 return {n:n0(t.n),err:n0(t.err),tin:n0(t.tok_in),tout:n0(t.tok_out),...histLatency(t),...cost}}
function provRows(){
 const mk=(id,r)=>{const La=LANES.find(l=>l.id===id)||null,built=id==='anthropic',q=provQuota(id),qs=quotaState(q);
  const x={id,name:(La&&La.name)||PNAME[id]||id,built,key:built||(!!r&&r.key===true),
   pattern:La?La.match:r&&r.lane||null,laneOn:built||!!(r&&r.lane)||(!!La&&La.state==='live'),
   models:La?La.models:[],nModels:r?n0(r.models):0,nAgents:r?n0(r.agents):0,
   q,console:provConsole(id),reset:qs.reset,quotaOut:qs.out||lastIsQuota(id),causes:liveRange()?causes(id):[],next:r&&r.next||null,...laneNumbers(id)};
  x.status=provStatus(x);if(!x.next&&x.status[1]==='Needs a fix'&&!built)x.next=`/huddle:${id}:setup`;
  return x};
 const all=[...PL.rows.map(r=>mk(r.name,r)),mk('anthropic',null)];
 all.forEach(x=>{x.rest=x.status[0]==='off'&&!x.n});
 const rest=all.filter(x=>x.rest),live=all.filter(x=>!x.rest);
 rest.forEach((x,i)=>{x.first=i===0});
 const ordered=[...live.filter(x=>x.id==='anthropic'),...live.filter(x=>x.id!=='anthropic'),...rest];
 return ordered}
const provSetUp=()=>provRows().filter(x=>!x.built&&x.key&&x.laneOn).length;
const provTotal=()=>PL.rows.length;
const errPct=x=>x.n?`${(x.err/x.n*100).toFixed(1)}%`:'';
const causeText=([k,n])=>`${n} ${CAUSE[k]||k}`;
const errCell=x=>!x.n?NA:x.err?`<span class="badc"><b>${x.err.toLocaleString('en-US')}</b> · ${errPct(x)}</span>${x.causes.length?`<div class="cs cw">${esc(x.causes.map(causeText).join(' · '))}</div>`:''}`:'<span class="faint">0</span>';
const statusCell=x=>`${pill(x.status[0],x.status[1])}${x.status[1]==='Out of quota'?`<div class="cs cw">${x.reset?`quota used up, resets ${resetAt(x.reset)}`:'quota used up'}</div>`:x.status[1]==='Failing'?`<div class="cs cw">${errPct(x)} of requests failed</div>`:''}`;
const quotaLine=w=>num(w.used_pct)==null?`<div class="qw"><span>${esc(w.name)}</span> <span class="faint">${esc(w.note||'not reported')}</span></div>`
 :`<div class="qw"><span>${esc(w.name)}</span> <b class="${w.used_pct>=90?'badc':w.used_pct>=80?'warnc':''}">${Math.round(w.used_pct)}%</b>${num(w.resets_ms)!=null?` <span class="faint">resets ${resetAt(w.resets_ms)}</span>`:''}</div>`;
function quotaCell(x){const q=x.q;
 if(x.status[0]==='off'||!q)return NA;
 return `<div${num(q.fetched_ms)!=null?` title="Checked at ${clock(q.fetched_ms).slice(0,5)}"`:''}>${quotaBody(x,q)}</div>`}
function quotaBody(x,q){
 if(q.kind==='none')return x.console?`<a class="lnk" href="${esc(x.console)}" target="_blank" rel="noopener">Open its console</a>`:'<span class="faint">no quota API</span>';
 const un=q.unverified?' <span class="faint">(unverified)</span>':'';
 if(q.error&&!arr(q.windows).length&&!q.balance)return `<span class="faint">${esc(q.error)}</span>`;
 if(q.kind==='balance'&&q.balance)return `<span class="mono">${num(q.balance.amount)==null?'—':q.balance.amount.toFixed(2)} ${esc(q.balance.currency||'')}</span>${un}`;
 if(arr(q.windows).length)return arr(q.windows).map(quotaLine).join('')+un;
 return '<span class="faint">quota unknown</span>'}
const nextHint=x=>x.status[1]==='Out of quota'?'<div class="cs cw sans">Wait for the reset or add credit</div>':x.status[1]==='Failing'?'<a class="lnk" href="#errors">See the errors</a>':'';
const priceCell=x=>x.cost==null?(x.n&&!HP.lane?checking():NA):x.n&&x.tin&&!x.cost?'<span class="faint">unpriced model</span>':`<span class="mono">${usd(x.cost)}</span>`;
const servedCell=x=>{const m=x.models.length||x.nModels;return `${m} model${m===1?'':'s'}${x.built?'':` · ${x.nAgents} agent${x.nAgents===1?'':'s'}`}`};
const provStat=()=>liveRange()?'':HP.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:HP.lane?'':`<div class="legendrow">${checking()}</div>`;
const errSplit=r=>{const e=r.filter(x=>x.err).sort((a,b)=>b.err-a.err);return e.length?e.slice(0,3).map(x=>`${esc(x.name)} ${x.err.toLocaleString('en-US')}`).join(' · '):'no errors'};
PAGES.providers={title:'Providers',after:loadProviders,body:()=>head('Providers','Where your model requests go, and what each provider still needs.',reg('provn',()=>PL.rows.length?`<span class="note">${provSetUp()}/${provTotal()} set up</span>`:''))+reg('pstat',provStat)+
 `<div class="card kpis k3">${reg('provk',()=>{const r=provRows(),n=sum(r.map(x=>x.n)),e=sum(r.map(x=>x.err));return kpiGrid([
  {k:'Set up',v:PL.rows.length?provSetUp():'—',small:PL.rows.length?`of ${provTotal()}`:'',d:PL.rows.length?'with a key and a lane':'Checking…'},
  {k:'Requests',v:n.toLocaleString('en-US'),d:`in ${rangeText()}, every provider`},
  {k:'Errors',v:e.toLocaleString('en-US'),d:n?`${(e/n*100).toFixed(1)}% of requests · ${errSplit(r)}`:'no requests in range'}])})}</div>`+
 `<div class="card t">${reg('lanes',()=>table('lanes',[
  {k:'name',label:'Provider',h:x=>`<div class="ct">${esc(x.name)}</div>${x.pattern?`<div class="cs">${esc(x.pattern)}</div>`:''}<div class="cs sans">${servedCell(x)}</div>`},
  {k:'status',label:'Status and next step',cls:'stc',h:x=>`<div class="stw">${statusCell(x)}${x.next?cmd(x.next):nextHint(x)}</div>`},
  {k:'quota',label:'Plan or balance',h:quotaCell},
  {k:'n',label:'Requests',num:1,h:x=>x.n?`<b>${x.n.toLocaleString('en-US')}</b>`:NA},
  {k:'err',label:'Errors',h:errCell},
  {k:'ttft',label:'Time to first token',num:1,h:x=>x.n&&x.p50!=null?`<span class="mono">${msf(x.p50)}</span>${x.p95!=null?`<div class="cs">p95 ${msf(x.p95)}</div>`:''}`:NA},
  {k:'tok',label:'Tokens',num:1,h:x=>x.n?`<span class="mono">${fmt(x.tin)} <span class="faint">/</span> ${fmt(x.tout)}</span>`:NA},
  {k:'cost',label:'API price',num:1,h:priceCell}
 ],provRows(),{stack:1,row:x=>'provider:'+x.id,cls:x=>x.rest?x.first?'dim sep':'dim':'',empty:PL.state==='checking'?checking():'No providers found'}))}<div class="legendrow"><span><b>Working</b> its lane answers</span><span><b>Out of quota</b> the plan or balance is used up</span><span><b>Failing</b> over 20% of requests in range failed</span><span><b>Needs a fix</b> traffic or a lane without a key, or a key without a lane</span><span><b>Not set up</b> optional; run the command to add it</span></div><div class="legendrow"><span>Time to first token reads p50 / p95. Tokens read in / out. API price is the list price, not what a plan charges.</span></div></div>`+
 `<div class="notebox">${ic('info')}<span>Keys stay in the operating system's key store. This page only learns whether a key exists, never its value.</span></div>`};
