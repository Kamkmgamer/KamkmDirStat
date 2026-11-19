const $=id=>document.getElementById(id);
let pending=null,viewId=null,lastView=null,poll=null,polling=false,refreshing=null,refreshAgain=false,leaves=[],tops=new Map(),mode='',tick=0,resizeTimer=null,sortBy={key:'size',dir:-1},typeColor=new Map();
const prefs=typeof window.Prefs==='object'&&window.Prefs?window.Prefs:null;
const size=n=>{if(!n||n<1)return '0 B';let i=Math.min(4,Math.floor(Math.log(n)/Math.log(1024)));return (n/1024**i).toFixed(i?1:0)+' '+['B','KiB','MiB','GiB','TiB'][i]};
const count=n=>(n||0).toLocaleString();
const pct=(a,b)=>b?(100*a/b).toFixed(1)+'%':'0.0%';
function el(tag,cls,text){const e=document.createElement(tag);if(cls)e.className=cls;if(text!==undefined)e.textContent=text;return e}
function icon(name,cls){const ns='http://www.w3.org/2000/svg',s=document.createElementNS(ns,'svg'),u=document.createElementNS(ns,'use');s.setAttribute('class','i'+(cls?' '+cls:''));u.setAttribute('href','#i-'+name);s.append(u);return s}
function status(message,error=false,busy=false){$('status').textContent=message;$('status').title='';$('status').classList.toggle('error',error);$('status').classList.toggle('busy',busy&&!error)}
function conn(state,text){$('conn').dataset.state=state;$('connText').textContent=text}
function showTrust(on){$('trust').classList.toggle('hidden',!on);if(on)$('reject').focus()}
$('method').onchange=()=>{let key=$('method').value==='key';$('passwordFields').classList.toggle('hidden',$('method').value!=='password');$('keyFields').classList.toggle('hidden',!key)};
let target='local',drives=[],platform='windows';
function newStorageSession(){return {who:'',viewId:null,lastView:null,poll:null,polling:false,refreshing:null,refreshAgain:false,leaves:[],tops:new Map(),mode:'',tick:0,scanReady:false,status:null,needsFinal:false,footer:'Ready',footerTitle:'',statusText:'Ready',statusError:false,statusBusy:false,filesMode:false,flashTarget:null,findKind:'all',findSort:'',typesLoaded:false,types:[],searchSeq:0,searchTimer:null,findScope:'',savedListing:null,searchValue:'',extValue:''}}
const storageSessions={local:newStorageSession(),remote:newStorageSession()};
const localName=()=>platform==='darwin'?'This Mac':platform==='linux'?'This computer':'This PC';
// The page starts with Windows wording and adjusts once the app reports its platform.
function applyPlatform(){
  $('localTargetName').textContent=localName();
  $('localFolder').placeholder=platform==='windows'?'C:\\Users\\you\\Downloads':platform==='darwin'?'/Users/you/Downloads':'/home/you/Downloads';
  if(platform==='darwin'){$('findKey').textContent='⌘F';$('footerKeys').innerHTML='<kbd>⌘</kbd> <kbd>F</kbd> search &nbsp; <kbd>⌘</kbd> <kbd>1</kbd> <kbd>2</kbd> <kbd>3</kbd> <kbd>4</kbd> view &nbsp; <kbd>⌘</kbd> <kbd>B</kbd> panel &nbsp; <kbd>⌘</kbd> <kbd>,</kbd> appearance'}
  syncFind();
}
try{if((prefs?prefs.get('target'):localStorage.getItem('kamkm.target'))==='remote')target='remote'}catch(e){}
function selectedDrives(){return drives.filter(d=>d.checked).map(d=>d.path)}
function connection(){
  if(target==='local'){const folder=$('localFolder').value.trim();return {local:true,paths:folder?[folder]:selectedDrives()}}
  let method=$('method').value,key=method==='key';return {method,host:$('host').value.trim(),port:$('port').value.trim(),user:$('user').value.trim(),password:method==='password'?$('password').value:'',keyFile:key?$('keyFile').value.trim():'',passphrase:key?$('passphrase').value:'',path:$('path').value.trim()||'/',skipVirtual:$('skipVirtual').checked,serverReport:true,serverHelper:!!$('serverHelper').checked}
}
function syncTools(){
  const local=target==='local';
  $('connect').classList.toggle('is-local',local);$('targetLocal').checked=local;$('targetRemote').checked=!local;
  $('targetLocal').setAttribute('aria-checked',String(local));$('targetRemote').setAttribute('aria-checked',String(!local));
  $('path').required=!local;$('host').required=!local;
  $('submitLabel').textContent=local?'Scan':'Connect';
}
function saveStorageTarget(t){
  const s=storageSessions[t];if(!s)return;
  Object.assign(s,{viewId,lastView,poll,polling,refreshing,refreshAgain,leaves,tops,mode,tick,scanReady,filesMode,flashTarget,findKind,findSort,typesLoaded,searchSeq,searchTimer,findScope,savedListing});
  s.searchValue=$('search').value;s.extValue=$('extFilter').value;s.footer=$('footer').textContent;s.footerTitle=$('footer').title;
  s.statusText=$('status').textContent;s.statusError=$('status').classList.contains('error');s.statusBusy=$('status').classList.contains('busy');
}
function restoreStorageTarget(t){
  const s=storageSessions[t];
  ({viewId,lastView,poll,polling,refreshing,refreshAgain,leaves,tops,mode,tick,scanReady,filesMode,flashTarget,findKind,findSort,typesLoaded,searchSeq,searchTimer,findScope,savedListing}=s);
  $('search').value=s.searchValue;$('extFilter').value=s.extValue;$('footer').textContent=s.footer;$('footer').title=s.footerTitle;
  fillTypes(s.types||[]);$('extFilter').value=s.extValue;
  status(s.statusText,s.statusError,s.statusBusy);$('connText').dataset.who=s.who||(t==='local'?localName():serverWho);
  $('filesBar').classList.toggle('hidden',!filesMode);$('cancelScanButton').classList.toggle('hidden',!poll);$('clearResults').classList.toggle('hidden',!scanReady);
  syncFind();syncPills();syncServerScan();
  if(lastView)render(lastView);
  if(poll){if(!filesMode)placeholder(t==='local'?'Scanning '+localName().toLowerCase()+'…':'Waiting for the completed report from the server…');pollScan()}
  else if(s.needsFinal)pollScan();
  else if(!filesMode){if(t==='remote')serverScanPrompt();else placeholder('No scan yet. Choose a drive or folder and scan this computer.',true)}
  if(filesMode)runSearch();
  if(filesMode&&findScope==='drives')startDrivePoll();else stopDrivePoll();
}
function setTarget(t){
  if(t!=='local'&&t!=='remote')return;
  if(t!==target){stopDrivePoll();saveStorageTarget(target);saveMonitorTarget(target);target=t;restoreStorageTarget(t);switchMonitorTarget(t)}
  try{if(prefs)prefs.set('target',t);else localStorage.setItem('kamkm.target',t)}catch(e){}syncTools();if(t==='local'&&!drives.length)loadDrives()
}
$('targetLocal').onchange=()=>setTarget('local');$('targetRemote').onchange=()=>setTarget('remote');
function driveName(d){return d.path.replace(/\\$/,'')+(d.label?' '+d.label:'')}
async function loadDrives(){
  if(typeof listDrives!=='function')return;
  try{
    const list=await listDrives()||[],was=new Map(drives.map(d=>[d.path,d.checked]));
    // Fixed drives start selected; removable, network and optical drives do not.
    drives=list.map(d=>({...d,checked:was.has(d.path)?was.get(d.path):d.kind==='fixed'}));
    renderDrives();
  }catch(e){$('drives').replaceChildren(el('p','hint',String(e)))}
}
function renderDrives(){
  const box=$('drives');
  if(!drives.length){box.replaceChildren(el('p','hint','No drives found. Enter a folder below instead.'));return}
  box.replaceChildren(...drives.map(d=>{
    const row=el('label','drive'),cb=el('input'),name=el('span','dname'),used=d.total-d.free,kind={removable:'Removable',network:'Network',optical:'Disc',ram:'RAM disk',boot:'Boot'}[d.kind];
    cb.type='checkbox';cb.checked=d.checked;cb.onchange=()=>{d.checked=cb.checked};
    name.append(el('b','',driveName(d)));if(kind)name.append(el('span','kind',kind));
    row.title=`${d.path} · ${d.fileSystem||'unknown file system'} · ${size(d.free)} free of ${size(d.total)} · ${pct(used,d.total)} used`;
    // A drive that is nearly full is the likeliest reason to scan, so it stands out.
    const share=d.total?100*used/d.total:0,level=share>=95?'crit':share>=90?'warn':'';
    row.append(cb,name,el('span','free'+(level?' '+level:''),`${size(d.free)} free`),meter([[share,level]]));
    return row;
  }));
}
$('allDrives').onclick=()=>{drives.forEach(d=>d.checked=true);renderDrives()};
$('noDrives').onclick=()=>{drives.forEach(d=>d.checked=false);renderDrives()};
$('refreshDrives').onclick=loadDrives;
$('connect').onsubmit=async e=>{
  e.preventDefault();
  if(target==='local'){
    const c=connection();
    if(!c.paths.length){status('Choose at least one drive, or enter a folder.',true);return}
    $('submit').disabled=true;await begin(c,localName());return;
  }
  showTrust(false);$('submit').disabled=true;
  const req=connection();
  status('Contacting server and checking its host key…',false,true);conn('busy','Connecting to '+req.host+'…');
  try{
    await startResolve(req);let resolved;
    do{await new Promise(resolve=>setTimeout(resolve,200));resolved=await resolveStatus()}while(resolved.running);
    if(resolved.error)throw resolved.error;
    let c=resolved.connection;await startFingerprint(c);let f;
    do{await new Promise(resolve=>setTimeout(resolve,400));f=await fingerprintStatus()}while(f.running);
    if(f.error)throw f.error;
    pending={...c,fingerprint:f.fingerprint};
    $('target').textContent=c.user+'@'+c.host+':'+c.port;$('fingerprint').textContent=f.fingerprint;
    status('Confirm the server fingerprint to continue.');conn('warn','Verify '+c.host);showTrust(true);
  }catch(e){status(String(e),true);conn('error','Connection failed');$('submit').disabled=false}
};
$('reject').onclick=()=>{pending=null;showTrust(false);$('submit').disabled=false;status('Connection cancelled.');conn('idle','Not connected')};
$('accept').onclick=()=>{const c=pending;pending=null;showTrust(false);begin(c,c.user+'@'+c.host+(c.port&&c.port!=='22'?':'+c.port:''))};
// serverConn is the trusted server last connected to. Connecting to a server only starts the
// monitor; its files are scanned when the user asks, using these details. They stay in memory
// for this session and are never saved.
let serverConn=null,serverWho='';
function syncServerScan(){
  const on=target==='remote'&&!!serverConn&&!storageSessions.remote.poll;
  $('scanServerButton').classList.toggle('hidden',!on);
  if(on)$('scanServerLabel').textContent='Scan files on '+serverWho;
}
// serverScanPrompt fills the Storage view while a server is monitored but not yet scanned.
function serverScanPrompt(){
  if(target!=='remote'||!serverConn||viewId!==null||poll)return;
  lastView=null;leaves=[];tops=new Map();$('workspace').classList.add('idle');
  $('heading').replaceChildren(el('span','here','No scan yet'));$('subheading').textContent='Monitoring '+serverWho+'. Its files are scanned only when you ask.';$('up').classList.add('hidden');
  const box=el('div','empty welcome'),b=el('button','btn primary');
  box.append(el('h2','','Files on '+serverWho+' have not been scanned'),el('p','','Scanning reads every folder under '+(serverConn.path||'/')+' on the server, which can take a while on large disks. Monitoring keeps running meanwhile.'));
  b.type='button';b.id='scanServerInline';b.append(icon('scan'),'Scan files');b.onclick=scanServer;box.append(b);
  $('listing').replaceChildren(box);drawMap(null);
}
// beginScan starts a scan of c and shows its progress in the Storage view.
async function beginScan(c,who,local){
  const t=local?'local':'remote',s=storageSessions[t];
  await startScan(c);$('password').value='';$('passphrase').value='';
  Object.assign(s,{who,viewId:0,lastView:null,mode:'',tick:0,scanReady:false,status:{running:true},needsFinal:false});
  clearInterval(s.poll);s.poll=setInterval(()=>pollScanFor(t),500);
  if(target===t){
    restoreStorageTarget(t);resetFind();scanReady=false;s.scanReady=false;$('cancelScanButton').classList.remove('hidden');$('clearResults').classList.add('hidden');conn('busy','Scanning '+who);
    placeholder(local?'Scanning '+localName().toLowerCase()+'…':'Waiting for the completed report from the server…');$('footer').textContent='Scanning…';
  }
  setTab('storage');syncServerScan();
}
async function scanServer(){
  if(!serverConn||storageSessions.remote.poll)return;
  const c=serverConn,who=serverWho;
  $('submit').disabled=true;$('scanServerButton').disabled=true;status('Connecting and starting the scan…',false,true);
  try{await beginScan(c,who,false)}
  catch(e){$('submit').disabled=false;status(String(e),true);conn('error','Scan failed')}
  finally{$('scanServerButton').disabled=false;syncServerScan()}
}
// begin starts the chosen tools once the computer is chosen and, for a server, trusted.
// This computer is monitored and scanned; a server is only monitored until the user asks for a scan.
async function begin(c,who){
  const local=!!c.local,scanOn=local,monOn=true;
  if(!local){serverConn={...c};serverWho=who}storageSessions[local?'local':'remote'].who=who;syncServerScan();
  status(scanOn?(local?'Starting the scan…':'Connecting and starting the scan…'):(local?'Starting the monitor…':'Connecting and starting the monitor…'),false,true);
  $('connText').dataset.who=who;
  try{
    if(monOn){const t=local?'local':'remote';if(!local||!monitorSessions.local.active){await startMonitor(c);beginMonitor(who,local,!scanOn)}}
    if(!scanOn){
      $('password').value='';$('passphrase').value='';$('submit').disabled=false;status('Monitoring '+who+'. Scan its files when you need them.');conn('busy',(local?'Starting monitor · ':'Connecting monitor · ')+who);
      if(!local)serverScanPrompt();
      syncServerScan();setTab('activity');return;
    }
    await beginScan(c,who,local);
  }catch(e){$('submit').disabled=false;syncServerScan();status(String(e),true);conn('error',local?'Could not start':'Connection failed')}
}
$('clearResults').onclick=async()=>{
  try{await clearResults(target)}catch(e){status(String(e),true);return}
  resetFind();viewId=null;Object.assign(storageSessions[target],{viewId:null,lastView:null,leaves:[],tops:new Map(),status:{cleared:true},scanReady:false,needsFinal:false});placeholder('Results cleared. Their memory was released; scan again to see them.',true);
  $('clearResults').classList.add('hidden');$('footer').textContent='Ready';$('footer').title='';status('Results cleared.');
};
$('scanServerButton').onclick=scanServer;
$('cancelScanButton').onclick=async()=>{try{await cancelScan(target);status('Cancelling scan…',false,true)}catch(e){status(String(e),true)}};
function goUp(){if(lastView&&lastView.trail.length>1)go(lastView.trail[lastView.trail.length-2].id)}
$('up').onclick=goUp;
function setSide(on){$('layout').classList.toggle('collapsed',!on);$('toggleSide').setAttribute('aria-pressed',String(on))}
$('toggleSide').onclick=()=>{const on=$('layout').classList.contains('collapsed');setSide(on);if(prefs)prefs.set('sidebar',on)};
const dialogOpen=id=>!$(id).classList.contains('hidden');
document.onkeydown=e=>{
  const t=e.target,typing=t&&(t.tagName==='INPUT'||t.tagName==='SELECT'||t.tagName==='TEXTAREA');
  if(dialogOpen('trust')){if(e.key==='Escape')$('reject').click();return}
  if(dialogOpen('confirm')){if(e.key==='Escape')$('confirmCancel').click();return}
  if(dialogOpen('appearance')){if(e.key==='Escape')closeAppearance();return}
  if(!$('menu').classList.contains('hidden')){if(e.key==='Escape'){e.preventDefault();hideMenu()}return}
  if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='f'){e.preventDefault();if(!$('find').classList.contains('hidden')){setTab('storage');$('search').focus();$('search').select()}return}
  if(e.key==='Escape'&&filesMode&&tab==='storage'){e.preventDefault();exitFiles();return}
  if(e.key==='Escape'&&Dash.escape()){e.preventDefault();return}
  if((e.ctrlKey||e.metaKey)&&e.key===','){e.preventDefault();openAppearance();return}
  if((e.ctrlKey||e.metaKey)&&e.key.toLowerCase()==='b'){e.preventDefault();$('toggleSide').click();return}
  if((e.ctrlKey||e.metaKey)&&['1','2','3','4'].includes(e.key)){e.preventDefault();setTab({1:'storage',2:'activity',3:'history',4:'events'}[e.key]);return}
  if(typing)return;
  if(tab==='storage'&&!filesMode&&(e.key==='Backspace'||(e.altKey&&e.key==='ArrowUp'))){e.preventDefault();goUp()}
};
function finishScan(t=target){
  const s=storageSessions[t];clearInterval(s.poll);s.poll=null;
  if(t===target){poll=null;syncServerScan();$('cancelScanButton').classList.add('hidden');$('submit').disabled=false;$('progress').classList.add('hidden')}
}
function applyBackgroundScan(t,s){
  const state=storageSessions[t];state.status=s;state.mode=s.mode||(s.progress&&s.progress.mode)||state.mode;
  if(s.running)return;
  clearInterval(state.poll);state.poll=null;state.scanReady=!!(s.files||!s.error);state.needsFinal=true;
}
async function pollScanFor(t){
  if(t===target)return pollScan();
  const state=storageSessions[t];if(state.polling||!state.poll)return;state.polling=true;
  try{applyBackgroundScan(t,await scanStatus(t))}
  catch(e){clearInterval(state.poll);state.poll=null;state.statusText=String(e);state.statusError=true;state.statusBusy=false}
  finally{state.polling=false}
}
// Remote reports publish only on completion. Streaming fallbacks can redraw partial results.
async function pollScan(){
  const t=target,state=storageSessions[t];
  if(state.polling){
    // A scan can finish while its target is being restored. Reapply the final status after
    // the in-flight poll releases the session instead of leaving the final view pending.
    if(state.needsFinal)setTimeout(()=>{if(t===target&&state.needsFinal)pollScan()},0);
    return;
  }
  state.polling=true;polling=true;
  try{
    const s=await scanStatus(t),p=s.progress||{};
    if(t!==target){applyBackgroundScan(t,s);return}
    state.status=s;mode=s.mode||p.mode||mode;state.mode=mode;
    if(s.running){
      const pc=p.expectedBytes?Math.min(99,Math.floor(100*(p.bytes||0)/p.expectedBytes)):p.expected?Math.min(99,Math.floor(100*(p.entries||0)/p.expected)):null;
      $('progress').classList.remove('hidden');$('progress').classList.toggle('indeterminate',pc===null);$('progressFill').style.width=(pc===null?0:pc)+'%';
      // Two short lines; the folder being read is shown on hover.
      status(s.cancelling?'Cancelling…':(p.files||p.directories)?`Scanning${pc!==null?' · '+pc+'%':'…'}\n${count(p.files)} files · ${size(p.bytes)}`:mode==='local'||p.mode==='local'?'Starting…':'Waiting for the server…',false,true);
      $('status').title=p.currentPath||'';
      const d=document.getElementById('scanDetail');if(d)d.textContent=(p.files||p.directories)?`${count(p.files)} files · ${count(p.directories)} folders · ${size(p.bytes)}${pc!==null?' · about '+pc+'%':''}`:'';
      if(!mode.startsWith('remote')&&tick++%3===0&&(p.files||p.directories))refresh();
      return;
    }
    finishScan(t);
    if(s.files||!s.error)await refresh();else placeholder('No results.',true);
    if(t!==target){state.needsFinal=true;return}
    $('clearResults').classList.toggle('hidden',!(s.files||!s.error));scanReady=!!(s.files||!s.error);state.scanReady=scanReady;state.needsFinal=false;syncFind();
    if(!mode.startsWith('remote'))setTimeout(()=>noteOnDisk(t),s.onDisk?0:1000);
    const took=s.seconds?` in ${s.seconds<10?s.seconds.toFixed(1):Math.round(s.seconds)} s`:'',who=state.who||$('connText').dataset.who||'server';
    if(s.cancelled){status(mode.startsWith('remote')?'Remote scan cancelled. No completed report was downloaded.':'Scan cancelled. Showing what was found before cancelling.');conn('warn','Cancelled · '+who)}
    else if(s.error){status(s.error+(s.files?'\nShowing partial results.':''),true);conn('error','Scan failed · '+who)}
    else{status((s.errorCount?`Scan finished${took}. ${count(s.errorCount)} paths could not be read.`:`Scan complete${took}.`)+((s.notes||[]).length?'\n'+s.notes.join('\n'):''));conn(s.errorCount?'warn':'ok',who)}
    if(s.files||!s.error){let foot=`${count(s.files)} files · ${size(s.size)} ${mode==='sftp'?'(file sizes, listed over SFTP)':mode==='local-ntfs'?'(file sizes) · read from the NTFS file table':mode==='local'?'(file sizes) · scanned on '+localName().toLowerCase():mode==='remote-native'?'on disk · aggregated on the server by the fast scanner':mode==='remote'?'on disk · aggregated on the server':'on disk · streamed from the server'}`;if((s.skipped||[]).length)foot+=' · Skipped '+s.skipped.join(', ');if((s.errors||[]).length)foot+=' · '+s.errors.slice(0,2).join('; ');$('footer').textContent=foot;$('footer').title=foot}else $('footer').textContent='Ready';
  }catch(e){if(t===target){finishScan(t);status(String(e),true);conn('error','Disconnected')}}
  finally{state.polling=false;if(t===target)polling=false}
}
// A finished scan moves from memory to a cache file shortly after it ends.
async function noteOnDisk(t=target){
  const state=storageSessions[t];if(state.poll)return;
  try{const s=await scanStatus(t);if(s.running||s.cleared)return;if(!s.onDisk){if(s.files)setTimeout(()=>noteOnDisk(t),2000);return}
  if(t===target){const f=$('footer');if(!f.textContent.includes('kept on disk')){f.textContent+=' · kept on disk to save memory';f.title=f.textContent}state.footer=f.textContent;state.footerTitle=f.title}
  else if(!state.footer.includes('kept on disk')){state.footer+=' · kept on disk to save memory';state.footerTitle=state.footer}}catch(e){}
}
async function loadView(id,t=target){
  await startView(t,id);
  let state;
  do{await new Promise(resolve=>setTimeout(resolve,50));state=await viewStatus(t)}while(state.running);
  if(state.error)throw state.error;
  return state.view;
}
function refresh(){
  const t=target,state=storageSessions[t];if(state.viewId===null)return Promise.resolve();
  if(state.refreshing){state.refreshAgain=true;return state.refreshing}
  state.refreshing=(async()=>{try{do{state.refreshAgain=false;const v=await loadView(state.viewId,t);state.lastView=v;if(t===target){lastView=v;render(v)}}while(state.refreshAgain)}catch(e){if(t===target)status(String(e),true)}finally{state.refreshing=null;if(t===target)refreshing=null}})();
  if(t===target)refreshing=state.refreshing;
  return state.refreshing;
}
function go(id){if(id===undefined||id===null)return;viewId=id;storageSessions[target].viewId=id;refresh()}
function placeholder(text,done=false){
  lastView=null;leaves=[];tops=new Map();$('workspace').classList.add('idle');
  $('heading').replaceChildren(el('span','here',done?'No results':'Scanning…'));$('subheading').textContent='';$('up').classList.add('hidden');
  const box=el('div','empty');if(!done)box.append(el('div','spinner'));box.append(el('div','',text));if(!done){const d=el('div','scan-detail');d.id='scanDetail';box.append(d)}
  $('listing').replaceChildren(box);drawMap(null);
}
function cell(text,cls){const td=document.createElement('td');td.textContent=text;if(cls)td.className=cls;return td}
const SORTS={name:(a,b)=>a.name.localeCompare(b.name,undefined,{numeric:true,sensitivity:'base'}),size:(a,b)=>a.size-b.size,files:(a,b)=>(a.dir?a.files||0:-1)-(b.dir?b.files||0:-1)};
function render(v){
  $('workspace').classList.remove('idle');hideMenu();
  const nav=$('heading');nav.replaceChildren();
  v.trail.forEach((c,i)=>{
    if(i){const sep=el('span','sep');sep.append(icon('chev'));nav.append(sep)}
    const last=i===v.trail.length-1,b=el(last?'span':'button',last?'here':'');
    if(!i)b.append(icon('drive'));b.append(c.name);
    if(!last){b.type='button';b.title='Open '+c.name;b.onclick=()=>go(c.id)}
    nav.append(b);
  });
  const sub=$('subheading');sub.replaceChildren();
  for(const [k,t] of [[size(v.size),'total'],[count(v.items),'items'],[count(v.files),'files']]){const s=el('span');s.append(el('b','',k),' '+t);sub.append(s)}
  if(v.incomplete)sub.append(el('span','badge','Incomplete'));
  $('up').classList.toggle('hidden',v.trail.length<2);
  computeColors(v);
  if(!filesMode)renderList(v);
  drawMap(v);
}
function renderList(v){
  const box=$('listing'),scroll=box.scrollTop,table=el('table'),body=el('tbody'),head=el('thead'),hr=el('tr');
  table.innerHTML='<colgroup><col><col class="c-size"><col class="c-share"><col class="c-files"></colgroup>';
  for(const [key,label] of [['name','Name'],['size','Size'],['size','Share'],['files','Files']]){
    const th=el('th','',label);
    if(sortBy.key===key&&label!=='Share'){th.setAttribute('aria-sort',sortBy.dir<0?'descending':'ascending');th.append(el('span','arrow',sortBy.dir<0?'▼':'▲'))}
    th.onclick=()=>{sortBy=sortBy.key===key?{key,dir:-sortBy.dir}:{key,dir:key==='name'?1:-1};if(lastView)render(lastView)};
    hr.append(th);
  }
  head.append(hr);table.append(head);
  const entries=[...(v.entries||[])].sort((a,b)=>sortBy.dir*SORTS[sortBy.key](a,b)||b.size-a.size);
  for(const n of entries){
    const tr=el('tr'),target={parent:v.id,name:n.name,dir:!!n.dir,id:n.id,size:n.size};
    tr.dataset.click='1';tr.tabIndex=0;tr.dataset.name=n.name;
    if(n.dir){tr.onclick=()=>go(n.id);tr.onkeydown=e=>{if(e.key==='Enter')go(n.id);else rowKeys(e)}}
    else{tr.ondblclick=()=>openEntry(v.id,n.name);tr.onkeydown=e=>{if(e.key==='Enter')openEntry(v.id,n.name);else rowKeys(e)}}
    tr.oncontextmenu=e=>itemMenu(e,target);
    const color=n.dir?null:typeColor.get(ext(n.name))||OTHER_TYPE,share=el('td'),wrap=el('div','share'),bar=el('span','bar'),fill=el('i');
    fill.style.width=Math.min(100,v.size?100*n.size/v.size:0).toFixed(1)+'%';if(color)fill.style.background=color;
    bar.append(fill);wrap.append(pct(n.size,v.size),bar);share.append(wrap);
    const nameTd=el('td'),name=el('div','name');
    if(n.dir)name.append(icon('folder','ic'));else{const sw=el('i','sw');sw.style.background=color;name.append(sw)}
    name.append(el('span','',n.name));if(n.incomplete)name.append(el('span','badge','Incomplete'));
    nameTd.append(name);nameTd.title=n.name;
    tr.append(nameTd,cell(size(n.size)),share,cell(n.dir?count(n.files):'','num'));
    tr.onmouseenter=()=>highlight(tops.get((n.dir?'d:':'f:')+n.name));tr.onmouseleave=()=>highlight(null);
    body.append(tr);
  }
  if(v.moreCount){const tr=el('tr','more');tr.append(cell(`${count(v.moreCount)} more smaller items`),cell(size(v.moreSize)),cell(pct(v.moreSize,v.size)),cell(''));body.append(tr)}
  if(!(v.entries||[]).length){const tr=el('tr','more');tr.append(cell('This folder is empty.'),cell(''),cell(''),cell(''));body.append(tr)}
  table.append(body);box.replaceChildren(table);box.scrollTop=scroll;
  if(flashTarget&&flashTarget.id===v.id){
    const row=[...body.children].find(r=>r.dataset.name===flashTarget.name);flashTarget=null;
    if(row){row.classList.add('flash');row.scrollIntoView({block:'center'});row.focus({preventScroll:true})}
  }
}
// Arrow keys move between rows of the folder list and the search results.
function rowKeys(e){
  if(e.key!=='ArrowDown'&&e.key!=='ArrowUp')return;
  e.preventDefault();let r=e.currentTarget;
  do r=e.key==='ArrowDown'?r.nextElementSibling:r.previousElementSibling;while(r&&r.tabIndex!==0);
  if(r&&r.tabIndex===0)r.focus();else if(e.key==='ArrowUp'&&filesMode)$('search').focus();
}
