if(typeof EventSource==='function'){
 const es=new EventSource('/huddle-router/live');
 es.addEventListener('snapshot',ev=>{let d;try{d=JSON.parse(ev.data)}catch(e){return}stopPoll();onSnapshot(d)});
 es.onerror=()=>{LOST=true;update(SNAP);startPoll()};
}else startPoll();

let saved='system';try{saved=localStorage.getItem('huddle-admin-theme')||'system'}catch(e){}
setTheme(saved);update({});
setInterval(()=>{if(document.visibilityState==='visible')update(SNAP)},2000);
setInterval(()=>{if(document.visibilityState==='visible')loadHistory()},60000);
setInterval(()=>{if(document.visibilityState==='visible')loadTotals()},60000);
renderRangeBar();loadMeta();
loadHistory();
