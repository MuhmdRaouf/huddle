const OUTCOME={running:['acc','Running'],queued:['off','Queued'],dead:['bad','Dead'],failed:['bad','Failed'],cancelled:['off','Cancelled'],awaiting_review:['ok','Finished'],accepted:['ok','Finished'],completed:['ok','Finished']};
const outcomePill=j=>{const o=OUTCOME[j.state]||['off',j.state||'—'];return pill(o[0],o[1])};
const reviewPill=j=>j.state==='awaiting_review'?pill('warn','To review'):j.state==='accepted'?pill('ok','Accepted'):j.state==='completed'?'<span class="faint">Not needed</span>':NA;
const stwCmd=c=>`<div class="stw">${cmd(c)}</div>`;
const jobRank=j=>({running:0,awaiting_review:1,queued:2,dead:3,failed:3,completed:4,accepted:4,cancelled:5})[j.state];
PAGES.jobs={title:'Jobs',body:()=>head('Delegated jobs',`Tasks handed to another coding CLI with /huddle:run, kept for ${rangeText()} after they finish. Each runs in its own worktree; you accept or return the result.`,cmd('/huddle:status'))+
 `<div class="card t">${reg('jobs',()=>table('jobs',[
  {k:'id',label:'Job',v:j=>j.id,h:j=>cid(j.id)},
  {k:'h',label:'Harness',v:j=>j.h,h:j=>`<span class="mono">${esc(j.h)}</span>`},
  {k:'task',label:'Task',v:j=>j.task,h:j=>`<div class="ct fw4">${esc(j.task)}</div><div class="cs">${esc(j.repo)}</div>${j.why?`<div class="err-small">${esc(j.why)}</div>`:''}`},
  {k:'state',label:'Outcome',v:jobRank,h:outcomePill},
  {k:'review',label:'Review',v:j=>j.state==='awaiting_review'?0:1,h:reviewPill,sm:1},
  {k:'dur',label:'Ran for',num:1,v:j=>j.started==null?0:(j.end||NOW)-j.started,h:j=>`<span class="mono">${j.started==null?'—':dur((j.end||NOW)-j.started)}</span><div class="cs">started ${ago(j.started)}</div>`},
  {k:'diff',label:'Changes',num:1,v:j=>n0(j.add)+n0(j.del),h:j=>j.files?`<span class="mono"><span class="okc">+${n0(j.add)}</span> <span class="badc">−${n0(j.del)}</span></span><div class="cs">${j.files} file${j.files===1?'':'s'}</div>`:NA,sm:1},
  {k:'next',label:'Next step',h:j=>j.state==='awaiting_review'?stwCmd(`/huddle:result ${j.id} --diff`):j.state==='running'?stwCmd(`/huddle:status ${j.id}`):j.state==='dead'||j.state==='failed'?stwCmd(`/huddle:cancel ${j.id}`):NA}
 ],jobsInRange(),{sort:{k:'state',d:1},stack:1,empty:'No delegated jobs in this range'}))}</div>`};
