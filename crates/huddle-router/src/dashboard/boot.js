let saved='system';try{saved=localStorage.getItem('huddle-admin-theme')||'system'}catch(e){}
setTheme(saved);update({});
setInterval(()=>{if(document.visibilityState==='visible')update(SNAP)},2000);
renderRangeBar();renderRefBar();renderRefNow();renderTzBar();loadMeta();
loadHistory();getState();RF.start();
