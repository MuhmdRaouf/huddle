const STSHORT={'Working':'On','Not set up':'Off','Needs a fix':'Fix'};
const PNAME={zai:'Z.ai GLM',kimi:'Kimi',deepseek:'DeepSeek',minimax:'MiniMax',qwen:'Qwen'};
/* A provider is Working when set up with its lane on; Not set up is neutral because every catalog provider is optional; Needs a fix is a real fault. */
function provStatus(x){
 if(x.built)return ['ok','Built in'];
 const bad=x.last&&x.last.status!==200&&x.last.status!==429;
 if(x.setup)return x.laneOn&&!bad?['ok','Working']:['bad','Needs a fix'];
 return x.key&&!x.laneOn?['bad','Needs a fix']:['off','Not set up'];
}
function provRows(){
 const mk=(id,r)=>{const La=LANES.find(l=>l.id===id)||null,built=id==='anthropic';
  const x={id,name:(La&&La.name)||PNAME[id]||id,built,setup:built||(!!r&&r.set_up===true),key:!!r&&r.key===true,
   pattern:La?La.match:r&&r.lane||null,laneOn:built||!!(r&&r.lane)||(!!La&&La.state==='live'),
   models:La?La.models:[],nModels:r?n0(r.models):0,nAgents:r?n0(r.agents):0,
   n:La?La.n:0,err:La?La.err:0,p50:La?La.p50:null,last:La?laneLast(La):null,next:r&&r.next||null};
  x.status=provStatus(x);if(!x.next&&x.status[0]==='bad'&&!built)x.next=`/huddle:${id}:setup`;
  return x};
 const all=[...PL.rows.map(r=>mk(r.name,r)),mk('anthropic',null)];
 const live=all.filter(x=>x.status[0]!=='off'),rest=all.filter(x=>x.status[0]==='off');
 const ordered=[...live.filter(x=>!x.built),...live.filter(x=>x.built),...rest];
 rest.forEach((x,i)=>{x.rest=true;x.first=i===0});
 return ordered}
const provSetUp=()=>PL.rows.filter(r=>r.set_up===true).length;
const errPct=x=>x.n?(x.err?`<span class="badc">${(x.err/x.n*100).toFixed(1)}%</span>`:'0%'):NA;
PAGES.providers={title:'Providers',body:()=>head('Providers','Where your model requests go, and what each provider still needs.',reg('provn',()=>PL.rows.length?`<span class="note">${provSetUp()}/${PL.rows.length} set up</span>`:''))+
 `<div class="card kpis k3">${reg('provk',()=>{const r=provRows(),n=sum(r.map(x=>x.n)),e=sum(r.map(x=>x.err));return kpiGrid([
  {k:'Set up',v:PL.rows.length?provSetUp():'—',small:PL.rows.length?`of ${PL.rows.length}`:'',d:PL.rows.length?'with a key and a lane':'Checking…'},
  {k:'Requests',v:n.toLocaleString('en-US'),d:'across every provider'},
  {k:'Errors',v:e.toLocaleString('en-US'),d:n?`${(e/n*100).toFixed(1)}% of requests`:'no requests yet'}])})}</div>`+
 `<div class="card t">${reg('lanes',()=>table('lanes',[
  {k:'name',label:'Provider',h:x=>`<div class="ct">${esc(x.name)}</div>${x.pattern?`<div class="cs">${esc(x.pattern)}</div>`:''}`},
  {k:'status',label:'Status',h:x=>pill(x.status[0],x.status[1],STSHORT[x.status[1]])},
  {k:'n',label:'Requests',num:1,h:x=>x.n?`<b>${x.n.toLocaleString('en-US')}</b>`:NA,sm:1},
  {k:'p50',label:'<span title="Time to first token">1st token</span>',num:1,h:x=>x.n&&x.p50?`<span class="mono">${msf(x.p50)}</span>`:NA,sm:1},
  {k:'err',label:'Errors',num:1,h:errPct,sm:1},
  {k:'next',label:'Next step',h:x=>x.next?cmd(x.next):NA}
 ],provRows(),{row:x=>'provider:'+x.id,cls:x=>x.rest?x.first?'dim sep':'dim':'',empty:PL.state==='checking'?checking():'No providers found'}))}<div class="legendrow"><span><b>Working</b> set up, and its lane answers</span><span><b>Not set up</b> optional; run the command to add it</span><span><b>Needs a fix</b> a key without a lane, or a failing lane</span></div></div>`+
 `<div class="notebox">${ic('info')}<span>Keys stay in the operating system's key store. This page only learns whether a key exists, never its value.</span></div>`};
