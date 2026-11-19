// Deleting from a scan of this computer, to the Recycle Bin (Trash) or for good.
const binName=()=>platform==='windows'?'Recycle Bin':'Trash';
const errorText=e=>String(e&&e.message||e);
let deleting=false;
function deleteKey(e,t){
  if(e.key!=='Delete'||target!=='local'||!scanReady||!isLocalScan()||!t.name)return false;
  e.preventDefault();deleteEntry(t,e.shiftKey);return true;
}
async function deleteEntry(t,permanent){
  if(deleting){toast('Still deleting the previous item.');return}
  const what=t.dir?'folder':'file',bin=binName(),amount=t.size?' ('+size(t.size)+')':'';
  const ok=await confirmAction(permanent
    ?{title:'Delete '+t.name+' for good?',text:'This '+what+amount+' is deleted permanently. It does not go to the '+bin+' and cannot be restored.'+(t.dir?' Everything inside it is deleted too.':''),detail:t.name,detailLabel:t.dir?'Folder':'File',label:'Delete permanently'}
    :{title:'Move '+t.name+' to the '+bin+'?',text:'This '+what+amount+' can be restored from the '+bin+'. It still takes space until the '+bin+' is emptied.',detail:t.name,detailLabel:t.dir?'Folder':'File',label:'Move to '+bin});
  if(!ok)return;
  deleting=true;
  try{
    const seq=await startDelete({target:'local',dir:t.parent,name:t.name,permanent});
    toast((permanent?'Deleting ':'Moving ')+t.name+'…',{stay:true,progress:null});
    let s;
    for(;;){s=await deleteStatus();if(s.seq===seq&&!s.running)break;await new Promise(r=>setTimeout(r,150))}
    if(s.error)toast(s.error,{error:true});
    else toast(permanent?'Deleted '+s.name+'. '+size(s.freed)+' freed.':'Moved '+s.name+' to the '+bin+'.');
    scanChanged();
  }catch(e){toast(errorText(e),{error:true})}
  finally{deleting=false}
}
// scanChanged shows the scan of this computer again after files were removed from it.
function scanChanged(){if(target!=='local'||!lastView)return;if(filesMode)runSearch();refresh()}
function cleanOn(){return typeof Clean==='object'&&!!Clean&&Clean.on()}
function cleanExit(restore){if(typeof Clean==='object'&&Clean)Clean.exit(restore)}

