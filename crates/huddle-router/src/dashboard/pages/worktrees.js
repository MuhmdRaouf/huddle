PAGES.worktrees={title:'Worktrees',body:()=>{const c=k=>k==='all'?WT.length:WT.filter(w=>w.kind===k).length;
 return head('Worktrees','Every checkout and worktree of the projects in use, read from git worktree list. Read only: huddle never changes them here.',reg('wt-f',()=>seg('wt',[['all','All',c('all')],['checkout','Checkouts',c('checkout')],['agent','Agent',c('agent')],['job','Job',c('job')]],F.wt)))+
 `<div class="card t">${reg('wt',()=>table('wt',[
  {k:'branch',label:'Branch',v:w=>w.branch,h:w=>`<div class="ct mono fs13">${esc(w.branch||'(detached)')}</div><div class="cs">${esc(tilde(w.path))}</div>`},
  {k:'project',label:'Project',v:w=>w.project,h:w=>esc(w.project)},
  {k:'kind',label:'Kind',v:w=>w.kind,h:w=>kindPill(w.kind)},
  {k:'by',label:'Used by',v:w=>w.by||'',h:w=>w.stale?pill('warn','No session · '+dur(NOW-w.commit).split(' ')[0]):w.by?`<span class="mono muted">${esc(w.by)}</span>`:NA},
  {k:'head',label:'HEAD',v:w=>w.head,h:w=>cid(w.head),sm:1},
  {k:'ch',label:'Uncommitted',num:1,v:w=>w.changes==null?-1:w.changes,h:changesCell},
  {k:'commit',label:'Last commit',num:1,v:w=>-(w.commit||0),h:w=>`<span class="muted">${ago(w.commit)}</span>`,sm:1}
 ],WT.filter(w=>F.wt==='all'||w.kind===F.wt),{sort:{k:'project',d:1},empty:'No worktrees yet'}))}</div>`}};
