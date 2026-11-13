PAGES.projects={title:'Projects',body:()=>head('Projects','Grouped by repository: sessions, worktrees and delegated jobs together.',search('proj','Filter by project or path'))+
 `<div class="card t">${reg('proj',()=>table('proj',[
  {k:'name',label:'Project',v:p=>p.name,h:p=>`<div class="ct">${esc(p.name)}</div><div class="cs">${esc(tilde(p.path))}</div>`},
  {k:'br',label:'Branches',v:p=>p.branches.length,h:p=>`<span class="mono muted">${esc(p.branches.slice(0,2).join(', '))}${p.branches.length>2?` +${p.branches.length-2}`:''}</span>`,sm:1},
  {k:'sess',label:'Sessions',num:1,v:p=>p.live,h:p=>p.sessions.length?`${p.live?`<b>${p.live}</b> live`:'<span class="faint">0 live</span>'} <span class="faint">/ ${p.sessions.length}</span>`:NA},
  {k:'ag',label:'Agents running',num:1,v:p=>p.agentsRun,h:p=>p.agentsRun||'<span class="faint">0</span>',sm:1},
  {k:'wt',label:'Worktrees',num:1,v:p=>p.wts.length,h:p=>p.wts.length},
  {k:'jobs',label:'Jobs',num:1,v:p=>p.jobs.length,h:p=>p.jobs.length||'<span class="faint">0</span>',sm:1},
  {k:'req',label:'Requests',num:1,v:p=>p.reqs.length,h:p=>p.reqs.length||'<span class="faint">0</span>'},
  {k:'tok',label:'Tokens in / out',num:1,v:p=>p.tin,h:p=>p.reqs.length?`${fmt(p.tin)} <span class="faint">/</span> ${fmt(p.tout)}`:NA,sm:1},
  {k:'top',label:'Top model',v:p=>p.top||'',h:p=>modelTag(p.top),sm:1},
  {k:'last',label:'Last activity',num:1,v:p=>-(p.last||0),h:p=>`<span class="muted">${p.last?ago(p.last):'—'}</span>`},
  ...histCols(p=>hOf(HT.prj,p.name))
 ],projects().filter(p=>qmatch('proj',p.name+' '+tilde(p.path))),{row:p=>'project:'+p.name,sort:{k:'last',d:1},empty:'No projects yet'}))}</div>`};

const kindPill=k=>k==='checkout'?pill('off','Checkout'):k==='agent'?`<span class="pill acc">${ic('agents')}Agent</span>`:`<span class="pill warn">${ic('jobs')}Job</span>`;
const changesCell=w=>w.changes==null?NA:w.changes?`<span class="warnc">${w.changes} file${w.changes===1?'':'s'}</span>`:'<span class="faint">clean</span>';
