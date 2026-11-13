/* history totals for the picked range, read for every range (the 15 min one included) */
const HU={q:null,at:0,day:null,model:null,proj:null,err:false};
function loadUsage(){const q=rangeQuery();if(HU.q===q&&HU.day&&Date.now()-HU.at<6e4)return;
 if(HU.q!==q){HU.day=HU.model=HU.proj=null;HU.err=false}HU.q=q;
 Promise.all([hget(HQ+'totals&by=day&'+q),hget(HQ+'totals&by=model&'+q),hget(HQ+'totals&by=project&'+q)]).then(([d,m,p])=>{if(HU.q!==q)return;HU.day=arr(d.totals);HU.model=arr(m.totals);HU.proj=arr(p.totals);HU.at=Date.now();HU.err=false;paint()},()=>{if(HU.q!==q)return;HU.err=true;paint()})}
const usageStat=()=>HU.err?`<div class="legendrow badc">Could not load history. Showing the last data.</div>`:HU.day?'':`<div class="legendrow">${checking()}</div>`;

/* Plans and quota: one row per provider the plugin lists, and Claude */
function planRows(){return [{id:'anthropic',name:'Claude',set:true},...PL.rows.map(r=>({id:r.name,name:laneName(r.name),set:r.key===true&&!!r.lane}))].map(x=>Object.assign(x,{q:provQuota(x.id),console:provConsole(x.id)}))}
const planCell=x=>x.q?quotaBody({console:x.console},x.q):x.set?(x.id==='anthropic'?'<span class="faint">not reported yet</span>':checking()):'<span class="faint">Not set up</span>';
const planMeter=x=>{const w=x.q&&arr(x.q.windows).filter(w=>num(w.used_pct)!=null);return w&&w.length?meter(Math.max(...w.map(v=>v.used_pct)),Math.max(...w.map(v=>v.used_pct))>=90?'max':'acc'):''};

/* Cost per day, stacked by provider */
function dayCost(){const days={},tot={};
 (HU.day||[]).forEach(d=>{const row=days[d.key]||(days[d.key]={});arr(d.models).forEach(m=>{const l=laneOfModel(m.model),c=n0(m.cost_usd);row[l]=(row[l]||0)+c;tot[l]=(tot[l]||0)+c})});
 const lanes=Object.keys(tot).sort((a,b)=>tot[b]-tot[a]);return {days:Object.keys(days).sort().map(k=>({key:k,by:days[k],sum:sum(Object.values(days[k]))})),lanes,tot}}
const dayLabel=k=>{const p=k.split('-');return `${MON[+p[1]-1]} ${+p[2]}`};
const laneColor=(lanes,l)=>`var(--c${lanes.indexOf(l)%6+1})`;
function costChart(){
 if(!HU.day)return `<div class="card pad">${HU.err?'<span class="badc">Could not load history.</span>':checking()}</div>`;
 const c=dayCost(),max=Math.max(1e-9,...c.days.map(d=>d.sum));
 if(!c.days.length||!sum(c.days.map(d=>d.sum)))return `<div class="card"><div class="allclear">${ic('info')} No priced requests in ${rangeText()}</div></div>`;
 const bars=c.days.map(d=>{const parts=c.lanes.filter(l=>d.by[l]).map(l=>`${laneName(l)} ${usd(d.by[l])}`).join(', ');
  return `<div class="daycol" role="img" aria-label="${esc(dayLabel(d.key))}: ${esc(usd(d.sum))}${parts?`. ${esc(parts)}`:''}" title="${esc(dayLabel(d.key))}: ${esc(usd(d.sum))}"><div class="daystack" data-css="height:${(d.sum/max*100).toFixed(1)}%">${c.lanes.filter(l=>d.by[l]).map(l=>`<i data-css="flex:${d.by[l]};background:${laneColor(c.lanes,l)}"></i>`).join('')}</div></div>`}).join('');
 const lg=c.lanes.map(l=>`<span><span class="sw" data-css="background:${laneColor(c.lanes,l)}"></span> ${esc(laneName(l))} <b>${usd(c.tot[l])}</b></span>`).join('');
 return `<div class="card pad"><div class="daychart">${bars}</div><div class="dayax"><span>${dayLabel(c.days[0].key)}</span><span>${dayLabel(c.days[c.days.length-1].key)}</span><span class="faint">tallest day ${usd(max)}</span></div><div class="legendrow bare">${lg}</div></div>`}
const costTotal=()=>HU.day?usd(sum(HU.day.map(d=>n0(d.cost_usd)))):'—';
const cacheSaved=()=>HU.model?usd(sum(HU.model.map(m=>n0(m.cache_saved_usd)))):'—';

