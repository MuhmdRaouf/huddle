PAGES.jobs={title:'Jobs',body:()=>head('Delegated jobs','Tasks handed to another coding CLI with /huddle:run. Each runs in its own worktree; you accept or return the result.',cmd('/huddle:status'))+
 `<div class="card t">${reg('jobs',()=>table('jobs',[
  {k:'id',label:'Job',v:j=>j.id,h:j=>cid(j.id)},
  {k:'h',label:'Harness',v:j=>j.h,h:j=>`<span class="mono">${esc(j.h)}</span>`},
  {k:'task',label:'Task',v:j=>j.task,h:j=>`<div class="ct fw4">${esc(j.task)}</div>${j.why?`<div class="err-small">${esc(j.why)}</div>`:''}`},
  {k:'repo',label:'Repo',v:j=>j.repo,h:j=>esc(j.repo),sm:1},
  {k:'state',label:'State',v:j=>({running:0,awaiting_review:1,queued:2,dead:3,failed:3,accepted:4})[j.state],h:jobPill},
  {k:'start',label:'Started',num:1,v:j=>-(j.started||0),h:j=>`<span class="muted">${ago(j.started)}</span>`,sm:1},
  {k:'dur',label:'Ran for',num:1,v:j=>j.started==null?0:(j.end||NOW)-j.started,h:j=>`<span class="mono">${j.started==null?'—':dur((j.end||NOW)-j.started)}</span>`,sm:1},
  {k:'diff',label:'Changes',num:1,v:j=>n0(j.add)+n0(j.del),h:j=>j.files?`<span class="mono"><span class="okc">+${n0(j.add)}</span> <span class="badc">−${n0(j.del)}</span> <span class="faint">· ${j.files} files</span></span>`:NA,sm:1},
  {k:'next',label:'Next step',h:j=>j.state==='awaiting_review'?cmd(`/huddle:result ${j.id} --diff`):j.state==='running'?cmd(`/huddle:status ${j.id}`):j.state==='dead'||j.state==='failed'?cmd(`/huddle:cancel ${j.id}`):NA}
 ],JOBS,{sort:{k:'state',d:1},empty:'No delegated jobs yet'}))}</div>`};
