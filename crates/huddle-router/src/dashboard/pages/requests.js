function speed(r){if(r.dur==null||r.ttft==null||!r.out||r.dur<=r.ttft)return null;return r.out/((r.dur-r.ttft)/1e3)}
const REASON={quota:'quota used up',overloaded_error:'overloaded',rate_limit_error:'rate limited',upstream:'upstream failed'};
/* why a request failed, in plain words; a cancelled request already says so in its status */
function reasonChip(r){if(r.status==null||r.status===200||r.status===499)return '';
 const t=r.status===429&&r.reason==='quota'?'quota used up':r.status===529?'overloaded':r.status===429?'rate limited':REASON[r.reason]||r.reason||'';
 return t?`<div class="cs">${pill(r.status===429?'warn':'bad',t)}</div>`:''}
function whoCell(r){const last=x=>String(x).split(':').pop(),ag=r.a?(r.a.type==='main'?'main':r.a.desc||last(r.a.type)):r.agentId?last(r.agentId):'—';
 const s=r.s?`${esc(r.s.project||r.s.name)} <span class="mono faint">${esc(String(r.s.sid).slice(0,8))}</span>`:NA;
 return `<div>${s}</div><div class="cs sans">${esc(ag)}${r.up?` · <span class="mono">${esc(r.up)}</span>`:''}</div>`}
/* live rows carry no price; the last 15 minutes of history do, keyed by upstream id */
const RC={map:{},at:0,busy:false};
function loadReqCost(){if(RC.busy||Date.now()-RC.at<15e3)return;RC.busy=true;
 hget(HQ+'search&range=15m&limit=500').then(d=>{const m={};arr(d.rows).forEach(x=>{if(x.upstream_id&&num(x.cost_usd)!=null)m[x.upstream_id]=x.cost_usd});RC.map=m;RC.at=Date.now();RC.busy=false;paint()},()=>{RC.at=Date.now();RC.busy=false})}
const reqCost=r=>r.cost!=null?r.cost:r.up&&RC.map[r.up]!=null?RC.map[r.up]:null;
const REQ_COLS=[
 {k:'time',label:'Time',v:r=>r.t,h:r=>`<span class="mono muted">${r.h?when(r.t):clock(r.t)}</span>`},
 {k:'status',label:'Status',v:r=>r.status==null?0:r.status,h:r=>reqStatus(r)+reasonChip(r)},
 {k:'lane',label:'Provider',v:r=>r.lane,h:r=>`<span class="muted">${esc(provName(r.lane))}</span>`},
 {k:'model',label:'Model',v:r=>r.m,h:r=>modelTag(r.m)},
 {k:'who',label:'Session · agent · upstream id',v:r=>r.s?r.s.project:'',h:whoCell},
 {k:'router',label:'Router',num:1,v:r=>r.rt||0,h:r=>`<span class="mono">${r.rt==null?'—':r.rt<1?'<1 ms':r.rt+' ms'}</span>`,sm:1},
 {k:'ttft',label:'<span title="Time to first token">1st token</span>',num:1,v:r=>r.ttft||0,h:r=>`<span class="mono">${msf(r.ttft)}</span>`},
 {k:'in',label:'In',num:1,v:r=>n0(r.in),h:r=>`<span class="mono">${fmt(r.in)}</span>`},
 {k:'out',label:'Out',num:1,v:r=>n0(r.out),h:r=>`<span class="mono">${r.status==null?'…':fmt(r.out)}</span>`},
 {k:'cost',label:'API price',num:1,v:r=>n0(reqCost(r)),h:r=>{const c=reqCost(r);return c==null?NA:costCell({cost_usd:c,api_equivalent:r.lane==='anthropic'})}}
];
const lanesWithTraffic=()=>[...new Set(REQS.map(r=>r.lane).filter(Boolean).concat(F.lane==='all'?[]:[F.lane]))].sort();
/* The live list pauses while the page is scrolled down, so a row never moves under the reader. */
const TAIL={paused:false,cut:0,top:200};
function tailRows(rows){return TAIL.paused?rows.filter(r=>r.t<=TAIL.cut):rows}
const tailNew=()=>TAIL.paused?REQS.filter(r=>r.t>TAIL.cut&&reqFilter(r)).length:0;
const tailBar=()=>HS.mode==='live'&&TAIL.paused?`<div class="tailbar" role="status">Paused while you read. <button type="button" data-tail="jump">${tailNew()} new</button></div>`:'';
window.addEventListener('scroll',()=>{if(page!=='requests'||HS.mode!=='live')return;const away=window.scrollY>TAIL.top;
 if(away&&!TAIL.paused){TAIL.paused=true;TAIL.cut=Math.max(0,...REQS.map(r=>r.t));paint()}else if(!away&&TAIL.paused){TAIL.paused=false;paint()}});
document.addEventListener('click',e=>{if(!e.target.closest('[data-tail="jump"]'))return;TAIL.paused=false;window.scrollTo(0,0);paint()});
function reqFilter(r){const f=F.req;const okS=f==='all'||(f==='ok'&&r.status===200)||(f==='err'&&isErr(r))||(f==='cancel'&&r.status===499)||(f==='flight'&&r.status==null);
 return okS&&(F.lane==='all'||r.lane===F.lane)&&qmatch('req',r.m+' '+(r.s?r.s.project+' '+r.s.id:'')+' '+(r.a?r.a.desc+' '+r.a.type:'')+' '+r.id+' '+(r.up||''))}
