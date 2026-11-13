function speed(r){if(r.dur==null||r.ttft==null||!r.out||r.dur<=r.ttft)return null;return r.out/((r.dur-r.ttft)/1e3)}
const REQ_COLS=[
 {k:'time',label:'Time',v:r=>r.t,h:r=>`<span class="mono muted">${clock(r.t)}</span>`},
 {k:'status',label:'Status',v:r=>r.status==null?0:r.status,h:reqStatus},
 {k:'model',label:'Model',v:r=>r.m,h:r=>modelTag(r.m)},
 {k:'lane',label:'Lane',v:r=>r.lane,h:r=>`<span class="muted">${esc(r.lane)}</span>`,sm:1},
 {k:'who',label:'Session · agent',v:r=>r.s?r.s.project:'',h:who},
 {k:'ttft',label:'<span title="Time to first token">1st token</span>',num:1,v:r=>r.ttft||0,h:r=>`<span class="mono">${msf(r.ttft)}</span>`},
 {k:'dur',label:'Duration',num:1,v:r=>r.dur||NOW-r.t,h:r=>`<span class="mono">${r.dur!=null?(r.dur/1e3).toFixed(1)+' s':r.status==null?dur(NOW-r.t):'—'}</span>`,sm:1},
 {k:'speed',label:'Speed',num:1,v:r=>speed(r)||0,h:r=>`<span class="mono">${speed(r)?Math.round(speed(r))+' tok/s':'—'}</span>`,sm:1},
 {k:'in',label:'In',num:1,v:r=>n0(r.in),h:r=>`<span class="mono">${fmt(r.in)}</span>`},
 {k:'cr',label:'Cache read',num:1,v:r=>n0(r.cr),h:r=>`<span class="mono">${fmt(r.cr)}</span>`,sm:1},
 {k:'out',label:'Out',num:1,v:r=>n0(r.out),h:r=>`<span class="mono">${r.status==null?'…':fmt(r.out)}</span>`},
 {k:'stop',label:'Stop',v:r=>r.stop||'',h:r=>`<span class="mono muted">${esc(r.stop||'—')}</span>`,sm:1}
];
function reqFilter(r){const f=F.req;const okS=f==='all'||(f==='ok'&&r.status===200)||(f==='err'&&isErr(r))||(f==='cancel'&&r.status===499)||(f==='flight'&&r.status==null);
 return okS&&(F.lane==='all'||r.lane===F.lane)&&qmatch('req',r.m+' '+(r.s?r.s.project+' '+r.s.id:'')+' '+(r.a?r.a.desc+' '+r.a.type:'')+' '+r.id+' '+(r.up||''))}
/* ---------- search over the kept history (Requests and Errors) ---------- */
const HPAGE=100,HFIL=['model','lane','session','agent','project','status'],HSTAT=[['any','Any status'],['ok','OK'],['error','Error'],['left','Client left']];
const HS={mode:'live',f:{},off:0,data:null,rows:[],err:false,seq:0,t:0};
const hdef=p=>({status:p==='errors'?'error':'any'});
const hrow=(x,i)=>{const cr=n0(x.cache_read),cw=n0(x.cache_write),ti=num(x.tokens_in);
 return {h:1,id:'h'+(x.upstream_id||x.at_ms+'-'+i),t:num(x.at_ms),status:num(x.status),reason:x.reason||null,m:x.model||'',lane:x.lane||'',ttft:num(x.ttft_ms),dur:num(x.dur_ms),rt:null,
  in:ti==null?null:Math.max(0,ti-cr-cw),cr,cw,out:num(x.tokens_out),stop:x.stop||null,up:x.upstream_id||null,cost:num(x.cost_usd),
  s:x.session?{id:x.session,sid:x.session,name:x.session,project:x.project||''}:null,a:x.agent?{type:x.agent,desc:''}:null}};
const HCOLS=REQ_COLS.concat([{k:'cost',label:'Cost',num:1,v:r=>n0(r.cost),h:r=>r.cost==null?NA:costCell({cost_usd:r.cost,api_equivalent:r.lane==='anthropic'})}]);
/* the filters and the page live in the hash: #requests?mode=history&model=glm-5.3&offset=100 */
function hload(p){const q=new URLSearchParams((location.hash.split('?')[1])||'');HS.mode=q.get('mode')==='history'?'history':'live';
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
  HFIL.filter(k=>k!=='status').map(k=>`<input class="hin" data-hf="${k}" placeholder="${k[0].toUpperCase()+k.slice(1)}" value="${esc(HS.f[k]||'')}" aria-label="Filter by ${k}">`).join('')+`</div>`}
function hresults(){const d=HS.data,n=HS.rows.length,tot=d?n0(d.total):0;
 const note=HS.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:d?'':`<div class="legendrow">${checking()}</div>`;
 return table('hreq',HCOLS,HS.rows,{row:r=>'request:'+r.id,empty:d?'No requests match these filters':HS.err?'No data':'Loading…'})+note+clampNote(d)+
  `<div class="hpager"><button data-hp="prev"${HS.off>0?'':' disabled'}>Prev</button><span class="mono">${n?`${HS.off+1}–${HS.off+n}`:'0'} of ${tot}</span><button data-hp="next"${d&&HS.off+n<tot?'':' disabled'}>Next</button></div>`+pricesLine(d)}
const hview=(t,desc)=>head(t,desc,hmodes())+hbar()+`<div class="card t">${reg('hist',hresults)}</div>`;
PAGES.requests={title:'Requests',body:()=>HS.mode==='history'?hview('Requests',`Every request in ${rangeText()}, newest first. Prompt text, tool output and header values never reach this page.`):head('Requests','The last 200 model requests through the router, newest first. Prompt text, tool output and header values never reach this page.',
 hmodes()+seg('req',[['all','All'],['ok','200'],['err','Errors'],['cancel','Cancelled'],['flight','In flight']],F.req)+reg('lanesel',()=>seg('lane',[['all','All lanes']].concat(LANES.map(l=>[l.id,l.id])),F.lane))+search('req','Filter by model, project or agent'))+
 `<div class="card t">${reg('req',()=>table('req',REQ_COLS,REQS.slice().reverse().slice(0,200).filter(reqFilter),{row:r=>'request:'+r.id,sort:{k:'time',d:-1},empty:'No requests match'}))}</div>`};
