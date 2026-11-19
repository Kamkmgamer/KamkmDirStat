// Cushion treemap in the style of WinDirStat (van Wijk and van de Wetering): every level adds a
// parabolic ridge, so nesting shows as shading. Files are coloured by extension, from a palette of
// equal lightness so no type stands out only because of its hue.
const PAL=['#539af2','#e6715b','#53be70','#eab532','#00c1d1','#a878db','#f0903c','#6d7cdf','#a2c854','#db6ea5'],OTHER_TYPE='#8f9297',SMALL='#6e7278',FOLDER='#77818c';
function ext(name){const i=name.lastIndexOf('.');return i>0&&name.length-i<=12?name.slice(i+1).toLowerCase():''}
function rgb(hex){return [1,3,5].map(i=>parseInt(hex.slice(i,i+2),16))}
function css(c){return 'rgb('+c.map(x=>Math.round(Math.min(255,x))).join(',')+')'}
let ranked=[],totalTypes=0;
function computeColors(v){
  const totals=new Map(),m=v&&v.map;
  if(m)(function walk(n){if(n.c)n.c.forEach(walk);if(!n.d)totals.set(ext(n.n),(totals.get(ext(n.n))||0)+n.s)})(m);
  ranked=[...totals].sort((a,b)=>b[1]-a[1]).slice(0,PAL.length);totalTypes=totals.size;
  typeColor=new Map(ranked.map(([e],i)=>[e,PAL[i]]));
}
function squarify(items,x0,y0,x1,y1,cb){
  let rest=items.reduce((a,k)=>a+k.s,0),i=0;
  while(i<items.length&&rest>0){
    const w=x1-x0,h=y1-y0;if(w<=0||h<=0)return;
    const scale=w*h/rest,side=Math.min(w,h),side2=side*side;
    let j=i,sum=0,big=0,small=Infinity,worst=Infinity;
    while(j<items.length){const a=items[j].s*scale,ns=sum+a,nb=Math.max(big,a),nm=Math.min(small,a),r=Math.max(side2*nb/(ns*ns),ns*ns/(side2*nm));if(j>i&&r>worst)break;worst=r;sum=ns;big=nb;small=nm;j++}
    const thick=sum/side;let pos=0;
    for(let k=i;k<j;k++){const len=k===j-1?side-pos:items[k].s*scale/thick;if(w>=h)cb(items[k],x0,y0+pos,x0+thick,y0+pos+len);else cb(items[k],x0+pos,y0,x0+pos+len,y0+thick);pos+=len;rest-=items[k].s}
    if(w>=h)x0+=thick;else y0+=thick;i=j;
  }
}
// Folder names sit in a band across the top of each folder big enough to hold one, with its
// contents below, and large files carry their name, in the style of WizTree.
const HEADER=14,LABEL_FONT=11;
let mapLabels=true;try{if(prefs&&prefs.get('mapLabels')===false)mapLabels=false}catch(e){}
function syncLabelToggle(){$('mapLabels').setAttribute('aria-pressed',String(mapLabels))}
syncLabelToggle();
$('mapLabels').onclick=()=>{mapLabels=!mapLabels;syncLabelToggle();if(prefs)prefs.set('mapLabels',mapLabels);redraw()};
const isLocalScan=()=>mode.startsWith('local');
const pathSep=()=>isLocalScan()&&platform==='windows'?'\\':'/';
let colorProbe=null;
function cssRgb(value){
  if(!colorProbe)colorProbe=document.createElement('canvas').getContext('2d');
  colorProbe.fillStyle='#000';colorProbe.fillStyle=value.trim()||'#000';
  const c=colorProbe.fillStyle;if(c[0]==='#')return rgb(c);
  const m=c.match(/[\d.]+/g);return m?m.slice(0,3).map(Number):[0,0,0];
}
function fitLabel(ctx,name,suffix,maxW){
  if(ctx.measureText(name+suffix).width<=maxW)return name+suffix;
  const best=(tail)=>{const fits=k=>ctx.measureText(name.slice(0,k)+'…'+tail).width<=maxW;if(!fits(2))return '';let lo=2,hi=name.length;while(lo<hi){const mid=(lo+hi+1)>>1;if(fits(mid))lo=mid;else hi=mid-1}return name.slice(0,lo)+'…'+tail};
  return (suffix&&best(suffix))||best('');
}
function drawMap(v){
  const canvas=$('map'),box=$('treemap'),m=v&&v.map;
  leaves=[];tops=new Map();$('hoverBox').classList.add('hidden');$('tip').classList.add('hidden');
  const W=box.clientWidth,H=box.clientHeight,empty=!m||!(m.s>0)||W<4||H<4;
  $('mapEmpty').classList.toggle('hidden',!empty);
  if(empty){canvas.width=1;canvas.height=1;$('legend').replaceChildren();$('mapEmpty').textContent=v?'Nothing to draw in this folder.':'The map will show how space is distributed.';return}
  const dpr=Math.min(2,window.devicePixelRatio||1),cw=Math.round(W*dpr),ch=Math.round(H*dpr);
  canvas.width=cw;canvas.height=ch;canvas.style.width=W+'px';canvas.style.height=H+'px';
  const colors=new Map(ranked.map(([e],i)=>[e,rgb(PAL[i])])),grey=rgb(OTHER_TYPE),small=rgb(SMALL),folder=rgb(FOLDER);
  const ctx=canvas.getContext('2d'),img=ctx.createImageData(cw,ch),data=img.data;
  const ld=Math.sqrt(102),Lx=-1/ld,Ly=-1/ld,Lz=10/ld,Ia=0.2,Is=1-Ia,B=1.22;
  const style=getComputedStyle(document.documentElement),back=cssRgb(style.getPropertyValue('--map-background')),dark=(back[0]*.299+back[1]*.587+back[2]*.114)<128;
  const bands=[0,1,2,3,4].map(d=>back.map(c=>c+((dark?255:0)-c)*(dark?.15+.035*d:.09+.03*d))),bandText=dark?'#e8eaed':'#1d1f22',labels=[],sep=pathSep();
  const hdr=HEADER*dpr,inset=Math.max(1,Math.round(dpr));
  function fill(x0,y0,x1,y1,col){
    const X0=Math.max(0,Math.round(x0)),X1=Math.min(cw,Math.round(x1)),Y0=Math.max(0,Math.round(y0)),Y1=Math.min(ch,Math.round(y1));
    for(let y=Y0;y<Y1;y++)for(let x=X0,o=(y*cw+X0)*4;x<X1;x++,o+=4){data[o]=col[0];data[o+1]=col[1];data[o+2]=col[2];data[o+3]=255}
  }
  function paint(x0,y0,x1,y1,s,col){
    const X0=Math.max(0,Math.round(x0)),X1=Math.min(cw,Math.round(x1)),Y0=Math.max(0,Math.round(y0)),Y1=Math.min(ch,Math.round(y1)),r=col[0],g=col[1],b=col[2];
    for(let y=Y0;y<Y1;y++){const ny=-(2*s[1]*(y+.5)+s[3]);let o=(y*cw+X0)*4;for(let x=X0;x<X1;x++,o+=4){const nx=-(2*s[0]*(x+.5)+s[2]);let c=(nx*Lx+ny*Ly+Lz)/Math.sqrt(nx*nx+ny*ny+1);if(c>1)c=1;let p=Is*c;if(p<0)p=0;p=(p+Ia)*B;data[o]=r*p;data[o+1]=g*p;data[o+2]=b*p;data[o+3]=255}}
  }
  function ridge(s,x0,y0,x1,y1,h){const w=x1-x0,hh=y1-y0;if(w>0){const f=4*h/w;s[2]+=f*(x1+x0);s[0]-=f}if(hh>0){const f=4*h/hh;s[3]+=f*(y1+y0);s[1]-=f}}
  function place(n,x0,y0,x1,y1,parent,h,trail,dirId,root,depth){
    const s=parent.slice();if(!root)ridge(s,x0,y0,x1,y1,h);
    const kids=(n.c||[]).filter(k=>k.s>0);if(n.o>0)kids.push({n:'',s:n.o,other:true});kids.sort((a,b)=>b.s-a.s);
    const w=(x1-x0)/dpr,hh=(y1-y0)/dpr,where=trail.join('/');
    if(n.other||!n.d||!kids.length||x1-x0<3||y1-y0<3){
      const kind=n.other?'other':n.d?'dir':'file';
      paint(x0,y0,x1,y1,s,kind==='file'?colors.get(ext(n.n))||grey:kind==='dir'?folder:small);
      leaves.push({x0:x0/dpr,y0:y0/dpr,x1:x1/dpr,y1:y1/dpr,size:n.s,dirId:kind==='dir'?n.i:dirId,parent:dirId,name:kind==='other'?'Smaller items':n.n,kind,label:kind==='other'?(where?where+'/':'')+'(smaller items)':kind==='dir'?where+'/':where});
      if(mapLabels&&kind!=='other'&&w>=44&&hh>=16)labels.push({x:x0+3*dpr,y:y0+8*dpr,w:x1-x0-6*dpr,name:n.n+(kind==='dir'?sep:''),suffix:' ('+size(n.s)+')',file:true});
      return;
    }
    const here=root?v.id:n.i;
    let cx0=x0,cy0=y0,cx1=x1,cy1=y1;
    if(mapLabels&&!root&&w>=30&&hh>=HEADER+12){
      const band=bands[Math.min(depth,bands.length-1)];
      cx0=x0+inset;cx1=x1-inset;cy0=y0+hdr;cy1=y1-inset;
      fill(x0,y0,x1,cy0,band);fill(x0,cy0,cx0,y1,band);fill(cx1,cy0,x1,y1,band);fill(cx0,cy1,cx1,y1,band);
      leaves.push({x0:x0/dpr,y0:y0/dpr,x1:x1/dpr,y1:cy0/dpr,size:n.s,dirId:n.i,parent:dirId,name:n.n,kind:'dir',header:true,label:where+'/'});
      labels.push({x:x0+3*dpr,y:y0+hdr/2,w:x1-x0-6*dpr,name:n.n+sep,suffix:' ('+size(n.s)+')'});
    }
    squarify(kids,cx0,cy0,cx1,cy1,(k,a,b,c,d)=>{
      if(root&&!k.other)tops.set((k.d?'d:':'f:')+k.n,{x0:a/dpr,y0:b/dpr,x1:c/dpr,y1:d/dpr});
      place(k,a,b,c,d,s,h*0.91,k.other?trail:trail.concat(k.n),here,false,depth+1);
    });
  }
  place(m,0,0,cw,ch,[0,0,0,0],0.38,[],v.id,true,0);
  ctx.putImageData(img,0,0);
  if(labels.length){
    ctx.font=(LABEL_FONT*dpr)+'px '+(getComputedStyle(document.body).fontFamily||'sans-serif');ctx.textBaseline='middle';
    for(const l of labels){
      const t=fitLabel(ctx,l.name,l.suffix,l.w);if(!t)continue;
      if(l.file){ctx.fillStyle='rgba(0,0,0,.62)';ctx.fillText(t,l.x+dpr,l.y+dpr);ctx.fillStyle='#fff'}else ctx.fillStyle=bandText;
      ctx.fillText(t,l.x,l.y);
    }
  }
  const chips=ranked.map(([e],i)=>[e?'.'+e:'no extension',PAL[i]]);
  if(totalTypes>ranked.length)chips.push(['other',OTHER_TYPE]);
  chips.push(['small items',SMALL]);
  $('legend').replaceChildren(...chips.map(([label,color])=>{const span=el('span'),sw=el('i');sw.style.background=color;span.append(sw,label);return span}));
}
function highlight(r){const hb=$('hoverBox');if(!r){hb.classList.add('hidden');return}Object.assign(hb.style,{left:r.x0+'px',top:r.y0+'px',width:Math.max(2,r.x1-r.x0)+'px',height:Math.max(2,r.y1-r.y0)+'px'});hb.classList.remove('hidden')}
function redraw(){if(lastView)drawMap(lastView)}
function hit(x,y){for(let i=leaves.length-1;i>=0;i--){const l=leaves[i];if(x>=l.x0&&x<l.x1&&y>=l.y0&&y<l.y1)return l}return null}
function mapPoint(e){const r=$('map').getBoundingClientRect();return [e.clientX-r.left,e.clientY-r.top]}
$('map').onmousemove=e=>{
  const [x,y]=mapPoint(e),l=hit(x,y),tip=$('tip');
  if(!l){highlight(null);tip.classList.add('hidden');return}
  highlight(l);
  const total=lastView&&lastView.size;
  tip.replaceChildren(el('b','',l.kind==='dir'?l.name+'/':l.name),el('span','path',l.label||'/'),el('span','meta',size(l.size)+(total?' · '+pct(l.size,total)+' of this folder':'')));
  tip.classList.remove('hidden');
  const box=$('treemap'),tw=tip.offsetWidth,th=tip.offsetHeight;
  tip.style.left=(x+16+tw>box.clientWidth?Math.max(4,x-tw-12):x+16)+'px';tip.style.top=(y+16+th>box.clientHeight?Math.max(4,y-th-12):y+16)+'px';
};
$('map').onmouseleave=()=>{highlight(null);$('tip').classList.add('hidden')};
// A click on a file opens its folder only after the time for a double-click has passed, so
// that a double-click opens the file instead.
let mapClickTimer=null;
$('map').onclick=e=>{
  if(e.detail>1)return;
  const t=target,l=hit(...mapPoint(e));clearTimeout(mapClickTimer);
  if(!l||l.dirId===undefined||l.dirId===null||l.dirId===viewId)return;
  if(l.kind==='file')mapClickTimer=setTimeout(()=>{if(target===t)go(l.dirId)},260);else go(l.dirId);
};
$('map').ondblclick=e=>{const l=hit(...mapPoint(e));if(l&&l.kind==='file'){clearTimeout(mapClickTimer);openEntry(l.dirId,l.name)}};
$('map').oncontextmenu=e=>{
  const l=hit(...mapPoint(e));if(!l||l.kind==='other'){e.preventDefault();return}
  itemMenu(e,l.kind==='dir'?{parent:l.parent,name:l.name,dir:true,id:l.dirId,size:l.size,nav:l.parent!==viewId}:{parent:l.dirId,name:l.name,dir:false,size:l.size,nav:l.dirId!==viewId});
};
// Drag (or arrow keys on) the splitter to share height between the list and the map.
function setSplit(f,save=true){f=Math.max(.15,Math.min(.8,f));$('workspace').style.setProperty('--list',(f*100).toFixed(1)+'%');clearTimeout(resizeTimer);resizeTimer=setTimeout(redraw,60);if(save&&prefs)prefs.set('split',+f.toFixed(3))}
function splitFraction(){const v=parseFloat(($('workspace').style.getPropertyValue('--list')||'42'));return (isNaN(v)?42:v)/100}
$('splitter').onpointerdown=e=>{
  const sp=$('splitter'),main=$('workspace'),top=$('listing').parentElement.getBoundingClientRect().top,avail=main.getBoundingClientRect().bottom-top-10-14;
  sp.setPointerCapture(e.pointerId);sp.classList.add('dragging');
  sp.onpointermove=ev=>setSplit((ev.clientY-top)/avail,false);
  sp.onpointerup=()=>{sp.classList.remove('dragging');sp.onpointermove=null;sp.onpointerup=null;setSplit(splitFraction())};
};
$('splitter').onkeydown=e=>{if(e.key==='ArrowUp'||e.key==='ArrowDown'){e.preventDefault();setSplit(splitFraction()+(e.key==='ArrowUp'?-.04:.04))}};
if(typeof ResizeObserver!=='undefined')new ResizeObserver(()=>{clearTimeout(resizeTimer);resizeTimer=setTimeout(redraw,80)}).observe($('treemap'));

