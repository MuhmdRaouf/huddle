function modelRows(){const t=Math.max(1,sum(MODELS.map(m=>m.n)));return MODELS.map(m=>Object.assign({share:m.n/t,hit:m.tin?m.cr/m.tin:0,trend:BK.map(x=>(x.byM[m.id]||EMPTY).n)},m))}
/* price and cost per model for the picked range: history totals answer for every range, the live one included */
const HM={q:null,at:0,tot:null,err:false};
function loadModels(){const q=rangeQuery();if(HM.q===q&&HM.tot&&Date.now()-HM.at<6e4)return;
 if(HM.q!==q){HM.tot=null;HM.err=false}HM.q=q;
 hget(HQ+'totals&by=model&'+q).then(d=>{if(HM.q!==q)return;HM.tot=hmap(d);HM.at=Date.now();HM.err=false;paint()},()=>{if(HM.q!==q)return;HM.err=true;paint()})}
function modelPriced(){const t=HM.tot||{};return rangeRows().map(x=>{const h=t[x.id];return Object.assign({},x,{lane:x.lane||laneOfModel(x.id),cost:h?num(h.cost_usd):null,priced:h?h.priced!==false:true,known:!!h})})}
const perMillion=x=>x.n&&x.tin+x.out?x.cost/(x.tin+x.out)*1e6:null;
const modelPrice=x=>!x.known?(HM.err||HM.tot?NA:checking()):!x.priced?'<span class="faint">no price</span>':`<span class="mono">${usd(x.cost)}</span>`;
const modelPer1M=x=>!x.known?(HM.err||HM.tot?NA:checking()):!x.priced?'<span class="faint">no price</span>':perMillion(x)==null?NA:`<span class="mono">${usd(perMillion(x))}</span>`;
const modelStat=()=>HM.err?`<div class="legendrow badc">Could not load prices. Showing the last data.</div>`:'';
const modelCols=()=>[
 {k:'model',label:'Model',v:x=>x.id,h:x=>modelTag(x.id)},
 {k:'lane',label:'Provider',v:x=>laneName(x.lane),h:x=>`<span class="muted">${esc(laneName(x.lane))}</span>`},
 {k:'n',label:'Requests',num:1,v:x=>x.n,h:x=>`<b>${x.n.toLocaleString('en-US')}</b>`},
 {k:'err',label:'Error rate',num:1,v:x=>x.n?x.err/x.n:0,h:x=>x.err?`<span class="badc">${(x.err/x.n*100).toFixed(1)}%</span><div class="cs">${x.err.toLocaleString('en-US')} failed</div>`:'<span class="faint">0</span>'},
 {k:'ttft',label:'Time to first token',num:1,v:x=>x.p50||0,h:x=>x.p50==null?NA:`<span class="mono">${msf(x.p50)}</span>${x.p95!=null?`<div class="cs">p95 ${msf(x.p95)}</div>`:''}`},
 ...(liveRange()?[{k:'speed',label:'Speed',num:1,v:x=>x.speed||0,h:x=>`<span class="mono">${x.speed?Math.round(x.speed)+' tok/s':'—'}</span>`}]:[]),
 {k:'tok',label:'Tokens',num:1,v:x=>x.tin+x.out,h:x=>`<span class="mono">${fmt(x.tin)} <span class="faint">/</span> ${fmt(x.out)}</span><div class="cs sans">${fmt(x.fresh)} fresh · ${fmt(x.cr)} cached</div>`},
 {k:'hit',label:'Cache hit',num:1,v:x=>x.hit,h:x=>`<span class="mono">${Math.round(x.hit*100)}%</span>`},
 {k:'cost',label:'API price',num:1,v:x=>x.cost||0,h:modelPrice},
 {k:'per1m',label:'Price per 1M tokens',num:1,v:x=>perMillion(x)||0,h:modelPer1M},
 ...(liveRange()?[{k:'sess',label:'Sessions',num:1,v:x=>x.sess||0,h:x=>x.sess==null?'—':x.sess},
  {k:'last',label:'Last used',num:1,v:x=>-(x.last||0),h:x=>`<span class="muted">${x.last?ago(x.last):'—'}</span>`}]:[])];
PAGES.models={title:'Models',after:loadModels,body:()=>head('Models',`Every model seen in ${rangeText()}, with latency, cache use, errors and API price.`)+reg('rstat',rangeStatus)+reg('mstat',modelStat)+
 `<div class="card t">${reg('models',()=>table('models',modelCols(),modelPriced(),{stack:1,sort:{k:'n',d:-1},empty:rangeEmpty()}))}<div class="legendrow"><span>API price is what the same tokens cost at list prices, not what a plan charges. A model with no list price says so.</span></div></div>${reg('prices',priceNote)}`};
