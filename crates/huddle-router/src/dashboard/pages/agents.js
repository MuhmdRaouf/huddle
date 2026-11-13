const AG_COLS=[
 {k:'agent',label:'Agent',v:a=>a.desc,h:a=>`<div class="ct">${a.type==='main'?'<b>main</b>':`<span class="mono">${esc(a.type)}</span>`}</div><div class="cs sans">${esc(a.desc)}</div>`},
 {k:'session',label:'Session',v:a=>a.s.project,h:a=>`<div>${esc(a.s.project)}</div><div class="cs">${esc(a.s.sid)}</div>`,sm:1},
 {k:'model',label:'Model',v:a=>a.model,h:a=>modelTag(a.model)},
 {k:'state',label:'State',v:a=>({running:0,waiting:1,done:2,idle:3})[a.state],h:agentPill},
 {k:'start',label:'Started',num:1,v:a=>-(a.start||0),h:a=>`<span class="muted">${ago(a.start)}</span>`,sm:1},
 {k:'dur',label:'Ran for',num:1,v:a=>a.start==null?0:(a.end||NOW)-a.start,h:a=>`<span class="mono">${a.start==null?'—':dur((a.end||NOW)-a.start)}</span>`},
 {k:'req',label:'Requests',num:1,v:a=>aReq(a).length,h:a=>aReq(a).length,sm:1},
 {k:'out',label:'Tokens out',num:1,v:a=>tokOutOf(aReq(a)),h:a=>fmt(tokOutOf(aReq(a))),sm:1},
 {k:'wt',label:'Worktree',v:a=>a.wt||'',h:a=>wtName(a.wt),sm:1}
];
PAGES.agents={title:'Agents',body:()=>{const c=k=>k==='all'?AGENTS.length:AGENTS.filter(a=>a.state===k).length;
 return head('Agents','Every main session and subagent, with its model, state and worktree. Subagent types and descriptions come from the session logs.',reg('agents-f',()=>seg('agents',[['all','All',c('all')],['running','Running',c('running')],['waiting','Waiting',c('waiting')],['done','Done',c('done')],['idle','Idle',c('idle')]],F.agents))+search('agents','Filter by agent, task or project'))+
 `<div class="card t">${reg('agents',()=>table('agents',AG_COLS,AGENTS.filter(a=>(F.agents==='all'||a.state===F.agents)&&qmatch('agents',a.type+' '+a.desc+' '+a.s.project+' '+a.model)),{row:a=>'session:'+a.s.id,sort:{k:'state',d:1},empty:'No agents match'}))}</div>`}};
