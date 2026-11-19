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

