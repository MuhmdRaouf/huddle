function modelRows(){const t=Math.max(1,sum(MODELS.map(m=>m.n)));return MODELS.map(m=>Object.assign({share:m.n/t,hit:m.tin?m.cr/m.tin:0,trend:BK.map(x=>(x.byM[m.id]||EMPTY).n)},m))}
PAGES.models={title:'Models',body:()=>head('Models',`Every model seen in ${rangeText()}, with latency, cache use, errors and estimated cost.`)+reg('rstat',rangeStatus)+
 `<div class="card t">${reg('models',()=>table('models',rangeCols([
  {k:'model',label:'Model',v:x=>x.id,h:x=>modelTag(x.id)},
  {k:'lane',label:'Lane',v:x=>x.lane,h:x=>`<span class="muted">${esc(x.lane)}</span>`,sm:1},
  {k:'n',label:'Requests',num:1,v:x=>x.n,h:x=>`<b>${x.n}</b>`},
  {k:'share',label:'Share',num:1,v:x=>x.share,h:x=>`<div class="sharebar"><span class="mono">${Math.round(x.share*100)}%</span>${meter(x.share*100,'acc')}</div>`,sm:1},
  {k:'trend',label:'Last 15 min',h:x=>spark(x.trend,col(x.id),'tsp'),sm:1},
  {k:'err',label:'Errors',num:1,v:x=>x.err,h:x=>x.err?`<span class="badc">${x.err}</span>`:'<span class="faint">0</span>'},
  {k:'p50',label:'Time to first token p50',num:1,v:x=>x.p50||0,h:x=>`<span class="mono">${msf(x.p50)}</span>`},
  {k:'p95',label:'Time to first token p95',num:1,v:x=>x.p95||0,h:x=>`<span class="mono">${msf(x.p95)}</span>`,sm:1},
  {k:'speed',label:'Output speed',num:1,v:x=>x.speed||0,h:x=>`<span class="mono">${x.speed?Math.round(x.speed)+' tok/s':'—'}</span>`,sm:1},
  {k:'tin',label:'Tokens in',num:1,v:x=>x.tin,h:x=>`<span class="mono">${fmt(x.tin)}</span>`},
  {k:'hit',label:'Cache hit',num:1,v:x=>x.hit,h:x=>`<span class="mono">${Math.round(x.hit*100)}%</span>`,sm:1},
  {k:'out',label:'Tokens out',num:1,v:x=>x.out,h:x=>`<span class="mono">${fmt(x.out)}</span>`},
  {k:'sess',label:'Sessions',num:1,v:x=>x.sess||0,h:x=>x.sess==null?'—':x.sess,sm:1},
  {k:'last',label:'Last used',num:1,v:x=>-(x.last||0),h:x=>`<span class="muted">${x.last?ago(x.last):'—'}</span>`,sm:1}
 ]),rangeRows(),{sort:{k:'n',d:-1},empty:rangeEmpty()}))}</div>`};

const laneLast=l=>{if(l.last)return l.last;const rs=stats().byLane[l.id]||[];const r=rs[rs.length-1];return r?{status:r.status,t:r.t}:null};
