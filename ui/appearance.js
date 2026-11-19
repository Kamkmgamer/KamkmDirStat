// ---- Appearance ----
let editing=null,fileTheme=null,appearanceReturn=null;
const TH=typeof window.Theme==='object'?window.Theme:null;
function themeCard(theme){
  const ap=TH.appearance,c=TH.resolve(theme,ap),b=el('button','theme-card'),w=el('span','wire');
  b.type='button';b.setAttribute('role','radio');b.setAttribute('aria-checked',String(theme.id===TH.id));b.title=theme.label;
  for(const [k,v] of [['canvas',c.canvas],['chrome',c.chrome],['surface',c.surface],['border',c.border],['text',c.text],['muted',c.textMuted],['accent',c.accent],['chart',c.chart1]])w.style.setProperty('--w-'+k,v);
  w.innerHTML='<i class="w-top"></i><i class="w-side"></i><i class="w-row" style="top:17px"></i><i class="w-row" style="top:24px;width:12%"></i><i class="w-row" style="top:31px"></i><i class="w-btn"></i><i class="w-panel"><i class="w-text"></i><svg viewBox="0 0 100 24" preserveAspectRatio="none"><path d="M0 19 14 14 26 17 40 8 54 12 68 4 82 9 100 6" fill="none" stroke="var(--w-chart)" stroke-width="2" vector-effect="non-scaling-stroke" stroke-linejoin="round"/></svg></i>';
  const name=el('span','theme-name');name.append(el('span','',theme.label));if(TH.isCustom(theme.id))name.append(el('small','','Custom'));
  b.append(w,name);
  b.onclick=()=>{closePanes();TH.setTheme(theme.id);renderThemes()};
  return b;
}
function renderThemes(){
  if(!TH)return;
  $('themeGrid').replaceChildren(...TH.list().map(themeCard));
  for(const r of document.querySelectorAll('input[name=mode]'))r.checked=r.value===TH.mode;
  const custom=TH.isCustom(TH.id);
  $('themeEdit').classList.toggle('hidden',!custom);$('themeDelete').classList.toggle('hidden',!custom);
  $('themeEdit').disabled=$('themeDelete').disabled=$('themeExport').disabled=!!editing;
}
function openAppearance(){
  if(!TH)return;
  appearanceReturn=document.activeElement;closePanes();renderThemes();$('appearance').classList.remove('hidden');
  const sel=$('themeGrid').querySelector('[aria-checked=true]');(sel||$('appearanceClose')).focus();
}
function closeAppearance(){closePanes();$('appearance').classList.add('hidden');if(appearanceReturn&&appearanceReturn.focus)appearanceReturn.focus()}
function closePanes(){
  if(editing&&TH){editing=null;TH.preview(null)}
  editing=null;fileTheme=null;$('themeEditor').classList.add('hidden');$('themeFile').classList.add('hidden');
}
$('openAppearance').onclick=openAppearance;$('appearanceClose').onclick=closeAppearance;
$('appearance').onclick=e=>{if(e.target===$('appearance'))closeAppearance()};
if(TH)for(const r of document.querySelectorAll('input[name=mode]'))r.onchange=()=>{TH.setMode(r.value);if(editing)TH.preview(draftTheme());renderThemes()};
// One accent serves both appearances; it is lightened for dark and darkened for light only as far
// as buttons and links need.
function accentFor(value,ap){
  const c=TH.parseColor(value);if(!c)return value;
  const o=TH.rgbToOklch(c),L=ap==='dark'?Math.max(o.L,0.5):Math.min(o.L,0.6);
  return L===o.L?TH.hex(c):TH.hex(TH.oklchToRgb({...o,L}));
}
function draftTheme(){
  const accent=$('edAccentHex').value.trim();
  return {id:editing&&editing.id||'draft',label:$('edName').value.trim()||'Untitled',light:{canvas:$('edLightHex').value.trim(),accent:accentFor(accent,'light')},dark:{canvas:$('edDarkHex').value.trim(),accent:accentFor(accent,'dark')}};
}
function setColorField(name,value){const c=TH.parseColor(value);$('ed'+name+'Hex').value=c?TH.hex(c):value;if(c)$('ed'+name).value=TH.hex(c)}
function openEditor(base,existing){
  closePanes();
  const ap=TH.appearance,light=TH.resolve(base,'light'),dark=TH.resolve(base,'dark');
  editing={id:existing?base.id:null};
  $('editorTitle').textContent=existing?'Edit theme':'New theme';$('edSave').textContent=existing?'Save changes':'Save theme';
  $('edName').value=existing?base.label:base.id==='graphite'?'My theme':base.label+' copy';
  setColorField('Accent',(ap==='dark'?dark:light).accent);setColorField('Light',light.canvas);setColorField('Dark',dark.canvas);
  $('editorMsg').textContent='';$('themeEditor').classList.remove('hidden');renderThemes();
  TH.preview(draftTheme());$('edName').focus();$('edName').select();
}
for(const name of ['Accent','Light','Dark']){
  $('ed'+name).oninput=()=>{$('ed'+name+'Hex').value=$('ed'+name).value;if(editing)TH.preview(draftTheme())};
  $('ed'+name+'Hex').oninput=()=>{const c=TH.parseColor($('ed'+name+'Hex').value);$('ed'+name+'Hex').setAttribute('aria-invalid',String(!c));if(c){$('ed'+name).value=TH.hex(c);if(editing)TH.preview(draftTheme())}};
}
$('themeNew').onclick=()=>openEditor(TH.find(TH.id)||TH.BUILT_IN[0],false);
$('themeEdit').onclick=()=>{const t=TH.find(TH.id);if(t&&TH.isCustom(t.id))openEditor(t,true)};
$('edCancel').onclick=()=>{closePanes();renderThemes()};
$('themeEditor').onsubmit=e=>{
  e.preventDefault();
  const bad=['Accent','Light','Dark'].find(n=>!TH.parseColor($('ed'+n+'Hex').value));
  if(bad){$('editorMsg').textContent='Enter colours as hex, such as #3b82f6, or another CSS colour.';$('editorMsg').className='msg error';$('ed'+bad+'Hex').focus();return}
  try{const saved=TH.save(draftTheme(),editing&&editing.id||undefined);editing=null;TH.setTheme(saved.id);closePanes();renderThemes()}
  catch(err){$('editorMsg').textContent=err.message;$('editorMsg').className='msg error'}
};
$('themeDelete').onclick=async()=>{
  const t=TH.find(TH.id);if(!t||!TH.isCustom(t.id))return;
  const r=await confirmAction({title:`Delete the theme “${t.label}”?`,text:'It is removed from this computer. Export it first if you want to keep a copy.',label:'Delete theme'});
  if(r){TH.remove(t.id);renderThemes()}
  $('themeDelete').focus();
};
function filePane(exporting){
  closePanes();
  const t=exporting?TH.find(TH.id):null;fileTheme=t;
  $('fileTitle').textContent=t?`Export “${t.label}”`:'Import a theme';
  $('fileHint').textContent=t?'Copy this file or save it, then import it on another computer. T3 Code theme files can be imported the same way.':'Paste a theme file, or open one. Themes exported from KamkmDirStat or T3 Code both work.';
  $('fileText').value=t?TH.toFile(t):'';$('fileText').readOnly=!!t;$('fileMsg').textContent='';$('fileMsg').className='msg';
  for(const id of ['fileBrowse','fileImport'])$(id).classList.toggle('hidden',!!t);
  $('fileCopy').classList.toggle('hidden',!t);$('fileSave').classList.toggle('hidden',!t||typeof window.exportTheme!=='function');
  $('themeFile').classList.remove('hidden');(t?$('fileCopy'):$('fileText')).focus();
}
function fileMessage(text,error){$('fileMsg').textContent=text;$('fileMsg').className='msg'+(error?' error':'')}
$('themeImport').onclick=()=>filePane(false);$('themeExport').onclick=()=>filePane(true);$('fileClose').onclick=()=>{closePanes();renderThemes()};
$('fileBrowse').onclick=()=>$('fileOpen').click();
$('fileOpen').onchange=()=>{const f=$('fileOpen').files[0];if(!f)return;if(f.size>262144){fileMessage('That file is too large to be a theme.',true);return}f.text().then(t=>{$('fileText').value=t;fileMessage(f.name)}).catch(e=>fileMessage(String(e),true));$('fileOpen').value=''};
$('fileImport').onclick=()=>{
  try{const theme=TH.parseFile($('fileText').value),saved=TH.save(theme);TH.setTheme(saved.id);closePanes();renderThemes()}
  catch(err){fileMessage(err.message,true)}
};
$('fileCopy').onclick=async()=>{
  const text=$('fileText').value;
  try{await navigator.clipboard.writeText(text);fileMessage('Copied.')}
  catch(e){$('fileText').readOnly=false;$('fileText').select();const ok=document.execCommand&&document.execCommand('copy');$('fileText').readOnly=true;fileMessage(ok?'Copied.':'Select the text and copy it.',!ok)}
};
$('fileSave').onclick=async()=>{try{const where=await window.exportTheme((fileTheme&&fileTheme.id)||'theme',$('fileText').value);fileMessage('Saved to '+where)}catch(e){fileMessage(String(e),true)}};
if(TH)TH.onChange(()=>{drawSparks();drawResourceHistory();if(!$('appearance').classList.contains('hidden')&&!editing)renderThemes()});

setTarget(target);
if(prefs){if(prefs.get('sidebar')===false)setSide(false);const f=prefs.get('split');if(typeof f==='number')setSplit(f,false)}
// Monitoring starts locally even when the last selected connection was an SSH server.
async function startLocalMonitoring(){
  if(typeof startMonitor!=='function')return;
  storageSessions.local.who=localName();if(target==='local')$('submit').disabled=true;
  try{
    await startMonitor({local:true});
    if(target==='local'){$('connText').dataset.who=localName();conn('busy','Starting monitor: '+localName())}
    beginMonitor(localName(),true,true);
  }catch(e){
    monitorSessions.local.sub='Could not start monitoring.';monitorNotice('local',String(e),'error');
    if(target==='local'){$('actSub').textContent=monitorSessions.local.sub;conn('error','Monitor failed to start')}
  }finally{if(target==='local')$('submit').disabled=false}
}
startLocalMonitoring();
startHistoryUpdates();
if(typeof appPlatform==='function')appPlatform().then(p=>{platform=p;applyPlatform();storageSessions.local.who=localName();monitorSessions.local.who=localName();if(monitorTarget==='local'){$('actHost').textContent=localName();$('connText').dataset.who=localName()}}).catch(()=>{});