/* Projects x models: total tokens, grouped by repository, for the picked range */
const MATRIX_MODELS=8,MATRIX_ROWS=20;
function matrix(){
 if(!HU.proj)return `<div class="allclear">${HU.err?'<span class="badc">Could not load history.</span>':checking()}</div>`;
 const cells={},mt={};
 HU.proj.forEach(p=>{cells[p.key]={};arr(p.models).forEach(m=>{const v=n0(m.tok_in)+n0(m.tok_out);cells[p.key][m.model]=v;mt[m.model]=(mt[m.model]||0)+v})});
 const ps=HU.proj.filter(p=>sum(Object.values(cells[p.key]))>0).sort((a,b)=>sum(Object.values(cells[b.key]))-sum(Object.values(cells[a.key])));
 if(!ps.length)return `<div class="allclear">${ic('info')} No requests matched to a project in ${rangeText()}</div>`;
 const ms=Object.keys(mt).sort((a,b)=>mt[b]-mt[a]),shown=ms.slice(0,MATRIX_MODELS),hid=ms.slice(MATRIX_MODELS),rows=ps.slice(0,MATRIX_ROWS);
 const cell=(p,m)=>m==='\0'?sum(hid.map(h=>cells[p.key][h]||0)):cells[p.key][m]||0;
 const cols=hid.length?[...shown,'\0']:shown;
 let max=1;rows.forEach(p=>cols.forEach(m=>{max=Math.max(max,cell(p,m))}));
 return `<div class="tbl"><table class="matrix fold"><thead><tr><th>Project</th>${cols.map(m=>m==='\0'?`<th class="num">${hid.length} more models</th>`:`<th class="num">${sw(m)} <span class="mono">${esc(m)}</span></th>`).join('')}<th class="num">Total</th></tr></thead><tbody>${rows.map(p=>`<tr><td data-label="Project"><b class="fw5">${p.key?esc(p.key):'<span class="faint">No project</span>'}</b></td>${cols.map(m=>{const v=cell(p,m);return `<td class="cellv" data-label="${m==='\0'?'More models':esc(m)}" data-css="background:${v?`color-mix(in srgb,var(--accent) ${Math.round(6+v/max*44)}%,transparent)`:'transparent'}">${v?fmt(v):'<span class="faint">·</span>'}</td>`}).join('')}<td class="cellv" data-label="Total"><b>${fmt(sum(Object.values(cells[p.key])))}</b></td></tr>`).join('')}</tbody></table></div>${ps.length>rows.length?`<div class="legendrow">Showing the ${rows.length} projects with the most tokens of ${ps.length}.</div>`:''}`}

PAGES.usage={title:'Usage',after:loadUsage,body:()=>head('Usage',`Where the tokens and the money went in ${rangeText()}.`)+reg('rstat',rangeStatus)+reg('ustat',usageStat)+
 `<section>${secH('Plans and quota','<span class="note">checked every 5 minutes while this page is open</span>')}<div class="card t">${reg('plans',()=>table('plans',[
  {k:'name',label:'Provider',h:x=>`<div class="ct">${esc(x.name)}</div>`},
  {k:'plan',label:'Plan or balance',h:x=>`${planCell(x)}${planMeter(x)}`}
 ],planRows(),{stack:1,empty:checking()}))}</div></section>`+
 `<section>${reg('costday',()=>secH('Cost per day',`<span class="note">API price, ${costTotal()} in ${rangeText()}. Plans charge a flat rate.</span>`)+costChart())}</section>`+
 `<section>${secH('Cache')}<div class="card kpis k5">${reg('cachek',()=>{const rm=RM(),cr=sum(rm.map(m=>m.cr)),cw=sum(rm.map(m=>m.cw)),fresh=sum(rm.map(m=>m.fresh)),out=sum(rm.map(m=>m.out));return kpiGrid([
  {k:'Read from cache',v:fmt(cr),d:cr+cw+fresh?`${Math.round(cr/(cr+cw+fresh)*100)}% of input tokens`:'—'},{k:'Written to cache',v:fmt(cw),d:'new prefixes cached'},{k:'Fresh input',v:fmt(fresh),d:'not cached'},{k:'Output',v:fmt(out),d:`${rate(out/(rangeMin()*60))} per second`},
  {k:'Saved by the cache',v:cacheSaved(),d:'vs the full input price'}])})}</div></section>`+
 `<section>${secH('Tokens by model')}<div class="card t">${reg('tokm',()=>table('tokm',rangeCols([
  {k:'m',label:'Model',v:x=>x.id,h:x=>modelTag(x.id)},
  {k:'in',label:'Fresh input',num:1,v:x=>x.fresh,h:x=>fmt(x.fresh)},
  {k:'cw',label:'Cache write',num:1,v:x=>x.cw,h:x=>fmt(x.cw),sm:1},
  {k:'cr',label:'Cache read',num:1,v:x=>x.cr,h:x=>fmt(x.cr)},
  {k:'out',label:'Output',num:1,v:x=>x.out,h:x=>fmt(x.out)},
  {k:'tot',label:'Total',num:1,v:x=>x.tin+x.out,h:x=>`<b>${fmt(x.tin+x.out)}</b>`},
  {k:'hit',label:'Cache hit',num:1,v:x=>x.hit,h:x=>`<div class="sharebar"><span class="mono">${Math.round(x.hit*100)}%</span>${meter(x.hit*100,'acc')}</div>`,sm:1}
 ]),rangeRows(),{sort:{k:'tot',d:-1},empty:rangeEmpty()}))}</div>${reg('prices',priceNote)}</section>`+
 `<section>${secH('Projects × models',`<span class="note">total tokens in ${rangeText()}, grouped by repository, darker is more</span>`)}<div class="card t">${reg('matrix',matrix)}</div></section>`};
