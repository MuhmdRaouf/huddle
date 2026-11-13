const SESS_STATES=[['needs_you','Needs you'],['working','Working'],['background','Background'],['your_turn','Your turn'],['done','Done'],['idle','Idle']];
const skey=s=>SESS_STATES.some(x=>x[0]===s.state)?s.state:'idle';
function sessPill(s){const k=skey(s),w=since(s.waitingSince),t=w==='—'?'':' '+w;
 if(k==='needs_you')return `<span class="pill warn"><span class="dot warn pulse"></span>Needs you${esc(t)}</span>`;
 if(k==='working')return pill('acc','Working');
 if(k==='background')return pill('acc','Background'+(s.openTasks?' '+s.openTasks:''));
 if(k==='your_turn')return pill('warn','Your turn'+t);
 if(k==='done')return pill('ok','Done');
 return `<span class="pill off">${ic('minus')}${esc(['Idle',since(lastT(s))].filter(x=>x!=='—').join(' '))}</span>`}
function sessCache(s){const l=cacheLeft(s);return `<div>${cacheCell(s)}</div>${l!=null&&l<=0&&s.ctxTok?`<div class="cs">Next message re-reads ${fmt(s.ctxTok)} tokens</div>`:''}`}
/* the shared range columns, hidden on a phone */
const HC=histCols(s=>hOf(HT.ses,s.id)),hideOnPhone=(c,cls)=>Object.create(c,{sm:{value:1},cls:{value:cls||''}});
const SESS_TBL_COLS=[
 {k:'state',label:'State',v:s=>SESS_STATES.findIndex(x=>x[0]===skey(s))*1e13-(lastT(s)||0),h:sessPill},
 {k:'title',label:'Session',v:s=>s.name,h:s=>`<div class="ct">${esc(s.name)}</div><div class="cs">${esc([s.project,s.branch,s.sid].filter(Boolean).join(' · '))}</div>`,cls:'fit'},
 {k:'model',label:'Model',v:s=>mainA(s).model,h:s=>modelTag(mainA(s).model)},
 {k:'ctx',label:'Context',num:1,v:s=>s.ctx==null?-1:s.ctx,h:ctxCell},
 {k:'cache',label:'Prompt cache',v:s=>{const l=cacheLeft(s);return l==null?-1e12:l},h:sessCache,sm:1,cls:'fit'},
 hideOnPhone(HC[0]),
 {k:'errors',label:'Errors',num:1,v:s=>n0((hOf(HT.ses,s.id)||HZERO).err),h:s=>hCell(hOf(HT.ses,s.id),t=>t.err?`<span class="badc">${fmt(t.err)}</span>`:'<span class="faint">0</span>'),sm:1},
 hideOnPhone(HC[2],'tight'),
 {k:'last',label:'Last activity',num:1,v:s=>-(lastT(s)||0),h:s=>`<span class="muted">${ago(lastT(s))}</span>`,sm:1}
];
F.sessgrp='flat';
const sessTable=rows=>table('sess',SESS_TBL_COLS,rows,{row:s=>'session:'+s.id,sort:{k:'state',d:1},stack:1,empty:'No sessions match'});
function sessByRepo(rows){if(!rows.length)return sessTable(rows);const g={};rows.forEach(s=>{const k=s.repo||s.cwd||s.project;(g[k]=g[k]||[]).push(s)});
 return Object.entries(g).sort((a,b)=>Math.max(...b[1].map(lastT))-Math.max(...a[1].map(lastT))).map(([k,ss])=>`<div class="rgrp"><div class="rgh"><b>${esc(ss[0].project||String(k).split('/').pop())}</b><span class="mono faint">${esc(tilde(k))}</span><span class="n">${ss.length}</span></div>${sessTable(ss)}</div>`).join('')}
/* the list follows the global range: a session stays listed while it is live (needs you, working,
 background) or has a request inside the range; before the totals load, or when they fail, every
 session is listed */
const sessShown=()=>!HT.ses||HT.err?SESS:SESS.filter(s=>ACTIVE_STATES.includes(skey(s))||hOf(HT.ses,s.id).n>0);
PAGES.sessions={title:'Sessions',body:()=>{const c=k=>k==='all'?sessShown().length:sessShown().filter(s=>skey(s)===k).length;
 return head('Sessions',`Claude Code sessions, with the requests, tokens and API price of ${rangeText()}. Paths, branches, titles and subagents come from Claude Code\'s own session logs, read only while this page is open.`,reg('sess-f',()=>seg('sess',[['all','All',c('all')]].concat(SESS_STATES.map(([k,l])=>[k,l,c(k)])),F.sess)+seg('sessgrp',[['flat','List'],['repo','Group by repo']],F.sessgrp))+search('sess','Filter by title, repo or branch'))+
 `<div class="card t">${reg('sess',()=>{const rows=sessShown().filter(s=>(F.sess==='all'||skey(s)===F.sess)&&qmatch('sess',s.name+' '+s.project+' '+s.branch+' '+s.id+' '+s.repo));return F.sessgrp==='repo'?sessByRepo(rows):sessTable(rows)})}</div>`}};