/* ---------- search over the kept history (Requests and Errors) ---------- */
const HPAGE=100,HFIL=['model','lane','session','agent','project','status'],HSTAT=[['any','Any status'],['ok','OK'],['error','Error'],['left','Client left']];
const HS={mode:'live',f:{},off:0,data:null,rows:[],err:false,seq:0,t:0};
const hdef=p=>({status:p==='errors'?'error':'any'});
const hrow=(x,i)=>{const cr=n0(x.cache_read),cw=n0(x.cache_write),ti=num(x.tokens_in);
 return {h:1,id:'h'+(x.upstream_id||x.at_ms+'-'+i),t:num(x.at_ms),status:num(x.status),reason:x.reason||null,m:x.model||'',lane:x.lane||'',ttft:num(x.ttft_ms),dur:num(x.dur_ms),rt:null,
  in:ti==null?null:Math.max(0,ti-cr-cw),cr,cw,out:num(x.tokens_out),stop:x.stop||null,up:x.upstream_id||null,agentId:x.agent||null,cost:num(x.cost_usd),
  s:x.session?{id:x.session,sid:x.session,name:x.session,project:x.project||''}:null,a:x.agent?{type:x.agent,desc:''}:null}};
/* the filters and the page live in the hash: #requests?mode=history&model=glm-5.3&offset=100 */
function hload(p){const q=new URLSearchParams((location.hash.split('?')[1])||'');HS.mode=(q.get('mode')||(liveRange()?'live':'history'))==='history'?'history':'live';
 if(HS.mode!=='history')return;
 const d=hdef(p);
 HS.f={};HFIL.forEach(k=>{HS.f[k]=q.has(k)?q.get(k):d[k]||''});
 HS.off=Math.max(0,parseInt(q.get('offset'),10)||0);HS.data=null;HS.rows=[];HS.err=false;hsearch()}
function hhash(){const q=new URLSearchParams({mode:'history'}),d=hdef(page);
 HFIL.forEach(k=>{if(HS.f[k]&&HS.f[k]!==d[k])q.set(k,HS.f[k])});if(HS.off)q.set('offset',HS.off);
 history.replaceState(null,'','#'+page+'?'+q)}
function hsearch(){clearTimeout(HS.t);hhash();const n=++HS.seq;
 hget(HQ+'search&'+rangeQuery()+HFIL.filter(k=>HS.f[k]&&HS.f[k]!=='any').map(k=>'&'+k+'='+encodeURIComponent(HS.f[k])).join('')+'&limit='+HPAGE+'&offset='+HS.off).then(d=>{if(n!==HS.seq)return;
  HS.data=d;HS.rows=arr(d.rows).map(hrow);HS.err=false;paint()},()=>{if(n!==HS.seq)return;HS.err=true;paint()})}
const hmodes=()=>seg('hmode',[['live','Live'],['history','History']],HS.mode);
function hbar(){const o=(l,c)=>l.map(([k,t])=>`<option value="${esc(k)}"${k===c?' selected':''}>${esc(t)}</option>`).join('');
 return `<div class="hbar"><select data-hf="status" aria-label="Status">${o(HSTAT,HS.f.status)}</select>`+
  HFIL.filter(k=>k!=='status').map(k=>`<input class="hin" data-hf="${k}" placeholder="${k==='lane'?'Provider':k[0].toUpperCase()+k.slice(1)}" value="${esc(HS.f[k]||'')}" aria-label="Filter by ${k==='lane'?'provider':k}">`).join('')+`</div>`}
function hresults(){const d=HS.data,n=HS.rows.length,tot=d?n0(d.total):0;
 const note=HS.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:d?'':`<div class="legendrow">${checking()}</div>`;
 return table('hreq',REQ_COLS,HS.rows,{row:r=>'request:'+r.id,stack:1,empty:d?'No requests match these filters':HS.err?'No data':'Loading…'})+note+clampNote(d)+
  `<div class="hpager"><button data-hp="prev"${HS.off>0?'':' disabled'}>Prev</button><span class="mono">${n?`${HS.off+1}–${HS.off+n}`:'0'} of ${tot}</span><button data-hp="next"${d&&HS.off+n<tot?'':' disabled'}>Next</button></div>`+pricesLine(d)}
const hview=(t,desc)=>head(t,desc,hmodes())+hbar()+`<div class="card t">${reg('hist',hresults)}</div>`;
PAGES.requests={title:'Requests',body:()=>HS.mode==='history'?hview('Requests',`Every request in ${rangeText()}, newest first. Prompt text, tool output and header values never reach this page.`):head('Requests','The last 200 model requests through the router, newest first. Prompt text, tool output and header values never reach this page.',
 hmodes()+seg('req',[['all','All'],['ok','200'],['err','Errors'],['cancel','Cancelled'],['flight','In flight']],F.req)+reg('lanesel',()=>seg('lane',[['all','All providers']].concat(lanesWithTraffic().map(id=>[id,provName(id)])),F.lane))+search('req','Filter by model, project or agent'))+
 reg('tail',tailBar)+`<div class="card t">${reg('req',()=>{loadReqCost();return table('req',REQ_COLS,tailRows(REQS).slice().reverse().slice(0,200).filter(reqFilter),{row:r=>'request:'+r.id,sort:{k:'time',d:-1},stack:1,empty:'No requests match'})})}</div>`,after:()=>{TAIL.paused=false}};