// Clean up: what the scan of this computer holds that programs download or build again,
// listed in place of the folder list. Only what the user chooses and confirms is removed.
var Clean=(()=>{
  const GROUPS={projects:'Projects',worktrees:'Agent worktrees',packages:'Package caches',build:'Build and editor caches',devices:'Xcode and Android',browsers:'Browsers',models:'AI models',system:'System'};
  let on=false,st=null,poll=null,wasBusy=false,listKey='';
  const chosen=new Set(),opened=new Set();
  const busy=()=>!!st&&(st.finding||st.cleaning);
  async function enter(){
    if(target!=='local'||!scanReady||busy())return;
    exitFiles();hideMenu();on=true;st=null;listKey='';$('cleanBar').classList.remove('hidden');render();
    try{await startCleanupFind();wasBusy=true}catch(e){toast(errorText(e),{error:true});exit();return}
    status();
  }
  function exit(restore=true){
    if(!on)return;
    on=false;listKey='';$('cleanBar').classList.add('hidden');
    if(restore&&lastView)renderList(lastView);
  }
  async function status(){
    clearTimeout(poll);poll=null;
    try{st=await cleanupStatus()}catch(e){return}
    const b=busy();
    if(st.cleaning)toast('Clearing… '+size(st.done)+' of '+size(st.total),{stay:true,progress:st.total?st.done/st.total:0,sub:st.current||''});
    else if(wasBusy){
      if(st.error)toast(st.error,{error:true});
      else if(st.cleared){
        const failed=(st.items||[]).filter(i=>i.failed&&i.failed.length).length;
        toast('Freed '+size(st.freed)+'.'+(failed?' '+(failed===1?'One item was':count(failed)+' items were')+' not cleared completely; the list says why.':''),{error:!!failed});
        scanChanged();
      }
    }
    wasBusy=b;
    const ids=new Set((st.items||[]).map(i=>i.id));for(const id of [...chosen])if(!ids.has(id))chosen.delete(id);
    if(on)render();
    if(b)poll=setTimeout(status,300);
  }
  function emptyBox(spin,title,text){
    const e=el('div','empty');if(spin)e.append(el('div','spinner'));
    if(title)e.append(el('h2','',title));e.append(el('p','',text));return e;
  }
  function render(){
    renderBar();
    const box=$('listing'),items=st&&st.items||[],key=JSON.stringify([st&&st.seq,st&&st.finding,items,[...chosen],[...opened],busy()]);
    if(key===listKey)return;
    listKey=key;
    const list=el('div','clean-list');
    if(!st||st.finding)list.append(emptyBox(true,'','Looking for caches and build output…'));
    else if(!items.length)list.append(emptyBox(false,'Nothing to clear','No caches or build output in this scan.'+(st.homeIn?'':' Scan your user folder to check its caches too.')));
    else{
      if(!st.homeIn){const n=el('p','hint','Your user folder was not scanned, so its caches are not listed.');n.style.padding='8px 14px';list.append(n)}
      let group=null;
      for(const it of items){
        if(it.group!==group){
          group=it.group;const h=el('div','clean-group'),total=items.filter(x=>x.group===group).reduce((a,x)=>a+x.size,0);
          h.append(el('span','',GROUPS[group]||group),el('b','',size(total)));list.append(h);
        }
        list.append(row(it));
      }
    }
    const scroll=box.scrollTop;box.replaceChildren(list);box.scrollTop=scroll;
  }
  function row(it){
    const on=chosen.has(it.id),b=busy(),r=el('div','clean-row'+(on?' on':''));r.dataset.id=it.id;
    const cb=el('input');cb.type='checkbox';cb.checked=on;cb.disabled=b;cb.setAttribute('aria-label','Choose '+it.name);
    cb.onclick=e=>{e.stopPropagation();toggle(it.id,cb.checked)};
    const nm=el('div','nm');nm.append(el('span','',it.name));nm.title=it.name;
    const expanded=opened.has(it.id),more=el('button','icon-btn sm more');more.type='button';more.setAttribute('aria-expanded',String(expanded));
    more.title=expanded?'Hide where it is':'Show where it is';more.setAttribute('aria-label',more.title+': '+it.name);more.append(icon('chev'));
    more.onclick=e=>{e.stopPropagation();if(expanded)opened.delete(it.id);else opened.add(it.id);render()};
    r.append(cb,nm,el('span','sz',size(it.size)),more,el('div','dt',it.detail));
    if(it.note)r.append(el('div','note',it.note));
    if(it.kept)r.append(el('div','kept',it.kept));
    if(it.freed)r.append(el('div','freed','Freed '+size(it.freed)+(it.size?'; '+size(it.size)+' is left.':'.')));
    for(const f of it.failed||[])r.append(el('div','err',f));
    if(expanded){
      const list=el('div','clean-paths');list.onclick=e=>e.stopPropagation();
      (it.paths||[]).forEach((p,i)=>{
        const d=el('div'),path=el('span','p','‎'+p.path);path.title=p.path;d.append(path,el('span','s',size(p.size)));
        if(/^([A-Za-z]:[\\/]|\/)/.test(p.path)){const s=el('button','linkbtn','Show');s.type='button';s.title='Show in '+fileManager();s.onclick=async()=>{try{await revealCleanupPath(it.id,i)}catch(e){toast(errorText(e),{error:true})}};d.append(s)}
        list.append(d);
      });
      if(it.more)list.append(el('div','','and '+count(it.more)+' smaller'));
      r.append(list);
    }
    r.onclick=()=>{if(!busy())toggle(it.id,!chosen.has(it.id))};
    return r;
  }
  function toggle(id,yes){if(yes)chosen.add(id);else chosen.delete(id);render()}
  function renderBar(){
    const items=(st&&st.items||[]).filter(i=>chosen.has(i.id)),total=items.reduce((a,i)=>a+i.size,0),all=(st&&st.items||[]).reduce((a,i)=>a+i.size,0),b=busy(),c=$('cleanChosen');
    if(items.length)c.replaceChildren(el('b','',size(total)),' chosen of '+size(all));
    else c.textContent=st&&st.found&&all?size(all)+' can be cleared':'';
    $('cleanGo').disabled=!items.length||b;$('cleanGoLabel').textContent=items.length?'Clear '+size(total):'Clear';
    $('cleanStop').classList.toggle('hidden',!b);$('cleanStop').disabled=!!(st&&st.cancelling);
    $('cleanAll').disabled=b;$('cleanNone').disabled=b;
  }
  async function clear(){
    const items=(st&&st.items||[]).filter(i=>chosen.has(i.id));if(!items.length||busy())return;
    const total=items.reduce((a,i)=>a+i.size,0),notes=items.filter(i=>i.note).map(i=>i.name+': '+i.note);
    const ok=await confirmAction({title:'Clear '+size(total)+'?',label:'Clear',detailLabel:items.length===1?'Item':items.length+' items',
      detail:items.map(i=>i.name+' · '+size(i.size)).join('\n'),
      text:'These are deleted for good and do not go to the '+binName()+'. Programs download or build them again when they need them.'+(notes.length?' '+notes.join(' '):'')});
    if(!ok)return;
    try{await startCleanup(items.map(i=>i.id));wasBusy=true;status()}catch(e){toast(errorText(e),{error:true})}
  }
  $('cleanButton').onclick=enter;
  $('closeClean').onclick=()=>exit();
  $('cleanStop').onclick=async()=>{try{await cancelCleanup()}catch(e){}status()};
  $('cleanAll').onclick=()=>{if(!st||busy())return;chosen.clear();for(const i of st.items||[])if(!i.note)chosen.add(i.id);render()};
  $('cleanNone').onclick=()=>{if(busy())return;chosen.clear();render()};
  $('cleanGo').onclick=clear;
  return {on:()=>on,enter,exit,status};
})();
