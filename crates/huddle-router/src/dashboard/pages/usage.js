PAGES.usage={title:'Usage',body:()=>head('Usage',`Where the tokens went in the last ${rangeLabel()}.`,rangeSeg())+reg('rstat',rangeStatus)+
 `<div class="${'g2e'}">${reg('planbox',()=>PLAN?`<section>${secH('Claude plan','<span class="note">from Anthropic\'s usage headers</span>')}<div class="card pad plan">${
  PLAN.h5!=null?`<div class="planrow"><div><div class="k">5-hour window</div><div class="v">${Math.round(PLAN.h5)}<small>%</small></div></div><div class="r">${PLAN.h5r!=null?'resets in '+dur(PLAN.h5r-NOW)+'<br><span class="faint">at '+clock(PLAN.h5r).slice(0,5)+'</span>':'—'}</div>${meter(PLAN.h5,'acc')}</div>`:''}${
  PLAN.wk!=null?`<div class="planrow"><div><div class="k">This week</div><div class="v">${Math.round(PLAN.wk)}<small>%</small></div></div><div class="r">${PLAN.wkr!=null?'resets in '+dur(PLAN.wkr-NOW):'—'}</div>${meter(PLAN.wk,'acc')}</div>`:''}</div></section>`:'')}
 <section>${secH('Cache')}<div class="card kpis two">${reg('cachek',()=>{const rm=RM(),cr=sum(rm.map(m=>m.cr)),cw=sum(rm.map(m=>m.cw)),fresh=sum(rm.map(m=>m.fresh)),out=sum(rm.map(m=>m.out));return kpiGrid([
  {k:'Read from cache',v:fmt(cr),d:cr+cw+fresh?`${Math.round(cr/(cr+cw+fresh)*100)}% of input tokens`:'—'},{k:'Written to cache',v:fmt(cw),d:'new prefixes cached'},{k:'Fresh input',v:fmt(fresh),d:'not cached'},{k:'Output',v:fmt(out),d:`${rate(out/(rangeMin()*60))} per second`}])})}</div></section></div>`+
 `<section>${secH('Tokens by model')}<div class="card t">${reg('tokm',()=>table('tokm',rangeCols([
  {k:'m',label:'Model',v:x=>x.id,h:x=>modelTag(x.id)},
  {k:'in',label:'Fresh input',num:1,v:x=>x.fresh,h:x=>fmt(x.fresh)},
  {k:'cw',label:'Cache write',num:1,v:x=>x.cw,h:x=>fmt(x.cw),sm:1},
  {k:'cr',label:'Cache read',num:1,v:x=>x.cr,h:x=>fmt(x.cr)},
  {k:'out',label:'Output',num:1,v:x=>x.out,h:x=>fmt(x.out)},
  {k:'tot',label:'Total',num:1,v:x=>x.tin+x.out,h:x=>`<b>${fmt(x.tin+x.out)}</b>`},
  {k:'hit',label:'Cache hit',num:1,v:x=>x.hit,h:x=>`<div class="sharebar"><span class="mono">${Math.round(x.hit*100)}%</span>${meter(x.hit*100,'acc')}</div>`,sm:1}
 ]),rangeRows(),{sort:{k:'tot',d:-1},empty:rangeEmpty()}))}</div>${reg('prices',priceNote)}</section>`+
 `<section>${secH('Projects × models','<span class="note">last 15 min, total tokens, darker is more</span>')}<div class="card t">${reg('matrix',matrix)}</div></section>`};
