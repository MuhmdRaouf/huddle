function errGroups(){const d=stats().done,o={};d.filter(r=>r.status!==200).forEach(r=>{(o[r.status]=o[r.status]||[]).push(r)});const t0=NOW-15*MIN;
 return Object.entries(o).map(([code,rs])=>{const c=+code;const trend=Array(30).fill(0);rs.forEach(r=>{trend[Math.max(0,Math.min(29,Math.floor((r.t-t0)/30e3)))]++});
  const reason=(rs.find(r=>r.reason)||{}).reason;
  return {code:c,def:errDef(c,reason),rs,trend,lanes:[...new Set(rs.map(r=>r.lane))],models:[...new Set(rs.map(r=>r.m))],sessions:new Set(rs.map(r=>r.sess).filter(Boolean)).size,last:rs[rs.length-1].t,share:rs.length/d.length}})}
PAGES.errors={title:'Errors',body:()=>HS.mode==='history'?hview('Errors','Failed requests kept in history, newest first. Choose the status to include requests you cancelled.'):head('Errors','Failed and cancelled requests in the last 15 minutes, grouped by cause. A 499 means you stopped the stream, so it never counts as an error.',hmodes())+
 `<section>${secH('By cause')}<div class="card t">${reg('errg',()=>table('errg',[
  {k:'err',label:'Cause',v:g=>g.def[0],h:g=>`<div class="ct">${esc(g.def[0])}</div><div class="cs">${esc(g.def[1])}</div>`},
  {k:'code',label:'Status',v:g=>g.code,h:g=>g.code===499?pill('off','499'):g.code===429?pill('warn','429'):pill('bad',String(g.code))},
  {k:'n',label:'Count',num:1,v:g=>g.rs.length,h:g=>`<b>${g.rs.length}</b>`},
  {k:'trend',label:'Last 15 min',h:g=>spark(g.trend,g.code===499?'var(--faint)':g.code===429?'var(--warn)':'var(--bad)','tsp'),sm:1},
  {k:'share',label:'Of requests',num:1,v:g=>g.share,h:g=>`${(g.share*100).toFixed(1)}%`,sm:1},
  {k:'lanes',label:'Lanes',h:g=>`<span class="muted">${esc(g.lanes.join(', '))}</span>`,sm:1},
  {k:'models',label:'Models',h:g=>g.models.map(modelTag).join(' &nbsp;')},
  {k:'sess',label:'Sessions',num:1,v:g=>g.sessions,h:g=>g.sessions,sm:1},
  {k:'last',label:'Last seen',num:1,v:g=>-g.last,h:g=>`<span class="muted">${ago(g.last)}</span>`},
  {k:'counts',label:'Counts as error',c:1,h:g=>g.def[3]?'<span class="badc">Yes</span>':'<span class="faint">No</span>',sm:1}
 ],errGroups(),{sort:{k:'n',d:-1},empty:'No errors in the last 15 minutes'}))}</div></section>`+
 `<section>${secH('What each cause means')}<div class="card list">${Object.entries(ERRDEF).map(([c,d])=>`<div class="li"><span class="mono w36">${c}</span><div class="grow"><div><b class="fw5">${d[0]}</b> <span class="mono faint">${d[1]}</span></div><div class="sm wrap">${d[2]}</div></div></div>`).join('')}</div></section>`+
 `<section>${secH('Recent')}<div class="card t">${reg('errr',()=>table('errr',REQ_COLS.filter(c=>['time','status','model','who','dur','in'].includes(c.k)),stats().done.filter(r=>r.status!==200).reverse(),{row:r=>'request:'+r.id,limit:40,empty:'No errors in the last 15 minutes'}))}</div></section>`};
