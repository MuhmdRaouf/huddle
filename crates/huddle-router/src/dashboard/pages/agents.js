const IDLE_AFTER=30*MIN;
const agentKind=a=>a.type==='main'?'Main':a.type==='background'?'Background':'Subagent';
const agentLast=a=>a.type==='main'||a.type==='background'?lastT(a.s):Math.max(a.start||0,...aReq(a).map(r=>r.t))||null;
/* a running row with no event for 30 minutes reads Idle */
const agentState=a=>{if(a.state!=='running')return a.state;const l=agentLast(a);return l!=null&&NOW-l>IDLE_AFTER?'idle':'running'};
const agentEnd=a=>a.end||(agentState(a)==='idle'?agentLast(a):NOW);
function agentStatePill(a){const k=agentState(a);if(k==='running')return pill('acc','Running');if(k==='waiting')return `<span class="pill warn"><span class="dot warn"></span>Waiting on you</span>`;if(k==='done')return pill('ok','Done');return pill('off','Idle')}
const AG_COLS=[
 {k:'agent',label:'Agent',v:a=>a.desc,h:a=>`<div class="ct">${a.type==='main'?'<b>Main</b>':`<span class="mono">${agentKind(a)==='Background'?'Background':'Subagent · '+esc(a.type)}</span>`}</div><div class="cs sans">${esc(a.desc)}</div>`},
 {k:'session',label:'Session',v:a=>a.s.project,h:a=>`<div>${esc(a.s.project)}</div><div class="cs">${esc(a.s.sid)}</div>`,sm:1},
 {k:'model',label:'Model',v:a=>a.model,h:a=>modelTag(a.model)},
 {k:'state',label:'State',v:a=>({running:0,waiting:1,done:2,idle:3})[agentState(a)],h:agentStatePill},
 {k:'dur',label:'Ran for',num:1,v:a=>a.start==null?0:agentEnd(a)-a.start,h:a=>`<span class="mono">${a.start==null?'—':dur(agentEnd(a)-a.start)}</span>`},
 {k:'req',label:'Requests',num:1,v:a=>aReq(a).length,h:a=>aReq(a).length,sm:1},
 {k:'tok',label:'Tokens in / out',num:1,v:a=>tokInOf(aReq(a))+tokOutOf(aReq(a)),h:a=>`${fmt(tokInOf(aReq(a)))} <span class="faint">/</span> ${fmt(tokOutOf(aReq(a)))}`,sm:1},
 {k:'wt',label:'Worktree',v:a=>a.wt||'',h:a=>wtName(a.wt),sm:1}
];
PAGES.agents={title:'Agents',body:()=>{const c=k=>k==='all'?AGENTS.length:AGENTS.filter(a=>agentState(a)===k).length;
 return head('Agents','Every main agent, subagent and open background task, with its session, model, state and requests. A row with no activity for 30 minutes reads Idle. Types and descriptions come from the session logs.',reg('agents-f',()=>seg('agents',[['all','All',c('all')],['running','Running',c('running')],['waiting','Waiting',c('waiting')],['done','Done',c('done')],['idle','Idle',c('idle')]],F.agents))+search('agents','Filter by agent, task or repo'))+
 `<div class="card t">${reg('agents',()=>table('agents',AG_COLS,AGENTS.filter(a=>(F.agents==='all'||agentState(a)===F.agents)&&qmatch('agents',agentKind(a)+' '+a.type+' '+a.desc+' '+a.s.project+' '+a.model)),{row:a=>'session:'+a.s.id,sort:{k:'state',d:1},stack:1,empty:'No agents match'}))}</div>`}};
