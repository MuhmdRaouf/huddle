const kindPill=k=>k==='checkout'?pill('off','Checkout'):k==='agent'?`<span class="pill acc">${ic('agents')}Agent</span>`:`<span class="pill warn">${ic('jobs')}Job</span>`;
const changesCell=w=>w.changes==null?NA:w.changes?`<span class="warnc">${w.changes} file${w.changes===1?'':'s'}</span>`:'<span class="faint">clean</span>';
const wtRemove=w=>w.kind==='agent'?`<div class="stw">${cmd(`git -C ${shq(w.repo||w.path)} worktree remove ${shq(w.path)}`)}</div>`:NA;
const shq=t=>/^[\w@%+=:,./-]+$/.test(t)?t:"'"+t.replace(/'/g,"'\\''")+"'";
const WT_FILTERS={all:()=>true,stale:w=>w.stale,dirty:w=>w.changes>0,checkout:w=>w.kind==='checkout',agent:w=>w.kind==='agent',job:w=>w.kind==='job'};
const WT_COLS=[
 {k:'branch',label:'Branch',v:w=>w.branch,h:w=>`<div class="ct mono fs13">${esc(w.branch||'(detached)')}</div><div class="cs">${esc(tilde(w.path))}</div>`},
 {k:'kind',label:'Kind',v:w=>w.kind,h:w=>kindPill(w.kind)},
 {k:'by',label:'Used by',v:w=>w.by||'',h:w=>w.stale?pill('warn','No session · '+dur(NOW-w.commit).split(' ')[0]):w.by?`<span class="mono muted">${esc(w.by)}</span>`:NA},
 {k:'head',label:'HEAD',v:w=>w.head,h:w=>cid(w.head),sm:1},
 {k:'ch',label:'Uncommitted',num:1,v:w=>w.changes==null?-1:w.changes,h:changesCell},
 {k:'commit',label:'Last commit',num:1,v:w=>-(w.commit||0),h:w=>`<span class="muted">${ago(w.commit)}</span>`,sm:1},
 {k:'rm',label:'Remove with',h:wtRemove,sm:1}
];
const wtTable=rows=>table('wt',WT_COLS,rows,{sort:{k:'branch',d:1},stack:1,empty:'No worktrees match'});
function wtByRepo(rows){if(!rows.length)return wtTable(rows);const g={};rows.forEach(w=>{(g[w.repo||w.project]=g[w.repo||w.project]||[]).push(w)});
 return Object.entries(g).sort((a,b)=>a[1][0].project.localeCompare(b[1][0].project)).map(([k,ws])=>`<div class="rgrp"><div class="rgh"><b>${esc(ws[0].project)}</b><span class="mono faint">${esc(tilde(k))}</span><span class="n">${ws.length}</span></div>${wtTable(ws)}</div>`).join('')}
PAGES.worktrees={title:'Worktrees',body:()=>{const c=k=>WT.filter(WT_FILTERS[k]).length;
 return head('Worktrees','Every checkout and worktree of the repositories in use, read from git worktree list. Read only: huddle never changes them here. Stale means an agent worktree with no session for 5 days.',reg('wt-f',()=>seg('wt',[['all','All',c('all')],['stale','Stale',c('stale')],['dirty','Uncommitted',c('dirty')],['checkout','Checkouts',c('checkout')],['agent','Agent',c('agent')],['job','Job',c('job')]],F.wt)))+
 reg('wt-n',()=>`<div class="legendrow bare mb8">${WT.length} worktrees in ${new Set(WT.map(w=>w.repo||w.project)).size} repos · ${c('stale')} stale · ${c('dirty')} with uncommitted changes</div>`)+
 `<div class="card t">${reg('wt',()=>wtByRepo(WT.filter(WT_FILTERS[F.wt]||WT_FILTERS.all)))}</div>`}};
