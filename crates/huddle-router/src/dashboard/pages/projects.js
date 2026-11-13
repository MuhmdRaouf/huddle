
const histTop=t=>{const m=arr(t&&t.models).slice().sort((a,b)=>n0(b.n)-n0(a.n))[0];return m&&m.model?m.model:null};
const wtCell=p=>`${p.wts.length}${p.stale?` <span class="warnc">(${p.stale} stale)</span>`:''}`;
PAGES.projects={title:'Projects',body:()=>head('Projects',`One row per git repository: its sessions, worktrees and delegated jobs together. Requests, tokens and API price cover ${rangeText()}.`,search('proj','Filter by project or path'))+
 `<div class="card t">${reg('proj',()=>table('proj',[
  {k:'name',label:'Project',v:p=>p.name,h:p=>`<div class="ct">${esc(p.name)}</div><div class="cs">${esc(tilde(p.path))}</div>`},
  {k:'sess',label:'Active sessions',num:1,v:p=>p.live,h:p=>p.sessions.length?`${p.live?`<b>${p.live}</b> active`:'<span class="faint">0 active</span>'} <span class="faint">/ ${p.sessions.length}</span>`:NA},
  {k:'wt',label:'Worktrees',num:1,v:p=>p.wts.length,h:wtCell},
  {k:'jobs',label:'Jobs',num:1,v:p=>p.jobs.length,h:p=>p.jobs.length||'<span class="faint">0</span>',sm:1},
  ...histCols(p=>hOf(HT.prj,p.name)),
  {k:'top',label:'Top model',v:p=>histTop(hOf(HT.prj,p.name))||'',h:p=>hCell(hOf(HT.prj,p.name),t=>t.n?modelTag(histTop(t)):NA),sm:1},
  {k:'last',label:'Last activity',num:1,v:p=>-(p.last||0),h:p=>`<span class="muted">${p.last?ago(p.last):'—'}</span>`}
 ],projects().filter(p=>qmatch('proj',p.name+' '+tilde(p.path))),{row:p=>'project:'+p.name,sort:{k:'last',d:1},stack:1,empty:'No projects yet'}))}</div>`};
