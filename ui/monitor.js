// ---- Activity monitor ----
// The server sends one sample a second. The page keeps the last minute for the sparklines and
// draws the monitor only while it is on screen.
let tab='activity',monitorTarget=target,monitorPoll=null,monitorPollFast=false,monitorPolling=false,monitorReconnecting=false,monitorOwnsConn=false,lastSeq=-1,hello=null,sample=null,activityStale=false,actionId=0,procSort={key:'cpu',dir:-1},procFilter='',confirmResolve=null;
const HISTORY=60,sparks={};
const newMonitorHistory=()=>({cpu:[],mem:[],gpu:[],rx:[],tx:[],cores:[]});
function newMonitorSession(){return {who:'',active:false,ownsConn:false,poll:null,pollFast:false,polling:false,reconnecting:false,hello:null,sample:null,lastSeq:-1,hist:newMonitorHistory(),pendingActions:new Map(),machine:null,generation:0,stale:false,sub:'Not monitoring',noticeText:'',noticeKind:''}}
const monitorSessions={local:newMonitorSession(),remote:newMonitorSession()};
let pendingActions=new Map(),hist=newMonitorHistory();
const rate=n=>size(n)+'/s';
const pct0=n=>n===null||n===undefined||isNaN(n)?'–':Math.round(n)+'%';
function duration(s){s=Math.floor(s||0);const d=Math.floor(s/86400),h=Math.floor(s%86400/3600),m=Math.floor(s%3600/60);return d?`${d} d ${h} h`:h?`${h} h ${m} min`:`${m} min`}
const PRIVILEGE={root:['Root','Connected as root: you can end any process and free the cache.'],sudo:['Sudo','This account can use sudo without a password: you can end any process and free the cache.'],'sudo-password':['Sudo','This account is an administrator. You are asked for its sudo password to end other users\' processes or free the cache.'],none:['Standard user','You can end your own processes. Connect as root or a sudo user to end others or free the cache.'],admin:['Administrator','Running as administrator: you can end other users\' processes and free the cache.']};
const onWindows=()=>!!hello&&hello.platform==='windows';
function privilegeInfo(){const p=privilegeOf();if(p==='none'&&onWindows())return ['Standard user','You can end your own processes. Restart as administrator to end others or free the cache.'];return PRIVILEGE[p]||PRIVILEGE.none}
function privilegeOf(){return hello&&hello.privilege||'none'}
function canEnd(p,priv){return !!p&&!p.kernel&&!p.protected&&(p.own||priv==='root'||priv==='sudo'||priv==='sudo-password'||priv==='admin')}
function needsSudoPassword(p,priv){return priv==='sudo-password'&&(!p||!p.own)}
function canFreeCache(priv){return priv==='root'||priv==='sudo'||priv==='sudo-password'||priv==='admin'}
function pushHist(k,v,h=hist){const a=h[k];a.push(v);if(a.length>HISTORY)a.shift()}
function gpuAverage(s){const u=(s.gpus||[]).map(g=>g.util).filter(v=>typeof v==='number');return u.length?u.reduce((a,b)=>a+b,0)/u.length:null}
function netTotals(s){let rx=0,tx=0;for(const n of s.network||[]){rx+=n.rx||0;tx+=n.tx||0}return [rx,tx]}
function record(s,h=hist){
  const m=s.memory||{},[rx,tx]=netTotals(s);
  pushHist('cpu',s.cpu?s.cpu.total:0,h);pushHist('mem',m.total?100*m.used/m.total:0,h);pushHist('gpu',gpuAverage(s),h);pushHist('rx',rx,h);pushHist('tx',tx,h);
  const cores=s.cpu&&s.cpu.cores||[];
  if(h.cores.length!==cores.length)h.cores=cores.map(()=>[]);
  cores.forEach((v,i)=>{const a=h.cores[i];a.push(v);if(a.length>HISTORY)a.shift()});
}
// The app tells the page when other windows cover its window completely, which the browser
// cannot see; a minimised or hidden window makes document.hidden true instead.
let windowCovered=false;
const pageShown=()=>!windowCovered&&!(typeof document.hidden==='boolean'&&document.hidden);
const activityVisible=()=>tab==='activity'&&pageShown();
function showActivity(){if(sample&&activityStale&&activityVisible()){activityStale=false;renderActivity()}}
// Brings the open tab up to date when the window can be seen again.
function pageShownAgain(){if(!pageShown())return;showActivity();if(tab==='history'&&typeof loadResourceHistory==='function')loadResourceHistory(true)}
window.__setWindowCovered=c=>{const was=windowCovered;windowCovered=!!c;if(was&&!windowCovered)pageShownAgain()};
if(typeof isWindowCovered==='function')Promise.resolve(isWindowCovered()).then(c=>window.__setWindowCovered(c),()=>{});
const TAB_VIEWS={storage:['workspace','tabStorage'],activity:['activity','tabActivity'],history:['history','tabHistory'],events:['events','tabEvents']};
function setTab(t){
  if(!TAB_VIEWS[t])return;
  tab=t;
  for(const [name,[panel,button]] of Object.entries(TAB_VIEWS)){
    const selected=name===t;$(panel).classList.toggle('hidden',!selected);$(panel).setAttribute('aria-hidden',String(!selected));
    $(button).setAttribute('aria-selected',String(selected));$(button).tabIndex=selected?0:-1;
  }
  if(t==='storage')setTimeout(redraw,0);
  else if(t==='activity')setTimeout(()=>{showActivity();drawSparks()},0);
  else if(t==='history')setTimeout(()=>{loadHistoryMachineList();loadResourceHistory();drawResourceHistory()},0);
  else setTimeout(()=>{loadHistoryMachineList();if(eventsDirty)renderMonitorEvents();loadMonitorEvents(eventBaseline)},0);
}
const tabButtons=Object.values(TAB_VIEWS).map(([,button])=>button);
for(const [name,[,button]] of Object.entries(TAB_VIEWS)){
  $(button).onclick=()=>setTab(name);
  $(button).onkeydown=e=>{if(!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;e.preventDefault();const current=tabButtons.indexOf(button),next=e.key==='Home'?0:e.key==='End'?tabButtons.length-1:(current+(e.key==='ArrowRight'?1:-1)+tabButtons.length)%tabButtons.length;$(tabButtons[next]).click();$(tabButtons[next]).focus()};
}
if(typeof document.addEventListener==='function')document.addEventListener('visibilitychange',pageShownAgain);
function paintNotice(text,kind){const n=$('actNotice');$('actNoticeText').textContent=text;n.classList.toggle('error',kind==='error');n.classList.toggle('busy',kind==='busy');n.classList.toggle('hidden',!text)}
function notice(text,kind){const s=monitorSessions[target];s.noticeText=text;s.noticeKind=kind||'';paintNotice(text,kind)}
function monitorNotice(t,text,kind){const s=monitorSessions[t];s.noticeText=text;s.noticeKind=kind||'';if(t===target)paintNotice(text,kind)}
$('actNoticeClose').onclick=()=>notice('');
function saveMonitorTarget(t){
  const s=monitorSessions[t];if(!s||t!==target)return;
  Object.assign(s,{poll:monitorPoll,pollFast:monitorPollFast,polling:monitorPolling,reconnecting:monitorReconnecting,ownsConn:monitorOwnsConn,lastSeq,hello,sample,hist,pendingActions,stale:activityStale});
}
function syncMonitorGlobals(t){
  const s=monitorSessions[t];monitorTarget=t;monitorPoll=s.poll;monitorPollFast=s.pollFast;monitorPolling=s.polling;monitorReconnecting=s.reconnecting;monitorOwnsConn=s.ownsConn;lastSeq=s.lastSeq;hello=s.hello;sample=s.sample;hist=s.hist;pendingActions=s.pendingActions;activityStale=s.stale;
}
function paintMonitorHello(){
  const p=$('actPriv');p.classList.toggle('hidden',!hello);
  if(!hello)return;
  const [label,detail]=privilegeInfo();p.textContent=label;p.title=detail;p.dataset.level=hello.privilege;
  $('elevateButton').classList.toggle('hidden',!(onWindows()&&hello.privilege==='none'));
  $('cpuModel').textContent=hello.cpuModel||'';
  if(monitorTarget==='local'&&hello.system)$('actHost').textContent=hello.system;
}
function switchMonitorTarget(t){
  const s=monitorSessions[t];syncMonitorGlobals(t);
  $('actHost').textContent=s.who||(t==='local'?localName():serverWho)||'';
  $('actSub').textContent=s.sub;
  $('stopMonitorButton').classList.toggle('hidden',!s.active);$('liveDot').classList.toggle('hidden',!s.active);
  $('actEmpty').classList.toggle('hidden',s.active||!!s.sample);$('actBody').classList.toggle('hidden',!s.sample);Dash.available(!!s.sample);
  paintNotice(s.noticeText,s.noticeKind);paintMonitorHello();
  if(s.machine&&s.machine!==monitorMachine){monitorMachine=s.machine;selectHistoryMachine(s.machine);loadHistoryMachineList()}
  if(s.sample){if(activityVisible()){activityStale=false;s.stale=false;renderActivity()}else activityStale=true}
}
function beginMonitor(who,local=false,ownsConn=false){
  const t=local?'local':'remote',s=monitorSessions[t];
  clearInterval(s.poll);
  Object.assign(s,{who,active:true,ownsConn,pollFast:true,polling:false,reconnecting:false,hello:null,sample:null,lastSeq:-1,hist:newMonitorHistory(),pendingActions:new Map(),machine:null,stale:false,sub:'Connecting…',noticeText:'',noticeKind:''});
  s.generation++;
  if(t===target)switchMonitorTarget(t);
  // Poll often until the first figures arrive so they show as soon as they exist, then once a second.
  s.poll=setInterval(()=>pollMonitor(t),100);if(t===target)syncMonitorGlobals(t);pollMonitor(t);
}
async function endMonitor(text,error=false,t=target){
  const s=monitorSessions[t];s.generation++;clearInterval(s.poll);s.poll=null;s.active=false;s.polling=false;s.sub=text;
  const unanswered=s.pendingActions.size;s.pendingActions.clear();
  if(error){s.noticeText=text;s.noticeKind='error'}else if(unanswered){s.noticeText='Monitoring stopped before the server confirmed the last action.';s.noticeKind='error'}
  try{await stopMonitor(t)}catch(e){}
  if(t===target){syncMonitorGlobals(t);switchMonitorTarget(t);$('actSub').textContent=text}
  if(s.ownsConn&&t===target)conn(error?'error':'idle',error?'Monitor disconnected':'Not connected');
}
$('stopMonitorButton').onclick=()=>endMonitor('Monitoring stopped. The last values are shown.');
$('elevateButton').onclick=async()=>{try{notice('Waiting for Windows to grant administrator access…','busy');await restartElevated()}catch(e){notice(String(e),'error')}};
async function pollMonitor(t=target){
  const state=monitorSessions[t];if(state.polling||!state.poll)return;state.polling=true;
  const generation=state.generation;
  try{
    const s=await monitorStatus(t);
    if(!state.poll||!state.active||state.generation!==generation)return;
    state.machine=s.machine||state.machine;
    if(t===target&&state.machine&&state.machine!==monitorMachine){monitorMachine=state.machine;selectHistoryMachine(state.machine);loadHistoryMachineList()}
    if(s.hello&&!state.hello){
      state.hello=s.hello;
      if(t===target){syncMonitorGlobals(t);paintMonitorHello()}
      if(state.ownsConn&&t===target)conn('ok',$('connText').dataset.who||state.hello.host);
    }
    for(const raw of s.results||[]){
      if(!state.pendingActions.has(raw.id))continue;
      state.pendingActions.delete(raw.id);monitorNotice(t,raw.message,raw.ok?'ok':'error');
    }
    if(s.reconnecting&&!state.reconnecting){state.reconnecting=true;monitorNotice(t,'The connection stopped responding. Reconnecting…','busy')}
    else if(!s.reconnecting&&state.reconnecting){state.reconnecting=false;monitorNotice(t,'','')}
    if(s.sample&&s.seq!==state.lastSeq){
      state.lastSeq=s.seq;state.sample=s.sample;record(state.sample,state.hist);state.stale=true;
      if(t===target){syncMonitorGlobals(t);if(activityVisible()){activityStale=false;state.stale=false;renderActivity()}else activityStale=true}
    }
    if(state.sample&&state.pollFast){clearInterval(state.poll);state.pollFast=false;state.poll=setInterval(()=>pollMonitor(t),1000);if(t===target)syncMonitorGlobals(t)}
    if(!s.running){await endMonitor(s.error||'Monitoring stopped.',!!s.error,t)}
    else if(state.hello&&state.ownsConn&&t===target&&$('conn').dataset.state==='busy')conn('ok',$('connText').dataset.who||state.hello.host);
  }catch(e){if(state.generation===generation&&state.active)await endMonitor(String(e),true,t)}
  finally{state.polling=false;if(t===target)monitorPolling=false}
}
function kv(label,value){const d=el('div');d.append(el('span','',label),el('b','',value));return d}
function meter(parts){const m=el('div','meter');for(const [v,cls] of parts){const i=el('i',cls||'');i.style.width=Math.max(0,Math.min(100,v)).toFixed(2)+'%';m.append(i)}return m}
function keyChip(color,text){const k=el('span','key'),i=el('i');i.style.background=color;k.append(i,text);return k}
function meterLine(label,value,text){const d=el('div','meterline');d.append(el('span','',label),meter([[value]]),el('span','',text));return d}
function renderActivity(){
  const s=sample,m=s.memory||{},cpu=s.cpu||{},cores=cpu.cores||[],priv=privilegeOf(),[rx,tx]=netTotals(s);
  $('actEmpty').classList.add('hidden');$('actBody').classList.remove('hidden');Dash.available(true);
  $('actSub').textContent=[monitorTarget!=='local'&&hello&&hello.system,s.uptime?'up '+duration(s.uptime):'',(s.load||[]).length?'load '+s.load.map(v=>v.toFixed(2)).join(' '):'','updated '+new Date(s.time*1000).toLocaleTimeString()].filter(Boolean).join(' · ');
  // Tiles
  $('tCpu').textContent=pct0(cpu.total);$('tCpuSub').textContent=`${cores.length} ${onWindows()?'logical processors':'cores'}${(s.load||[]).length?' · load '+s.load[0].toFixed(2):''}`;
  $('tMem').textContent=pct0(m.total?100*m.used/m.total:0);$('tMemSub').textContent=`${size(m.used)} of ${size(m.total)} in use`;
  const gpus=s.gpus||[],g=gpuAverage(s);
  if($('actBody').classList.contains('no-gpu')!==!gpus.length){$('actBody').classList.toggle('no-gpu',!gpus.length);Dash.spans()}
  $('tGpu').textContent=gpus.length?pct0(g):'None';
  $('tGpuSub').textContent=gpus.length?(gpus.length>1?gpus.length+' GPUs':gpus[0].name)+(gpus[0].memTotal?` · ${size(gpus.reduce((a,x)=>a+(x.memUsed||0),0))} VRAM`:''):(s.gpuNote||'');
  $('tGpuSub').title=$('tGpuSub').textContent;
  $('tNetRx').textContent=rate(rx);$('tNetTx').textContent=rate(tx);$('tNetSub').textContent=`${(s.network||[]).length} interfaces`;
  drawSparks();
  // Processor
  const k1=$('cpuStats');
  k1.replaceChildren(kv('Total',pct0(cpu.total)),kv('User',pct0(cpu.user)),kv(onWindows()?'Kernel':'System',pct0(cpu.system)));
  if(cpu.iowait!==undefined)k1.append(kv('I/O wait',pct0(cpu.iowait)));
  if((s.load||[]).length){const l=kv('Load 1 / 5 / 15 min',s.load.map(v=>v.toFixed(2)).join(' / '));l.className='wide';k1.append(l)}
  k1.append(kv('Uptime',s.uptime?duration(s.uptime):'–'));
  // Memory
  const mp=$('memPanel'),cache=(m.cached||0)+(m.buffers||0),keys=el('div','keyrow');
  keys.append(keyChip('var(--chart-1)',`In use ${size(m.used)}`),keyChip('var(--chart-2)',`${onWindows()?'Standby':'Cache'} ${size(cache)}`),keyChip('var(--border-strong)',`Free ${size(m.free)}`));
  const k2=el('div','kvs');k2.append(kv('Total',size(m.total)),kv('Available',size(m.available)));if(m.shmem!==undefined)k2.append(kv('Shared',size(m.shmem)));if(m.dirty!==undefined)k2.append(kv(onWindows()?'Modified':'Waiting to be written',size(m.dirty)));if(m.wired!==undefined)k2.append(kv('Wired',size(m.wired)),kv('Compressed',size(m.compressed)));
  const parts=[meter([[m.total?100*m.used/m.total:0],[m.total?100*Math.min(cache,m.total-m.used)/m.total:0,'s2']]),keys,k2];
  if(m.commitLimit)parts.push(meterLine('Committed',100*m.commitUsed/m.commitLimit,`${size(m.commitUsed)} of ${size(m.commitLimit)}`));
  else parts.push(m.swapTotal?meterLine('Swap',100*m.swapUsed/m.swapTotal,`${size(m.swapUsed)} of ${size(m.swapTotal)}`):el('p','muted','No swap'));
  mp.replaceChildren(...parts);
  $('freeCacheButton').classList.toggle('hidden',!canFreeCache(priv));
  // Graphics
  const gp=$('gpuPanel');
  if(!gpus.length)gp.replaceChildren(el('p','muted',s.gpuNote||'No GPU found.'));
  else gp.replaceChildren(...gpus.map(x=>{const d=el('div','gpu'),k=el('div','kvs');d.append(el('b','',x.name));
    d.append(meterLine('Use',x.util||0,typeof x.util==='number'?pct0(x.util):'Not reported'));
    if(x.memTotal)d.append(meterLine('Memory',100*(x.memUsed||0)/x.memTotal,`${size(x.memUsed)} of ${size(x.memTotal)}`));
    k.append(kv('Temperature',typeof x.temp==='number'&&x.temp>0?Math.round(x.temp)+' °C':'–'),kv('Power',typeof x.power==='number'?x.power.toFixed(0)+' W':'–'),kv('Vendor',x.vendor||'–'));d.append(k);return d}));
  // Network
  const np=$('netPanel'),nets=s.network||[];
  if(!nets.length)np.replaceChildren(el('p','muted','No network interfaces reported.'));
  else{const t=el('table','mini'),h=el('tr');for(const x of ['Interface','In','Out','Received','Sent'])h.append(el('th','',x));t.append(h);
    for(const n of [...nets].sort((a,b)=>(b.rx+b.tx)-(a.rx+a.tx))){const r=el('tr'),name=el('td','',n.name);name.title=n.name;r.append(name,el('td','',rate(n.rx)),el('td','',rate(n.tx)),el('td','',size(n.rxTotal)),el('td','',size(n.txTotal)));t.append(r)}
    np.replaceChildren(t)}
  renderProcesses();
}
const PROC_SORTS={name:(a,b)=>a.name.localeCompare(b.name,undefined,{sensitivity:'base'}),pid:(a,b)=>a.pid-b.pid,user:(a,b)=>a.user.localeCompare(b.user),cpu:(a,b)=>a.cpu-b.cpu,rss:(a,b)=>a.rss-b.rss};
function filterProcesses(list,q){q=(q||'').trim().toLowerCase();if(!q)return list;return list.filter(p=>p.name.toLowerCase().includes(q)||String(p.pid)===q||p.user.toLowerCase().includes(q)||(p.cmd||'').toLowerCase().includes(q))}
// The process table keeps one row per process and updates it in place each second, so rows do
// not flicker under the pointer and the page does little work on slow machines.
const procRows=new Map();let procTable=null,procEmpty=null;
function setText(node,text){if(node.textContent!==text)node.textContent=text}
function procHead(){
  if(!procTable){
    procTable=el('table');procTable.innerHTML='<colgroup><col><col class="c-pid"><col class="c-user"><col class="c-cpu"><col class="c-mem"><col class="c-act"></colgroup><thead><tr></tr></thead><tbody></tbody>';
    $('procList').replaceChildren(procTable);
  }
  const key=procSort.key+procSort.dir;if(procTable.dataset.sort===key)return;procTable.dataset.sort=key;
  const hr=procTable.tHead.rows[0];hr.replaceChildren();
  for(const [k,label] of [['name','Name'],['pid','PID'],['user','User'],['cpu','CPU'],['rss','Memory'],['','']]){
    const th=el('th',k==='user'?'left':'',label);
    if(k){if(procSort.key===k){th.setAttribute('aria-sort',procSort.dir<0?'descending':'ascending');th.append(el('span','arrow',procSort.dir<0?'▼':'▲'))}
      th.onclick=()=>{procSort=procSort.key===k?{key:k,dir:-procSort.dir}:{key:k,dir:k==='name'||k==='user'||k==='pid'?1:-1};renderProcesses()}}
    else th.style.cursor='default';
    hr.append(th);
  }
}
function procRow(){
  const r={tr:el('tr'),nameTd:el('td'),n:el('span'),cmd:el('span','cmd'),pid:cell('','num'),user:cell('','left num'),cpu:cell(''),mem:cell(''),end:el('button','btn sm','End'),kill:el('button','btn sm danger','Force end'),p:null};
  const name=el('div','name'),acts=el('td'),wrap=el('div','acts');
  name.append(r.n,r.cmd);r.nameTd.append(name);r.end.type=r.kill.type='button';wrap.append(r.end,r.kill);acts.append(wrap);
  r.end.onclick=e=>{e.stopPropagation();endProcess(r.p,false)};r.kill.onclick=e=>{e.stopPropagation();endProcess(r.p,true)};
  r.tr.append(r.nameTd,r.pid,r.user,r.cpu,r.mem,acts);
  return r;
}
function renderProcesses(){
  if(!sample)return;
  const priv=privilegeOf(),all=sample.processes||[],list=filterProcesses(all,procFilter).sort((a,b)=>procSort.dir*PROC_SORTS[procSort.key](a,b)||b.cpu-a.cpu||b.rss-a.rss);
  $('procCount').textContent=`Top ${count(all.length)} of ${count(sample.processCount)}`;
  procHead();
  const body=procTable.tBodies[0],seen=new Set();let prev=null;
  if(procEmpty){procEmpty.remove();procEmpty=null}
  for(const p of list){
    const key=p.pid+':'+(p.start||0);seen.add(key);
    let r=procRows.get(key);if(!r){r=procRow();procRows.set(key,r)}
    r.p=p;setText(r.n,p.name);setText(r.cmd,p.cmd||'');r.cmd.hidden=!p.cmd;
    const title=p.cmd||p.name;if(r.nameTd.title!==title)r.nameTd.title=title;
    setText(r.pid,String(p.pid));setText(r.user,p.user||'–');setText(r.cpu,p.cpu.toFixed(1)+'%');setText(r.mem,size(p.rss));
    const ok=canEnd(p,priv),why=p.kernel?'Kernel threads cannot be ended.':p.protected?'This process runs the system or this monitoring session.':ok?'':'You can only end your own processes. Connect as root or a sudo user to end this one.';
    r.end.disabled=r.kill.disabled=!ok;r.end.title=why||'Ask the process to exit (SIGTERM).';r.kill.title=why||'Stop immediately (SIGKILL); unsaved work is lost.';
    const want=prev?prev.nextSibling:body.firstChild;if(want!==r.tr)body.insertBefore(r.tr,want);
    prev=r.tr;
  }
  for(const [key,r] of procRows)if(!seen.has(key)){r.tr.remove();procRows.delete(key)}
  if(!list.length){procEmpty=el('tr','more');const td=cell(procFilter?'No matching processes among the busiest shown.':'No processes reported.');td.colSpan=6;procEmpty.append(td);body.append(procEmpty)}
}
$('procFilter').oninput=()=>{procFilter=$('procFilter').value;renderProcesses()};
// Confirmation dialog for actions that change the server.
function confirmAction({title,text,detail,password,label,detailLabel}){
  $('confirmTitle').textContent=title;$('confirmText').textContent=text;$('confirmOk').textContent=label;
  $('confirmDetail').textContent=detail||'';$('confirmDetailBox').classList.toggle('hidden',!detail);$('confirmDetailBox').firstElementChild.textContent=detailLabel||'Process';
  $('sudoField').classList.toggle('hidden',!password);$('sudoPassword').value='';$('sudoPassword').required=!!password;
  $('sudoLabel').textContent='sudo password for '+(hello&&hello.user||'this account');
  $('confirm').classList.remove('hidden');(password?$('sudoPassword'):$('confirmCancel')).focus();
  return new Promise(resolve=>{confirmResolve=resolve});
}
function closeConfirm(result){const r=confirmResolve;confirmResolve=null;$('confirm').classList.add('hidden');$('sudoPassword').value='';if(r)r(result)}
$('confirmCancel').onclick=()=>closeConfirm(null);
$('confirmForm').onsubmit=e=>{e.preventDefault();closeConfirm({password:$('sudoPassword').value})};
async function sendAction(action,label,t=target){
  const state=monitorSessions[t],id=++actionId;state.pendingActions.set(id,label);monitorNotice(t,label,'busy');
  try{await monitorAction(t,{...action,id})}catch(e){state.pendingActions.delete(id);monitorNotice(t,String(e),'error')}
}
async function endProcess(p,force){
  const t=target,priv=privilegeOf(),password=needsSudoPassword(p,priv);
  const r=await confirmAction({title:(force?'Force end ':'End ')+p.name+'?',label:force?'Force end':'End process',password,
    text:(force?'It stops immediately; unsaved work is lost.':onWindows()?'Its windows are asked to close so it can save its work.':'It is asked to exit so it can save its work.')+(p.own?'':' It belongs to '+(p.user||'another account')+(onWindows()?'.':', so sudo is used.')),
    detail:`PID ${p.pid} · ${p.user} · ${size(p.rss)}\n${p.cmd||p.name}`});
  if(!r)return;
  await sendAction({type:'kill',pid:p.pid,start:p.start,signal:force?'KILL':'TERM',password:password?r.password:''},`${force?'Force-ending':'Ending'} ${p.name} (PID ${p.pid})…`,t);
}
$('freeCacheButton').onclick=async()=>{
  const t=target,priv=privilegeOf(),password=needsSudoPassword(null,priv);
  const r=await confirmAction({title:'Free the memory cache?',label:'Free cache',password,
    text:(hello&&hello.platform==='darwin'?'Runs purge.':onWindows()?'Clears the standby list.':'Runs drop_caches.')+' Programs keep their memory, but file access may be slower for a while. Rarely needed: the system frees cache by itself.'});
  if(!r)return;
  await sendAction({type:'dropcaches',password:password?r.password:''},'Freeing the memory cache…',t);
};
// Sparklines: one value axis per chart, newest sample on the right, with a hover readout.
function drawSparks(){
  if(tab!=='activity'||!sample)return;
  const netMax=Math.max(1024,...hist.rx,...hist.tx);
  sparks.sCpu={series:[{values:hist.cpu,color:'--chart-1'}],max:100,fmt:v=>pct0(v[0])};
  sparks.sMem={series:[{values:hist.mem,color:'--chart-1'}],max:100,fmt:v=>pct0(v[0])};
  sparks.sGpu={series:[{values:hist.gpu,color:'--chart-1'}],max:100,fmt:v=>v[0]===null||v[0]===undefined?'No data':pct0(v[0])};
  sparks.sNet={series:[{values:hist.rx,color:'--chart-1'},{values:hist.tx,color:'--chart-2'}],max:netMax,fmt:v=>`In ${rate(v[0])} · Out ${rate(v[1])}`};
  for(const id in sparks)drawSpark(id);
  drawCores();
}
function withAlpha(color,a){const m=/^#([0-9a-f]{6})$/i.exec(color);return m?'rgba('+[0,2,4].map(i=>parseInt(m[1].slice(i,i+2),16)).join(',')+','+a+')':color}
function drawSpark(id){
  const c=$(id),cfg=sparks[id];if(!c||!cfg||!c.getContext)return;
  const W=c.clientWidth,H=c.clientHeight;if(W<4||H<4)return;
  const dpr=Math.min(2,window.devicePixelRatio||1),st=getComputedStyle(document.documentElement),cv=name=>st.getPropertyValue(name).trim();
  c.width=Math.round(W*dpr);c.height=Math.round(H*dpr);
  const ctx=c.getContext('2d');ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,W,H);
  // Faint guides at the middle and the floor give the empty start of the minute a frame.
  ctx.strokeStyle=cv('--border');ctx.lineWidth=1;ctx.setLineDash([2,3]);ctx.beginPath();ctx.moveTo(0,Math.round(H/2)+.5);ctx.lineTo(W,Math.round(H/2)+.5);ctx.stroke();ctx.setLineDash([]);
  const step=W/(HISTORY-1),y=v=>H-1.5-Math.min(1,Math.max(0,v/cfg.max))*(H-6);
  for(const s of cfg.series){
    const col=cv(s.color),off=HISTORY-s.values.length;let started=false,first=0,last=0;
    ctx.beginPath();
    s.values.forEach((v,i)=>{if(v===null||v===undefined)return;const x=(off+i)*step;if(!started){ctx.moveTo(x,y(v));first=x;started=true}else ctx.lineTo(x,y(v));last=x});
    if(!started)continue;
    ctx.lineWidth=1.5;ctx.lineJoin='round';ctx.lineCap='round';ctx.strokeStyle=col;ctx.stroke();
    if(cfg.series.length===1){ctx.lineTo(last,H);ctx.lineTo(first,H);ctx.closePath();const g=ctx.createLinearGradient(0,0,0,H);g.addColorStop(0,withAlpha(col,.26));g.addColorStop(1,withAlpha(col,0));ctx.fillStyle=g;ctx.fill()}
  }
  if(c._hover!==undefined&&c._hover!==null){
    const x=c._hover*step;ctx.strokeStyle=cv('--text-faint');ctx.lineWidth=1;ctx.beginPath();ctx.moveTo(x+.5,0);ctx.lineTo(x+.5,H);ctx.stroke();
    for(const s of cfg.series){const v=s.values[c._hover-(HISTORY-s.values.length)];if(v===null||v===undefined)continue;
      ctx.fillStyle=cv(s.color);ctx.strokeStyle=cv('--surface');ctx.lineWidth=2;ctx.beginPath();ctx.arc(x,y(v),3.5,0,Math.PI*2);ctx.fill();ctx.stroke()}
  }
}
// One small graph per logical processor, all drawn on one canvas so that many cores stay cheap.
// They fill the space the Processor section is given. Past CARD_LIMIT cores, or when the graphs
// would be too short to read, the cores are drawn as a heat map instead: one thin row per core,
// the last minute across, brighter when busier.
const CARD_LIMIT=32,CARD_MIN_HEIGHT=44,coreGrid={mode:'',cells:[],map:null,hover:null};
function coreLayout(W,n,H){
  // Try each number of rows and keep the one whose graphs are closest to a 1.6:1 shape, so they
  // fill the space without turning into slivers. None fits when every choice is too small.
  const gap=8;let best=null;
  for(let rows=1;rows<=n;rows++){
    const cols=Math.ceil(n/rows),w=(W-gap*(cols-1))/cols,h=Math.floor((H-gap*(rows-1))/rows);
    if(w<64)continue;if(h<CARD_MIN_HEIGHT)break;
    const score=Math.abs(Math.log(w/h/1.6))+0.3*(rows*cols-n)/n;
    if(!best||score<best.score)best={gap,cols,rows,w,h,score};
  }
  return best;
}
function heatLayout(W,n,H){
  const left=58,top=30,bottom=20,row=Math.max(.5,Math.min(10,(H-top-bottom)/n));
  return {top,row,x:left,y:top,w:W-left,h:n*row};
}
function roundRect(ctx,x,y,w,h,r){ctx.beginPath();ctx.moveTo(x+r,y);ctx.arcTo(x+w,y,x+w,y+h,r);ctx.arcTo(x+w,y+h,x,y+h,r);ctx.arcTo(x,y+h,x,y,r);ctx.arcTo(x,y,x+w,y,r);ctx.closePath()}
const rgbOf=c=>{const m=/^#([0-9a-f]{6})$/i.exec(c);return m?[0,2,4].map(i=>parseInt(m[1].slice(i,i+2),16)):[128,128,128]};
// Square root, so light load still shows against idle cores.
const heatShade=v=>0.08+0.92*Math.sqrt(v);
let heatImage=null;
function drawCores(){
  const c=$('coreChart');if(!c||!c.getContext||tab!=='activity'||!sample)return;
  const n=hist.cores.length,W=c.clientWidth;
  c.parentElement.classList.toggle('hidden',!n);if(!n||W<40)return;
  const H=c.clientHeight;if(H<20)return;
  const cards=n<=CARD_LIMIT&&coreLayout(W,n,H),heat=!cards,L=heat?heatLayout(W,n,H):cards;
  coreGrid.mode=heat?'heat':'cards';
  const dpr=Math.min(2,window.devicePixelRatio||1),st=getComputedStyle(document.documentElement),cv=name=>st.getPropertyValue(name).trim();
  if(c.width!==Math.round(W*dpr)||c.height!==Math.round(H*dpr)){c.width=Math.round(W*dpr);c.height=Math.round(H*dpr)}
  const ctx=c.getContext('2d');ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,W,H);
  const k={font:getComputedStyle(c).fontFamily,line:cv('--chart-1'),border:cv('--border'),raised:cv('--surface-raised'),muted:cv('--text-muted'),text:cv('--text'),warn:cv('--warning-foreground'),faint:cv('--text-faint')};
  if(heat)drawHeat(ctx,L,n,k);else drawCards(ctx,L,n,k);
}
function drawCards(ctx,L,n,k){
  const hv=coreGrid.hover;
  // Narrow graphs show only the core number, so the name never runs into the percentage.
  ctx.font=`600 11px ${k.font}`;const room=L.w-22-ctx.measureText('100%').width;
  ctx.font=`500 11px ${k.font}`;const prefix=ctx.measureText('CPU '+(n-1)).width<=room?'CPU ':'';
  coreGrid.cells=[];
  for(let i=0;i<n;i++){
    const col=i%L.cols,row=Math.floor(i/L.cols),x=col*(L.w+L.gap),y=row*(L.h+L.gap),w=L.w,h=L.h,values=hist.cores[i];
    coreGrid.cells.push({x,y,w,h});
    roundRect(ctx,x+.5,y+.5,w-1,h-1,6);ctx.fillStyle=k.raised;ctx.fill();ctx.strokeStyle=k.border;ctx.lineWidth=1;ctx.stroke();
    const now=values[values.length-1],top=y+(h>=52?22:18),gh=y+h-2-top;
    ctx.font=`500 11px ${k.font}`;ctx.textBaseline='alphabetic';ctx.fillStyle=k.muted;ctx.textAlign='left';ctx.fillText(prefix+i,x+8,top-7);
    ctx.font=`600 11px ${k.font}`;ctx.textAlign='right';ctx.fillStyle=now>=90?k.warn:k.text;ctx.fillText(pct0(now),x+w-8,top-7);
    // The graph sits inside the cell, newest sample on the right.
    ctx.save();roundRect(ctx,x+1,y+1,w-2,h-2,5);ctx.clip();
    const step=(w-2)/(HISTORY-1),off=HISTORY-values.length,px=j=>x+1+(off+j)*step,py=v=>top+gh-Math.min(100,Math.max(0,v||0))/100*gh;
    ctx.strokeStyle=k.border;ctx.setLineDash([2,3]);ctx.beginPath();ctx.moveTo(x+1,Math.round(top+gh/2)+.5);ctx.lineTo(x+w-1,Math.round(top+gh/2)+.5);ctx.stroke();ctx.setLineDash([]);
    if(values.length){
      ctx.beginPath();values.forEach((v,j)=>j?ctx.lineTo(px(j),py(v)):ctx.moveTo(px(j),py(v)));
      ctx.lineWidth=1.25;ctx.lineJoin='round';ctx.strokeStyle=k.line;ctx.stroke();
      ctx.lineTo(px(values.length-1),y+h);ctx.lineTo(px(0),y+h);ctx.closePath();ctx.fillStyle=withAlpha(k.line,.22);ctx.fill();
    }
    if(hv&&hv.cell===i){const j=hv.index-off;if(j>=0&&j<values.length){const hx=px(j);ctx.strokeStyle=k.faint;ctx.beginPath();ctx.moveTo(Math.round(hx)+.5,top);ctx.lineTo(Math.round(hx)+.5,y+h);ctx.stroke();ctx.fillStyle=k.line;ctx.beginPath();ctx.arc(hx,py(values[j]),3,0,Math.PI*2);ctx.fill()}}
    ctx.restore();
  }
}
// The heat map is built as a 60 by n pixel image, one pixel per core and second, then scaled up.
function drawHeat(ctx,L,n,k){
  coreGrid.map=L;
  const base=rgbOf(k.raised),hot=rgbOf(k.line),shade=f=>base.map((b,j)=>b+(hot[j]-b)*f);
  if(!heatImage||heatImage.height!==n){heatImage=document.createElement('canvas');heatImage.width=HISTORY;heatImage.height=n}
  const hc=heatImage.getContext('2d'),img=hc.createImageData(HISTORY,n),d=img.data;
  for(let i=0;i<n;i++){
    const values=hist.cores[i],off=HISTORY-values.length;
    for(let t=0;t<HISTORY;t++){
      const o=(i*HISTORY+t)*4,c=t<off?base:shade(heatShade(Math.min(100,Math.max(0,values[t-off]||0))/100));
      d[o]=c[0];d[o+1]=c[1];d[o+2]=c[2];d[o+3]=255;
    }
  }
  hc.putImageData(img,0,0);
  ctx.imageSmoothingEnabled=false;ctx.drawImage(heatImage,L.x,L.y,L.w,L.h);
  ctx.strokeStyle=k.border;ctx.lineWidth=1;ctx.strokeRect(L.x-.5,L.y-.5,L.w+1,L.h+1);
  // Caption and colour key above, core numbers on the left, time below.
  const mid=L.top/2-2;
  ctx.font=`500 11px ${k.font}`;ctx.textBaseline='middle';ctx.fillStyle=k.muted;ctx.textAlign='left';
  ctx.fillText(`${n} logical processors`,0,mid);
  const hundred=ctx.measureText('100%').width,kw=80,kx=L.x+L.w-hundred-6-kw,g=ctx.createLinearGradient(kx,0,kx+kw,0);
  for(const s of [0,.25,.5,.75,1])g.addColorStop(s,`rgb(${shade(heatShade(s)).map(Math.round).join(',')})`);
  ctx.fillStyle=g;ctx.fillRect(kx,mid-4,kw,8);ctx.strokeStyle=k.border;ctx.strokeRect(kx-.5,mid-4.5,kw+1,9);
  ctx.fillStyle=k.faint;ctx.textAlign='right';ctx.fillText('0%',kx-6,mid);ctx.textAlign='left';ctx.fillText('100%',kx+kw+6,mid);
  ctx.textAlign='right';
  let every=1;while(every*L.row<16)every*=2;
  for(let i=0;i<n;i+=every)ctx.fillText('CPU '+i,L.x-8,L.y+(i+.5)*L.row);
  ctx.textBaseline='alphabetic';ctx.textAlign='left';ctx.fillText('1 min ago',L.x,L.y+L.h+15);ctx.textAlign='right';ctx.fillText('now',L.x+L.w,L.y+L.h+15);
  const hv=coreGrid.hover;
  if(hv){
    const y=L.y+hv.cell*L.row,x=L.x+(hv.index+.5)*L.w/HISTORY;
    ctx.strokeStyle=k.text;ctx.lineWidth=1;ctx.strokeRect(L.x-.5,Math.round(y)-.5,L.w+1,Math.max(1,Math.round(L.row))+1);
    ctx.strokeStyle=withAlpha(k.text,.45);ctx.beginPath();ctx.moveTo(Math.round(x)+.5,L.y);ctx.lineTo(Math.round(x)+.5,L.y+L.h);ctx.stroke();
  }
}
{
  const c=$('coreChart');
  if(c&&c.getBoundingClientRect){
    const tip=c.nextElementSibling,hide=()=>{coreGrid.hover=null;tip.classList.add('hidden');drawCores()};
    const ago=index=>HISTORY-1-index?(HISTORY-1-index)+' s ago':'now';
    c.onmousemove=e=>{
      const r=c.getBoundingClientRect(),mx=e.clientX-r.left,my=e.clientY-r.top;
      let i,index,tx,ty;
      if(coreGrid.mode==='heat'){
        const L=coreGrid.map;
        if(!L||mx<L.x||mx>=L.x+L.w||my<L.y||my>=L.y+L.h){if(coreGrid.hover)hide();return}
        i=Math.min(hist.cores.length-1,Math.floor((my-L.y)/L.row));index=Math.min(HISTORY-1,Math.floor((mx-L.x)/(L.w/HISTORY)));tx=mx;ty=L.y+i*L.row-6;
      }else{
        i=coreGrid.cells.findIndex(q=>mx>=q.x&&mx<q.x+q.w&&my>=q.y&&my<q.y+q.h);
        if(i<0){if(coreGrid.hover)hide();return}
        const q=coreGrid.cells[i];index=Math.max(0,Math.min(HISTORY-1,Math.round((mx-q.x-1)/((q.w-2)/(HISTORY-1)))));tx=q.x+(index/(HISTORY-1))*q.w;ty=q.y-4;
      }
      const values=hist.cores[i]||[],j=index-(HISTORY-values.length);
      if(j<0){if(coreGrid.hover)hide();return}
      coreGrid.hover={cell:i,index};drawCores();
      tip.textContent=`CPU ${i} · ${pct0(values[j])} · ${ago(index)}`;
      tip.style.left=Math.max(60,Math.min(r.width-60,tx))+'px';tip.style.top=ty+'px';tip.classList.remove('hidden');
    };
    c.onmouseleave=hide;
  }
}
for(const id of ['sCpu','sMem','sGpu','sNet']){
  const c=$(id);
  c.onmousemove=e=>{
    const cfg=sparks[id];if(!cfg)return;
    const r=c.getBoundingClientRect(),i=Math.max(0,Math.min(HISTORY-1,Math.round((e.clientX-r.left)/(r.width/(HISTORY-1))))),n=cfg.series[0].values.length,off=HISTORY-n;
    const tip=c.nextElementSibling;
    if(i<off){c._hover=null;tip.classList.add('hidden');drawSpark(id);return}
    c._hover=i;drawSpark(id);
    tip.textContent=cfg.fmt(cfg.series.map(s=>s.values[i-(HISTORY-s.values.length)]))+' · '+(HISTORY-1-i?(HISTORY-1-i)+' s ago':'now');
    tip.style.left=Math.max(60,Math.min(r.width-60,i*r.width/(HISTORY-1)))+'px';tip.classList.remove('hidden');
  };
  c.onmouseleave=()=>{c._hover=null;c.nextElementSibling.classList.add('hidden');drawSpark(id)};
}
