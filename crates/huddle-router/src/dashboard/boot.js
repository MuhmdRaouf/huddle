let saved='system';try{saved=localStorage.getItem('huddle-admin-theme')||'system'}catch(e){}
setTheme(saved);update({});
renderRangeBar();renderRefBar();renderRefNow();renderTzBar();loadMeta();
loadHistory();getState();RF.start();
