PAGES.harnesses={title:'Harnesses',body:()=>{const c=k=>k==='all'?HARN.length:k==='ready'?HARN.filter(h=>h.st==='ready').length:HARN.filter(h=>h.st!=='ready').length;
 return head('Harnesses','The coding CLIs huddle can delegate to, checked by /huddle:setup. Only ready ones take jobs.',reg('harn-f',()=>seg('harn',[['all','All',c('all')],['ready','Ready',c('ready')],['att','Needs attention',c('att')]],F.harn))+cmd('/huddle:setup'))+
 `<div class="card t">${reg('harn',()=>table('harn',[
  {k:'name',label:'Harness',v:h=>h.name,h:h=>`<div class="ct">${esc(h.name)}</div><div class="cs">${esc(h.id)}</div>`},
  {k:'st',label:'Status',v:h=>h.st==='ready'?0:1,h:h=>h.st==='ready'?pill('ok','Ready'):h.st==='fail'?pill('bad','Fails to start'):pill('off','Not on PATH')},
  {k:'ver',label:'Version',v:h=>h.ver,h:h=>h.ver?(h.st==='ready'?`<span class="mono">${esc(h.ver)}</span>`:`<span class="err-small">${esc(h.ver)}</span>`):NA},
  {k:'bin',label:'Binary',v:h=>h.bin,h:h=>h.bin?`<span class="mono muted">${esc(tilde(h.bin))}</span>`:NA,sm:1},
  {k:'flags',label:'Flags',v:h=>h.flags,h:h=>h.flags==='checked'?pill('ok','Checked'):h.flags?pill('off','Unverified'):NA,sm:1},
  {k:'report',label:'Report',v:h=>h.report,h:h=>h.report?`<span class="muted">${esc(h.report)}</span>`:NA,sm:1},
  {k:'access',label:'Access',h:h=>h.access.length?`<span class="access">${h.access.map(a=>`<span>${esc(a)}</span>`).join('')}</span>`:NA,sm:1},
  {k:'jobs',label:'Jobs',num:1,v:h=>h.jobs,h:h=>h.jobs||'<span class="faint">0</span>'}
 ],HARN.filter(h=>F.harn==='all'||(F.harn==='ready'?h.st==='ready':h.st!=='ready')),{sort:{k:'st',d:1},stack:1,cls:h=>h.st==='missing'?'dim':'',empty:HARN.state==='checking'?checking():'No harnesses found'}))}<div class="legendrow"><span><b>Flags</b> Checked: the flags huddle passes are verified. Unverified: not yet</span><span><b>Access</b> what a job may do: readonly, exec, write</span><span><b>Report</b> how huddle reads the result: checked, extracted or free text</span></div></div>`}};

const wirePill=(on,label)=>WIRE.state==='checking'||on==null?`<span class="muted">Checking…</span>`:pill(on?'ok':'bad',label);
