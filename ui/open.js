// ---- Opening files ----
const fileManager=()=>platform==='windows'?'Explorer':platform==='darwin'?'Finder':'the file manager';
let toastTimer=null;
function toast(text,{error=false,progress,sub,action,stay=false}={}){
  const t=$('toast');clearTimeout(toastTimer);
  $('toastMsg').textContent=text;t.classList.toggle('error',error);
  $('toastBar').classList.toggle('hidden',progress===undefined);$('toastBar').classList.toggle('indeterminate',progress===null);
  if(typeof progress==='number')$('toastFill').style.width=(100*Math.max(0,Math.min(1,progress))).toFixed(1)+'%';
  $('toastSub').classList.toggle('hidden',!sub);$('toastSub').textContent=sub||'';
  $('toastAct').classList.toggle('hidden',!action);if(action){$('toastAct').textContent=action.label;$('toastAct').onclick=action.run}
  t.classList.remove('hidden');
  if(!stay)toastTimer=setTimeout(hideToast,error?9000:3500);
}
function hideToast(){clearTimeout(toastTimer);$('toast').classList.add('hidden')}
$('toastClose').onclick=hideToast;
async function openRequest(req){
  const seq=await startOpen(req);let s,since=performance.now(),last=0,lastAt=since;
  for(let wait=15;;wait=Math.min(150,wait*2)){
    await sleep(wait);s=await openStatus();if(s.seq!==seq)return null;if(!s.running)return s;
    if(s.downloading){
      const now=performance.now(),rate=now>lastAt+400?(s.done-last)/((now-lastAt)/1000):null;if(rate!==null){last=s.done;lastAt=now}
      toast('Copying '+s.name+' to '+localName().toLowerCase()+'…',{progress:s.total?s.done/s.total:null,stay:true,
        sub:size(s.done)+(s.total?' of '+size(s.total):'')+(rate?' · '+size(rate)+'/s':''),action:{label:'Cancel',run:()=>cancelOpen()}});
    }
  }
}
// openEntry opens a file, or a folder in the file manager, given the folder holding it and its
// name; an empty name means the folder itself. It returns the finished status.
// folder, when given, is the folder on this computer holding name, for results of a search of
// every drive.
async function openEntry(dir,name,action='open',folder){
  if(folder===undefined&&(dir===undefined||dir===null))return null;
  const t=folder!==undefined?'local':target;
  const base=folder!==undefined?{target:t,dir:0,name,action,path:folder}:{target:t,dir,name,action};
  try{
    let s=await openRequest(base);
    if(s&&s.confirm){
      const run=s.confirm==='run';
      const ok=await confirmAction(run?{title:'Run '+s.name+'?',text:'This is a program or script. Opening it runs it with your permissions. Run it only if you trust where it came from.',detail:s.path,detailLabel:'File',label:'Run'}
        :{title:'Copy '+s.name+' to '+localName().toLowerCase()+'?',text:'The file is on the server. It is copied to a temporary folder on '+localName().toLowerCase()+' ('+size(s.size)+') and then opened with its usual program.',detail:s.path,detailLabel:'Server file',label:'Copy and open'});
      if(!ok)return null;
      s=await openRequest(Object.assign({},base,{confirmed:true}));
    }
    if(!s)return null;
    if(s.cancelled){toast('Copy of '+s.name+' cancelled.');return null}
    if(s.error){toast(s.error,{error:true});return null}
    if(action==='open')toast((s.total?'Copied and opened ':'Opened ')+s.name+'.');
    else if(action==='reveal')hideToast();
    return s;
  }catch(e){toast(String(e),{error:true});return null}
}
async function copyPath(dir,name,folder){
  const s=await openEntry(dir,name,'path',folder);if(!s)return;
  try{await navigator.clipboard.writeText(s.path)}catch(e){
    const ta=el('textarea');ta.value=s.path;ta.style.position='fixed';ta.style.opacity='0';document.body.append(ta);ta.select();document.execCommand('copy');ta.remove();
  }
  toast('Copied '+s.path);
}
// Context menu for a file or folder in the list, the search results or the map.
function hideMenu(){const m=$('menu');if(m.classList&&!m.classList.contains('hidden')){m.classList.add('hidden');m.replaceChildren()}}
function itemMenu(e,t){
  e.preventDefault();e.stopPropagation();
  const local=isLocalScan(),items=[];
  if(t.dir){
    items.push(['Open','folder',()=>{exitFiles();go(t.id)},true]);
    if(local)items.push(['Open in '+fileManager(),'up',()=>openEntry(t.id,'')]);
  }else items.push([local?'Open':'Copy to '+localName().toLowerCase()+' and open','scan',()=>openEntry(t.parent,t.name),true]);
  if(t.nav)items.push(['Show in its folder','list',()=>showInFolder(t)]);
  if(local)items.push(['Show in '+fileManager(),'search',()=>openEntry(t.parent,t.name,'reveal')]);
  items.push('-',['Copy path','copy',()=>t.dir?copyPath(t.id,''):copyPath(t.parent,t.name)]);
  showMenu(e,items);
}
// Context menu for a result of a search of every drive, which is opened by its path.
function driveMenu(e,h){
  showMenu(e,[[h.dir?'Open in '+fileManager():'Open',h.dir?'folder':'scan',()=>openEntry(null,h.name,'open',h.path),true],
    ['Show in '+fileManager(),'search',()=>openEntry(null,h.name,'reveal',h.path)],'-',['Copy path','copy',()=>copyPath(null,h.name,h.path)]]);
}
function showMenu(e,items){
  e.preventDefault();e.stopPropagation();
  const m=$('menu');m.replaceChildren();
  for(const it of items){
    if(it==='-'){m.append(el('hr'));continue}
    const [label,ic,run,main]=it,b=el('button',main?'main':'');b.type='button';b.setAttribute('role','menuitem');b.append(icon(ic),label);
    b.onclick=()=>{hideMenu();run()};m.append(b);
  }
  m.classList.remove('hidden');
  const r=m.getBoundingClientRect(),x=Math.min(e.clientX,innerWidth-r.width-6),y=Math.min(e.clientY,innerHeight-r.height-6);
  m.style.left=Math.max(6,x)+'px';m.style.top=Math.max(6,y)+'px';
  m.querySelector('button').focus();
}
$('menu').onkeydown=e=>{
  const bs=[...$('menu').querySelectorAll('button')],i=bs.indexOf(document.activeElement);
  if(e.key==='ArrowDown'||e.key==='ArrowUp'){e.preventDefault();bs[(i+(e.key==='ArrowDown'?1:bs.length-1))%bs.length].focus()}
  else if(e.key==='Tab'){e.preventDefault()}
};
window.addEventListener('mousedown',e=>{if(!$('menu').contains(e.target))hideMenu()},true);
window.addEventListener('blur',hideMenu);window.addEventListener('resize',hideMenu);
if(typeof document.addEventListener==='function')document.addEventListener('scroll',hideMenu,true);

