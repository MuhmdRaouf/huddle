/* needs you: only what a person has to act on */
const NEED_GROUPS=[['questions','Questions'],['providers','Providers'],['router','Router'],['jobs','Jobs to review'],['setup','Setup checks'],['context','Context']];
const quotaOf=id=>{const q=SNAP.providers&&typeof SNAP.providers==='object'&&SNAP.providers[id];return q&&q.quota&&typeof q.quota==='object'?q.quota:null};
const openBtn=s=>`<button class="btn sm" data-open="session:${esc(s.id)}">Open session</button>`;
const provLink=`<a class="btn sm" href="#providers">Providers</a>`;
function attnItems(){const it=[];
 const P=RT.path;if(P.state==='failing')it.push({g:'router',sev:0,k:'bad',tag:'Anthropic',t:'Anthropic is not answering',why:`${P.code||'error'} at ${P.phase||'?'} (${num(P.failed)||0} requests failed): ${P.hint||'no detail'}${P.peer?', peer '+P.peer:''}`,since:num(P.since_ms),act:cmd('huddle router log')});
 if(WIRE.state==='bad')it.push({g:'router',sev:0,k:'bad',tag:'Wiring',t:'Claude Code is not pointed at the router',why:'The proxy setting or the local CA is missing.',since:null,act:cmd('/huddle:setup')});
 SESS.filter(s=>s.state==='needs_you').forEach(s=>it.push({g:'questions',sev:1,k:'warn',tag:'Question',t:s.name,why:(s.questionTool==='ExitPlanMode'?'A plan is waiting for your approval':'A question is waiting for your answer')+[s.project,s.branch].filter(Boolean).map(x=>' · '+x).join(''),since:s.waitingSince,act:openBtn(s)}));
 Object.keys(SNAP.providers&&typeof SNAP.providers==='object'?SNAP.providers:{}).forEach(id=>{const q=quotaOf(id),full=q?arr(q.windows).filter(w=>num(w.used_pct)!=null&&w.used_pct>=100):[];
  if(!full.length)return;const rs=full.map(w=>num(w.resets_ms)).filter(x=>x!=null).sort((a,b)=>a-b);
  it.push({g:'providers',sev:0,k:'bad',tag:'Out of quota',t:`${provName(id)} is out of quota`,why:`The ${full.map(w=>w.name).join(' and ')} window is used up${rs.length?`, resets ${when(rs[0])}`:''}.`,since:null,act:provLink})});
 provRows().filter(x=>x.status[1]==='Needs a fix'||(x.status[1]==='Failing'&&x.id!=='anthropic')).forEach(x=>it.push({g:'providers',sev:0,k:'bad',tag:x.status[1],t:`${x.name} ${x.status[1]==='Failing'?'is failing':'needs a fix'}`,why:x.status[1]==='Failing'?`${x.err} of ${x.n} requests failed.`:x.key&&!x.laneOn?'The key is saved but the lane is off.':'Its last request failed or its lane is off.',since:x.last&&num(x.last.t)!=null?x.last.t:null,act:cmd(x.next)}));
 JOBS.filter(j=>j.state==='awaiting_review').forEach(j=>it.push({g:'jobs',sev:2,k:'ok',tag:'Review',t:`Job ${j.id} is ready`,why:[j.h,j.repo,j.task].filter(Boolean).join(' · '),since:j.end,act:cmd(`/huddle:result ${j.id} --diff`)}));
 PL.checks.filter(c=>c.level==='fail').forEach(c=>it.push({g:'setup',sev:1,k:'bad',tag:'Check',t:`${c.label||c.id}: ${c.value||''}`,why:c.detail||'',since:null,act:`<a class="btn sm" href="#router">Router</a>`}));
 SESS.filter(s=>(s.state==='working'||s.state==='background')&&s.ctx!=null&&s.ctx>=90).forEach(s=>it.push({g:'context',sev:1,k:'bad',tag:`Context ${Math.round(s.ctx)}%`,t:`${s.name} is close to compaction`,why:'Wrap up or start a fresh session.',since:null,act:openBtn(s)}));
 return it.sort((a,b)=>a.sev-b.sev||(a.since||0)-(b.since||0))}
const unsetProviders=()=>provRows().filter(x=>x.status[1]==='Not set up').length;
const attnRow=a=>`<div class="attn-row"><span class="tag ${a.k}">${esc(a.tag)}</span><div class="what"><div class="t">${esc(a.t)}</div><div class="m">${esc(a.why)}${a.since!=null?esc(' · for '+since(a.since)):''}</div></div>${a.act}</div>`;
const needsCount=()=>attnItems().length;
PAGES['needs-you']={title:'Needs you',body:()=>
 head('Needs you','Only what you can answer or fix.',reg('needs-n',()=>`<span class="note">${needsCount()} items</span>`))+
 reg('needs-all',()=>{const it=attnItems();if(!it.length)return `<div class="card"><div class="allclear">${ic('check')} Nothing needs you.</div></div>`;
  return NEED_GROUPS.map(([k,l])=>{const g=it.filter(a=>a.g===k);return g.length?`<section>${secH(l,`<span class="note">${g.length}</span>`)}<div class="card">${g.map(attnRow).join('')}</div></section>`:''}).join('')})};
