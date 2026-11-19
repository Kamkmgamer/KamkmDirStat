// ---- Search and the Files view ----
// Typing in the search box, or pressing Ctrl+F, lists matching files and folders from the
// whole scan in place of the folder list. With nothing typed it lists the largest files,
// which the type buttons and the extension list narrow down.
// On Windows the search box also searches every drive, without a scan: "All drives" uses an
// index of each drive that the app, or its background indexer, keeps.
let filesMode=false,flashTarget=null,findKind='all',findSort='',typesLoaded=false,searchSeq=0,searchTimer=null,searchRunning=false;
let findScope='',scanReady=false,savedListing=null,drivePoll=null,driveSeen='',driveSearchAt=0,lastSetupError='';
const sleep=ms=>new Promise(r=>setTimeout(r,ms));
const drivesOK=()=>platform==='windows';
function enterFiles(){
  if(filesMode||$('find').classList.contains('hidden'))return;
  if(!scanReady)findScope='drives';
  savedListing=lastView?null:$('listing').firstElementChild||null;
  filesMode=true;hideMenu();$('filesBar').classList.remove('hidden');syncScope();runSearch();
}
function exitFiles(){
  if(!filesMode)return;
  filesMode=false;searchSeq++;$('filesBar').classList.add('hidden');$('search').blur();stopDrivePoll();
  storageSessions[target].searchSeq=searchSeq;
  if(lastView)renderList(lastView);else if(savedListing)$('listing').replaceChildren(savedListing);
}
function resetFind(){
  if(filesMode){filesMode=false;$('filesBar').classList.add('hidden');stopDrivePoll()}
  searchSeq++;typesLoaded=false;scanReady=false;findScope='';$('search').value='';findKind='all';findSort='';syncPills();syncFind();
  storageSessions[target].searchSeq=searchSeq;storageSessions[target].types=[];
  $('extFilter').replaceChildren(anyExtension());$('extFilter').value='';
}
// syncFind shows the search box when there is something to search.
function syncFind(){
  $('find').classList.toggle('hidden',!(scanReady||drivesOK()));
  $('search').placeholder=scanReady?'Search files and folders':'Search every drive';
  $('search').setAttribute('aria-label',scanReady?'Search files and folders in this scan':'Search files and folders on every drive');
  if(filesMode)syncScope();
}
function syncScope(){
  if(!scanReady&&findScope!=='drives')findScope='drives';
  $('scopes').classList.toggle('hidden',!(scanReady&&drivesOK()));
  $('driveBar').classList.toggle('hidden',findScope!=='drives');
  syncPills();
  if(filesMode&&findScope==='drives')startDrivePoll();else stopDrivePoll();
}
function syncPills(){
  if(typeof document.querySelectorAll!=='function')return;
  for(const b of document.querySelectorAll('#kinds button'))b.setAttribute('aria-pressed',String(b.dataset.kind===findKind&&!$('extFilter').value));
  for(const b of document.querySelectorAll('#sorts button'))b.setAttribute('aria-pressed',String(b.dataset.sort===findSort));
  for(const b of document.querySelectorAll('#scopes button'))b.setAttribute('aria-pressed',String(b.dataset.scope===findScope));
}
function scheduleSearch(){const t=target;clearTimeout(searchTimer);searchTimer=setTimeout(()=>{if(target===t)runSearch(t)},45)}
async function runSearch(t=target){
  if(t!==target||!filesMode)return;
  const state=storageSessions[t];
  const text=$('search').value.trim(),ext=$('extFilter').value;
  $('sorts').classList.toggle('hidden',!text);
  const scope=findScope,q={text,kind:findKind,exts:ext?[ext]:[],sort:text?findSort:'size',limit:1000,types:!typesLoaded,scope};
  const mine=++searchSeq;state.searchSeq=mine;const started=performance.now();
  if(scope==='drives')driveSearchAt=started;
  const spin=setTimeout(()=>{if(mine===state.searchSeq&&t===target){$('found').classList.remove('error');$('found').textContent='Searching…'}},120);
  try{
    const seq=await startSearch(t,q);
    let s=null;
    for(let wait=4;;wait=Math.min(40,wait*2)){await sleep(wait);if(mine!==state.searchSeq)return;s=await searchStatus(t);if(s.seq>=seq)break}
    if(s.seq!==seq||mine!==state.searchSeq)return;
    if(s.error)throw s.error;
    const r=s.result;
    if(r.types&&r.types.length){state.types=r.types;state.typesLoaded=true}
    if(t!==target)return;
    typesLoaded=state.typesLoaded;
    if(state.types.length)fillTypes(state.types);
    renderHits(r,text,performance.now()-started,scope);
  }catch(e){if(mine===state.searchSeq&&t===target){$('found').classList.add('error');$('found').textContent=String(e);$('listing').replaceChildren(el('div','empty',String(e)))}}
  finally{clearTimeout(spin)}
}
function anyExtension(){const o=el('option','','Any extension');o.value='';return o}
function fillTypes(types){
  const sel=$('extFilter'),keep=sel.value,opts=[anyExtension()];
  for(const t of types){if(!t.ext)continue;const o=el('option','','.'+t.ext+'  ·  '+size(t.size)+'  ·  '+count(t.files)+(t.files===1?' file':' files'));o.value=t.ext;opts.push(o);if(opts.length>80)break}
  sel.replaceChildren(...opts);sel.value=keep;
}
// markName highlights the matched characters, given as code point positions.
function markName(name,match){
  const box=el('span');if(!match||!match.length){box.textContent=name;return box}
  const chars=Array.from(name),hits=new Set(match);let run='',on=false;
  const flush=()=>{if(!run)return;box.append(on?el('mark','',run):document.createTextNode(run));run=''};
  chars.forEach((c,i)=>{const m=hits.has(i);if(m!==on){flush();on=m}run+=c});flush();
  return box;
}
function hitLeaf(h){return leaves.find(l=>h.dir?l.kind==='dir'&&l.dirId===h.id:l.kind==='file'&&l.dirId===h.parent&&l.name===h.name)}
function renderHits(r,text,ms,scope=''){
  const drives=scope==='drives';
  const found=$('found');found.classList.remove('error');found.replaceChildren();
  const what=findKind==='folders'?'folders':'files';
  if(r.files&&!text)found.append('Largest files · ');
  found.append(el('b','',count(r.total)),' '+(text?(r.total===1?'match':'matches'):what)+' · ',el('b','',size(r.totalSize)));
  if(ms>=1)found.title=`Searched in ${Math.round(ms)} ms`;
  const box=$('listing'),table=el('table'),body=el('tbody'),head=el('thead'),hr=el('tr');
  table.innerHTML='<colgroup><col><col class="c-where"><col class="c-size"><col class="c-act"></colgroup>';
  for(const [label,sort] of [['Name',''],['Folder',null],['Size','size'],['',null]]){
    const th=el('th','',label);
    if(sort!==null&&(text||sort==='size')){if((text?findSort:'size')===sort){th.setAttribute('aria-sort','descending');th.append(el('span','arrow','▼'))}th.onclick=()=>{if(!text)return;findSort=sort;syncPills();runSearch()}}
    else th.style.cursor='default';
    hr.append(th);
  }
  head.append(hr);table.append(head);
  for(const h of r.hits){
    const tr=el('tr'),nameTd=el('td'),name=el('div','name'),where=el('td','where'),act=el('td','act'),show=el('button','icon-btn sm');
    tr.dataset.click='1';tr.tabIndex=0;
    if(h.dir)name.append(icon('folder','ic'));else{const sw=el('i','sw');sw.style.background=typeColor.get(ext(h.name))||OTHER_TYPE;name.append(sw)}
    name.append(markName(h.name,h.match));nameTd.append(name);nameTd.title=h.name;
    const p=el('span');p.dir='ltr';p.textContent=h.path;where.append(p);where.title=h.path;
    const showTitle=drives?'Show in '+fileManager():'Show in its folder';
    show.type='button';show.title=showTitle;show.setAttribute('aria-label',showTitle+': '+h.name);show.append(icon(drives?'search':'up'));
    show.onclick=e=>{e.stopPropagation();if(drives)openEntry(null,h.name,'reveal',h.path);else showInFolder(h)};show.ondblclick=e=>e.stopPropagation();act.append(show);
    // A drive index does not add up folders, so their size is not shown.
    tr.append(nameTd,where,cell(drives&&h.dir?'':size(h.size)),act);
    const open=()=>drives?openEntry(null,h.name,'open',h.path):openHit(h);
    tr.ondblclick=open;
    tr.onkeydown=e=>{if(e.key==='Enter')open();else rowKeys(e)};
    tr.oncontextmenu=e=>drives?driveMenu(e,h):itemMenu(e,{parent:h.parent,name:h.name,dir:!!h.dir,id:h.id,size:h.size,nav:true});
    if(!drives){tr.onmouseenter=()=>{const l=hitLeaf(h);highlight(l||null)};tr.onmouseleave=()=>highlight(null)}
    body.append(tr);
  }
  if(r.total>r.hits.length){const tr=el('tr','more');tr.append(cell(`${count(r.total-r.hits.length)} more not shown. Type more of the name to narrow the list.`),cell(''),cell(''),cell(''));body.append(tr)}
  if(!r.hits.length){const tr=el('tr','more');tr.append(cell(drives&&driveBusy?'Nothing found yet. The drives are still being indexed.':text?'Nothing matches. Check the spelling, or choose All above.':'No files of this type.'),cell(''),cell(''),cell(''));body.append(tr)}
  table.append(body);box.replaceChildren(table);box.scrollTop=0;
}
function openHit(h){if(h.dir){exitFiles();go(h.id)}else openEntry(h.parent,h.name)}
function showInFolder(t){exitFiles();flashTarget={id:t.parent,name:t.name};if(viewId===t.parent&&lastView){renderList(lastView);return}go(t.parent)}
$('search').onfocus=()=>enterFiles();
$('search').oninput=()=>{enterFiles();scheduleSearch()};
$('search').onkeydown=e=>{
  if(e.key==='Escape'){e.preventDefault();e.stopPropagation();if($('search').value){$('search').value='';scheduleSearch()}else exitFiles();return}
  if(e.key==='ArrowDown'||e.key==='Enter'){const r=$('listing').querySelector('tbody tr[tabindex="0"]');if(r){e.preventDefault();r.focus();if(e.key==='Enter'&&$('search').value.trim())r.dispatchEvent(new KeyboardEvent('keydown',{key:'Enter'}))}}
};
$('kinds').onclick=e=>{const b=e.target.closest&&e.target.closest('button[data-kind]');if(!b)return;findKind=b.dataset.kind;$('extFilter').value='';syncPills();runSearch()};
$('sorts').onclick=e=>{const b=e.target.closest&&e.target.closest('button[data-sort]');if(!b)return;findSort=b.dataset.sort;syncPills();runSearch()};
$('extFilter').onchange=()=>{if($('extFilter').value)findKind='all';syncPills();runSearch()};
$('closeFiles').onclick=()=>exitFiles();
$('scopes').onclick=e=>{
  const b=e.target.closest&&e.target.closest('button[data-scope]');if(!b||b.dataset.scope===findScope)return;
  findScope=b.dataset.scope;typesLoaded=false;$('extFilter').replaceChildren(anyExtension());$('extFilter').value='';syncScope();runSearch();
};
let driveBusy=false;
function startDrivePoll(){if(drivePoll)return;driveSeen='';pollDrives();drivePoll=setInterval(pollDrives,1500)}
function stopDrivePoll(){if(drivePoll)clearInterval(drivePoll);drivePoll=null}
async function pollDrives(){
  let st;try{st=await driveIndexStatus()}catch(e){return}
  if(!filesMode||findScope!=='drives')return;
  renderDriveBar(st);
  // Search again when a drive's index becomes ready, and every few seconds while indexes are
  // still being made, unless the list has been scrolled.
  const sig=st.source+'|'+st.drives.map(d=>d.root+':'+d.state).join(',');
  driveBusy=st.drives.some(d=>d.state==='building'||d.state==='loading');
  if(driveSeen&&sig!==driveSeen||driveBusy&&performance.now()-driveSearchAt>3000&&$('listing').scrollTop<8)runSearch();
  driveSeen=sig;
}
function ago(ms){
  const s=Math.max(0,(Date.now()-ms)/1000);
  return s<90?'just now':s<5400?Math.round(s/60)+' min ago':s<172800?Math.round(s/3600)+' h ago':Math.round(s/86400)+' days ago';
}
function renderDriveBar(st){
  const chips=$('driveChips'),list=[];
  for(const d of st.drives){
    const c=el('span','chip'),dot=el('i','dot');let note;
    if(d.state==='building'){dot.classList.add('busy');note=d.read?'indexing · '+count(d.read):'indexing…'}
    else if(d.state==='loading'){dot.classList.add('busy');note='loading…'}
    else if(d.state==='error'){dot.classList.add('error');note='not indexed'}
    else if(d.mode==='live'){dot.classList.add('live');note='live'}
    else note=d.built?'read '+ago(d.built):'ready';
    c.append(dot,el('b','',d.root.replace(/\\$/,'')),note);
    c.title=(d.label?d.label+' ('+d.root+')':d.root)+' · '+count(d.entries)+' files and folders'+(d.mode==='live'?' · kept current from the drive\'s change journal':d.built?' · folders read '+new Date(d.built).toLocaleString():'')+(d.error?'\n'+d.error:'');
    list.push(c);
  }
  if(!list.length)list.push(el('span','','Looking for drives…'));
  chips.replaceChildren(...list);
  let hint='';
  if(st.setup)hint=st.setup==='install'?'Setting up the background indexer. Approve the Windows prompt to continue.':'Removing the background indexer. Approve the Windows prompt to continue.';
  else if(st.source==='service')hint=st.outdated?'The background indexer is from another version of this app.':'Kept current by the background indexer.';
  else if(st.service==='unreachable')hint='The background indexer is not answering'+(st.note?': '+st.note:'.');
  else if(!st.admin&&st.drives.some(d=>d.mode!=='live'))hint='Snapshots can miss recent changes. Live indexes need administrator rights or the background indexer.';
  else if(st.admin&&st.drives.some(d=>d.mode==='live'))hint='Kept current while this window is open.';
  $('driveHint').textContent=hint;
  $('driveRefresh').classList.toggle('hidden',!st.drives.some(d=>d.mode==='snapshot'&&d.state==='ready'));
  $('driveAdmin').classList.toggle('hidden',!!(st.admin||st.source==='service'||st.setup));
  const svc=$('driveService');
  svc.textContent=!st.service?'Set up background indexer':st.outdated?'Update background indexer':'Restart background indexer';
  svc.classList.toggle('hidden',!!st.setup||!!st.service&&!st.outdated&&st.service==='running'&&st.source==='service');
  $('driveRemove').classList.toggle('hidden',!st.service||!!st.setup);
  if(st.setupError&&st.setupError!==lastSetupError)toast(st.setupError,{error:true});
  lastSetupError=st.setupError||'';
}
$('driveRefresh').onclick=async()=>{try{await refreshDriveIndex('');toast('Reading the drives\' folders again…');pollDrives()}catch(e){toast(String(e),{error:true})}};
$('driveAdmin').onclick=async()=>{
  const ok=await confirmAction({title:'Restart as administrator?',text:'The app starts again with administrator rights, so it can read each drive\'s file table directly and keep the index current from the drive\'s change journal.'+(scanReady?' The scan results in this window are closed.':''),label:'Restart'});
  if(!ok)return;
  try{await restartElevated()}catch(e){toast(String(e),{error:true})}
};
$('driveService').onclick=async()=>{
  const fresh=!$('driveService').textContent.startsWith('Restart')&&!$('driveService').textContent.startsWith('Update');
  if(fresh){
    const ok=await confirmAction({title:'Set up the background indexer?',text:'A Windows service keeps an index of the name of every file on this PC\'s drives, so searching every drive is instant and current without administrator rights. It is copied to Program Files, runs as the system account and starts with Windows. Only you and administrators can search through it, and its results include the names of files in every account on this PC. Windows asks for administrator approval.',label:'Set up'});
    if(!ok)return;
  }
  try{await setupIndexService(true);pollDrives()}catch(e){toast(String(e),{error:true})}
};
$('driveRemove').onclick=async()=>{
  const ok=await confirmAction({title:'Remove the background indexer?',text:'The service, its copy in Program Files and the indexes it saved are deleted. Windows asks for administrator approval.',label:'Remove'});
  if(!ok)return;
  try{await setupIndexService(false);pollDrives()}catch(e){toast(String(e),{error:true})}
};

