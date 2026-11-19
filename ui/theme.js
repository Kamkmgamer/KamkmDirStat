// Themes. A theme gives a background (canvas) and an accent colour for the light and dark
// appearances; every other colour is derived from them in OKLCH, with text lightness solved
// for contrast, as in T3 Code's theme engine. Colours a theme names explicitly win. Theme
// files use T3 Code's format, so T3 Code themes can be imported.
(function(){
'use strict';
const ROLES=['canvas','chrome','surface','surfaceRaised','surfaceOverlay','hover','border','borderStrong','input','inputBackground','text','textMuted','textFaint','accent','accentHover','accentForeground','accentText','accentSurface','focus','success','warning','warningForeground','warningSurface','error','errorForeground','errorSurface','chart1','chart2','mapBackground','scrollbar','scrollbarHover'];
const ROLE_SET=new Set(ROLES);
const cssVar=role=>'--'+role.replace(/[A-Z0-9]/g,c=>'-'+c.toLowerCase());
// T3 Code roles with the same meaning under another name.
const T3_ALIASES={sidebar:'chrome',placeholder:'textFaint',terminalScrollbar:'scrollbar',terminalScrollbarHover:'scrollbarHover'};
const FILE_VERSION=1;
const SEEDS={light:{canvas:'#fafafa',accent:'#2563eb'},dark:{canvas:'#0f1012',accent:'#2c6be6'}};
const BUILT_IN=[
  {id:'graphite',label:'Graphite',light:{},dark:{}},
  {id:'mono',label:'Mono',light:{canvas:'#fafafa',accent:'#18181b'},dark:{canvas:'#0e0e0f',accent:'#e4e4e7'}},
  {id:'grove',label:'Grove',light:{canvas:'oklch(0.972 0.0055 157.2)',accent:'oklch(0.523 0.112 158.1)'},dark:{canvas:'oklch(0.235 0.018 162.8)',accent:'oklch(0.796 0.133 157.3)'}},
  {id:'ocean',label:'Ocean',light:{canvas:'oklch(0.974 0.0029 241.6)',accent:'oklch(0.537 0.12 247)'},dark:{canvas:'oklch(0.225 0.022 250.6)',accent:'oklch(0.759 0.106 241.5)'}},
  {id:'ember',label:'Ember',light:{canvas:'oklch(0.977 0.0027 60.7)',accent:'oklch(0.553 0.129 44.7)'},dark:{canvas:'oklch(0.225 0.016 42)',accent:'oklch(0.762 0.124 52.1)'}},
  {id:'iris',label:'Iris',light:{canvas:'oklch(0.977 0.0039 303.2)',accent:'oklch(0.525 0.154 294.2)'},dark:{canvas:'oklch(0.215 0.028 293.7)',accent:'oklch(0.672 0.169 293.9)'}},
];
const RESERVED=new Set(['system','light','dark',...BUILT_IN.map(t=>t.id)]);

// ---- Colour maths. RGB channels are 0-255 floats; OKLCH lightness is 0-1. ----
const clamp=(v,lo,hi)=>Math.min(hi,Math.max(lo,v));
const toLinear=c=>{c/=255;return c<=0.04045?c/12.92:((c+0.055)/1.055)**2.4};
const fromLinear=c=>255*clamp(c<=0.0031308?c*12.92:1.055*c**(1/2.4)-0.055,0,1);
function rgbToOklch({r,g,b}){
  r=toLinear(r);g=toLinear(g);b=toLinear(b);
  const l=Math.cbrt(0.4122214708*r+0.5363325363*g+0.0514459929*b),m=Math.cbrt(0.2119034982*r+0.6806995451*g+0.1073969566*b),s=Math.cbrt(0.0883024619*r+0.2817188376*g+0.6299787005*b);
  const L=0.2104542553*l+0.793617785*m-0.0040720468*s,A=1.9779984951*l-2.428592205*m+0.4505937099*s,B=0.0259040371*l+0.7827717662*m-0.808675766*s;
  const C=Math.hypot(A,B);return {L,C,h:C<1e-6?0:(Math.atan2(B,A)*180/Math.PI+360)%360};
}
function oklchLinear({L,C,h}){
  const a=C*Math.cos(h*Math.PI/180),b=C*Math.sin(h*Math.PI/180);
  const l=(L+0.3963377774*a+0.2158037573*b)**3,m=(L-0.1055613458*a-0.0638541728*b)**3,s=(L-0.0894841775*a-1.291485548*b)**3;
  return [4.0767416621*l-3.3077115913*m+0.2309699292*s,-1.2684380046*l+2.6097574011*m-0.3413193965*s,-0.0041960863*l-0.7034186147*m+1.707614701*s];
}
// Out-of-gamut colours keep their lightness and hue and lose chroma.
function oklchToRgb(c){
  const inGamut=C=>oklchLinear({...c,C}).every(v=>v>=-1e-4&&v<=1+1e-4);
  let C=Math.max(0,c.C);
  if(!inGamut(C)){let lo=0,hi=C;for(let i=0;i<20;i++){const mid=(lo+hi)/2;if(inGamut(mid))lo=mid;else hi=mid}C=lo}
  const [r,g,b]=oklchLinear({L:clamp(c.L,0,1),C,h:c.h});
  return {r:fromLinear(r),g:fromLinear(g),b:fromLinear(b)};
}
function parseColor(value){
  if(typeof value!=='string')return null;
  const s=value.trim().toLowerCase();let m;
  if((m=/^#([0-9a-f]{3,8})$/.exec(s))){
    let h=m[1];if(h.length<5)h=[...h].map(c=>c+c).join('');if(h.length!==6&&h.length!==8)return null;
    const n=i=>parseInt(h.slice(i,i+2),16);return {r:n(0),g:n(2),b:n(4),a:h.length===8?n(6)/255:1};
  }
  const num=(t,scale)=>t==='none'?0:t.endsWith('%')?parseFloat(t)/100*scale:parseFloat(t);
  if((m=/^rgba?\(([^)]*)\)$/.exec(s))){
    const p=m[1].split(/[\s,/]+/).filter(Boolean);if(p.length<3||p.length>4)return null;
    const c={r:num(p[0],255),g:num(p[1],255),b:num(p[2],255),a:p[3]===undefined?1:num(p[3],1)};
    if(![c.r,c.g,c.b,c.a].every(Number.isFinite))return null;
    return {r:clamp(c.r,0,255),g:clamp(c.g,0,255),b:clamp(c.b,0,255),a:clamp(c.a,0,1)};
  }
  if((m=/^oklch\(([^)]*)\)$/.exec(s))){
    const [main,alpha]=m[1].split('/'),p=main.trim().split(/[\s,]+/);if(p.length!==3)return null;
    const L=num(p[0],1),C=num(p[1],0.4),h=p[2]==='none'?0:parseFloat(p[2]),a=alpha===undefined?1:num(alpha.trim(),1);
    if(![L,C,h,a].every(Number.isFinite))return null;
    return {...oklchToRgb({L:clamp(L,0,1),C:Math.max(0,C),h}),a:clamp(a,0,1)};
  }
  return null;
}
const hex=c=>'#'+[c.r,c.g,c.b].map(v=>Math.round(clamp(v,0,255)).toString(16).padStart(2,'0')).join('');
const mix=(a,b,t)=>({r:a.r+(b.r-a.r)*t,g:a.g+(b.g-a.g)*t,b:a.b+(b.b-a.b)*t});
const rgba=(c,a)=>`rgba(${Math.round(c.r)},${Math.round(c.g)},${Math.round(c.b)},${a})`;
function luminance(c){return 0.2126*toLinear(c.r)+0.7152*toLinear(c.g)+0.0722*toLinear(c.b)}
// Contrast is measured on the 8-bit colours CSS will paint.
const round8=c=>({r:Math.round(c.r),g:Math.round(c.g),b:Math.round(c.b)});
function contrast(a,b){const x=luminance(round8(a)),y=luminance(round8(b));return (Math.max(x,y)+0.05)/(Math.min(x,y)+0.05)}
const worst=(c,on)=>Math.min(...on.map(s=>contrast(c,s)));
// At this luminance black and white text contrast equally.
const isDark=c=>luminance(c)<0.179;
// Moves only lightness, so a colour that is too faint keeps its hue.
function solve(base,on,target,lighter){
  if(worst(oklchToRgb(base),on)>=target)return oklchToRgb(base);
  let lo=lighter?base.L:0,hi=lighter?1:base.L;
  for(let i=0;i<24;i++){const mid=(lo+hi)/2,ok=worst(oklchToRgb({...base,L:mid}),on)>=target;if(ok===lighter)hi=mid;else lo=mid}
  return oklchToRgb({...base,L:lighter?hi:lo});
}
// The largest step from fg towards bg that still reads at the target contrast.
function soften(fg,bg,on,target){
  let lo=0,hi=1;for(let i=0;i<20;i++){const mid=(lo+hi)/2;if(worst(mix(fg,bg,mid),on)>=target)lo=mid;else hi=mid}
  return mix(fg,bg,lo);
}
function readableOn(bg){
  const light={r:255,g:255,b:255},dark={r:17,g:17,b:19};
  return contrast(light,bg)>=contrast(dark,bg)?light:dark;
}

// ---- Palette generation ----
// Lightness offsets from the canvas. Dark themes layer surfaces lighter, light themes put white
// panels on an off-white canvas with darker lines.
const STEPS={
  dark:{chrome:-0.012,surface:0.03,surfaceRaised:0.05,surfaceOverlay:0.06,hover:0.07,border:0.085,borderStrong:0.135,input:0.16,inputBackground:-0.016,mapBackground:-0.04},
  light:{chrome:-0.018,surface:0.03,surfaceRaised:-0.012,surfaceOverlay:0.03,hover:-0.022,border:-0.085,borderStrong:-0.13,input:-0.16,inputBackground:0.03,mapBackground:-0.05},
};
const STATUS={
  dark:{success:['#30c06f','#4ade80'],warning:['#fe9a00','#ffb900'],error:['#f2414a','#ff6467']},
  light:{success:['#16a34a','#15803d'],warning:['#f59e0b','#b45309'],error:['#e5313a','#c10007']},
};
function generate(appearance,canvasValue,accentValue){
  const dark=appearance==='dark';
  const given=parseColor(canvasValue)||parseColor(SEEDS[appearance].canvas);
  const accentRgb=parseColor(accentValue)||parseColor(SEEDS[appearance].accent);
  const base=rgbToOklch(given),acc=rgbToOklch(accentRgb);
  // Keep the background within the range its appearance can carry readable text on.
  const L0=clamp(base.L,dark?0.1:0.9,dark?0.32:1),hue=base.C>0.006?base.h:acc.h,tint=Math.min(0.05,base.C);
  const S=STEPS[appearance],at=(d,C=tint,h=hue)=>oklchToRgb({L:clamp(L0+d,0,1),C,h});
  const canvas=L0===base.L?given:at(0),out={};
  const set=(role,c)=>{out[role]=hex(c);return c};
  set('canvas',canvas);
  for(const role in S)set(role,at(S[role]));
  const surfaces=['canvas','chrome','surface','surfaceRaised','hover'].map(r=>parseColor(out[r]));
  const panels=surfaces.slice(0,4);
  const text=set('text',solve({L:dark?0.95:0.22,C:Math.min(0.012,tint),h:hue},panels,7,dark));
  const away=parseColor(out[dark?'chrome':'canvas']);
  set('textMuted',soften(text,away,surfaces,dark?5.2:5));
  set('textFaint',soften(text,away,panels,3.2));
  set('accent',accentRgb);
  set('accentHover',oklchToRgb({...acc,L:clamp(acc.L+(isDark(accentRgb)?0.06:-0.06),0,1)}));
  set('accentForeground',readableOn(accentRgb));
  const accentText=set('accentText',solve(acc,surfaces,4.6,dark));
  set('accentSurface',oklchToRgb({L:clamp(L0+(dark?0.075:-0.045),0,1),C:Math.min(0.09,acc.C*0.5),h:acc.h}));
  set('focus',solve(acc,surfaces,3,dark));
  for(const kind of ['success','warning','error']){
    const [b,f]=STATUS[appearance][kind],bc=parseColor(b),surface=mix(canvas,bc,dark?0.16:0.09);
    set(kind,solve(rgbToOklch(bc),panels,3,dark));
    if(kind==='success')continue;
    set(kind+'Surface',surface);
    set(kind+'Foreground',solve(rgbToOklch(parseColor(f)),[surface,...panels],4.6,dark));
  }
  // Two chart series that stay apart for colour-blind readers: blue against orange, whichever
  // is not the accent's family.
  const neutral=acc.C<=0.04,warm=!neutral&&acc.h>15&&acc.h<120;
  const chart1=set('chart1',solve(neutral?{L:dark?0.72:0.55,C:0.14,h:255}:{...acc,C:Math.max(acc.C,0.11)},panels,3,dark));
  set('chart2',solve({L:rgbToOklch(chart1).L,C:0.15,h:warm?250:55},panels,3,dark));
  const surface=parseColor(out.surface);
  set('scrollbar',mix(surface,text,dark?0.2:0.22));
  set('scrollbarHover',mix(surface,text,dark?0.34:0.38));
  return out;
}
// Extra values CSS needs that are not part of a theme.
function derived(colors,appearance){
  const dark=appearance==='dark',focus=parseColor(colors.focus),error=parseColor(colors.error);
  const solid=solve(rgbToOklch(error),[{r:255,g:255,b:255}],4.6,false);
  return {
    '--focus-ring':rgba(focus,dark?0.45:0.35),
    '--error-solid':hex(solid),
    '--backdrop':dark?'rgba(0,0,0,.58)':'rgba(24,24,30,.28)',
    '--shadow':dark?'0 18px 48px rgba(0,0,0,.5),0 2px 8px rgba(0,0,0,.35)':'0 18px 48px rgba(20,24,40,.14),0 2px 8px rgba(20,24,40,.08)',
  };
}

// ---- Theme definitions: {id, label, light:{role:colour}, dark:{role:colour}} ----
function resolve(theme,appearance){
  const spec=theme[appearance]||{},other=theme[appearance==='dark'?'light':'dark']||{};
  const colors=generate(appearance,spec.canvas||SEEDS[appearance].canvas,spec.accent||other.accent||SEEDS[appearance].accent);
  const canvas=parseColor(colors.canvas);
  for(const role of ROLES){
    if(role==='canvas'||role==='accent'||!spec[role])continue;
    const c=parseColor(spec[role]);if(c)colors[role]=hex(mix(canvas,c,c.a));
  }
  return colors;
}
function cleanColors(colors,strict){
  const out={};
  if(!colors||typeof colors!=='object'||Array.isArray(colors))throw new Error('Theme colours must be an object.');
  for(const [key,value] of Object.entries(colors)){
    const role=ROLE_SET.has(key)?key:T3_ALIASES[key];
    if(!role){if(strict)throw new Error(`"${key}" is not a theme colour.`);continue}
    const c=parseColor(value);
    if(!c)throw new Error(`The colour for "${key}" is not a CSS colour such as #3b82f6 or oklch(0.62 0.2 260).`);
    if(ROLE_SET.has(key)||!(role in out))out[role]=c.a<1?value.trim():hex(c);
  }
  return out;
}
function idFromName(name){return name.trim().toLowerCase().replace(/[^a-z0-9]+/g,'-').replace(/^-+|-+$/g,'').slice(0,48)||'custom-theme'}
// Reads a theme file from this app or from T3 Code. Roles this app does not use are ignored.
function parseFile(input){
  let v=input;
  if(typeof v==='string'){try{v=JSON.parse(v)}catch(e){throw new Error('This is not a theme file: it is not valid JSON.')}}
  if(!v||typeof v!=='object'||Array.isArray(v))throw new Error('Theme files contain a JSON object.');
  if(v.version!==FILE_VERSION)throw new Error(`This theme file uses an unsupported version. Expected version ${FILE_VERSION}.`);
  const name=typeof v.name==='string'?v.name.trim():'';
  if(!name||name.length>48)throw new Error('Theme files need a name of 48 characters or fewer.');
  if(v.appearance!=='light'&&v.appearance!=='dark')throw new Error('Theme files need an appearance of "light" or "dark".');
  const theme={id:'',label:name,light:{},dark:{}};
  theme[v.appearance]=cleanColors(v.colors,false);
  if(v.variants!==undefined){
    if(!v.variants||typeof v.variants!=='object')throw new Error('Theme variants must be an object.');
    for(const [ap,colors] of Object.entries(v.variants)){
      if(ap!=='light'&&ap!=='dark')throw new Error('Theme variants are named "light" or "dark".');
      if(ap!==v.appearance)theme[ap]=cleanColors(colors,false);
    }
  }
  if(!Object.keys(theme.light).length&&!Object.keys(theme.dark).length)throw new Error('This theme file has no colours this app can use.');
  let id=typeof v.id==='string'&&/^[a-z0-9][a-z0-9-]{0,47}$/.test(v.id)?v.id:idFromName(name);
  if(RESERVED.has(id))id='custom-'+id;
  theme.id=id;return theme;
}
function toFile(theme){
  const dark=Object.keys(theme.dark||{}).length||!Object.keys(theme.light||{}).length,base=dark?'dark':'light',other=dark?'light':'dark';
  const file={version:FILE_VERSION,id:theme.id,name:theme.label,appearance:base,colors:{...theme[base]}};
  if(Object.keys(theme[other]||{}).length)file.variants={[other]:{...theme[other]}};
  return JSON.stringify(file,null,2)+'\n';
}
// Stored themes are checked like files, so a damaged settings file cannot break the page.
function storedTheme(t){
  try{
    if(!t||typeof t.id!=='string'||!/^[a-z0-9][a-z0-9-]{0,47}$/.test(t.id)||RESERVED.has(t.id))return null;
    const label=typeof t.label==='string'?t.label.trim().slice(0,48):'';if(!label)return null;
    return {id:t.id,label,light:cleanColors(t.light||{},true),dark:cleanColors(t.dark||{},true)};
  }catch(e){return null}
}

// ---- Settings, saved by the app next to its other settings (localStorage in a browser) ----
const Prefs=(function(){
  let data={};
  const injected=window.KAMKM_SETTINGS;
  if(injected&&typeof injected==='object'&&!Array.isArray(injected))data=injected;
  else try{const s=JSON.parse(localStorage.getItem('kamkm.settings')||'{}');if(s&&typeof s==='object')data=s}catch(e){}
  let timer=null,saved=JSON.stringify(data);
  function flush(){
    timer=null;const s=JSON.stringify(data);if(s===saved)return;saved=s;
    if(typeof window.saveSettings==='function')Promise.resolve().then(()=>window.saveSettings(s)).catch(()=>{});
    else try{localStorage.setItem('kamkm.settings',s)}catch(e){}
  }
  return {get:(key,fallback)=>Object.prototype.hasOwnProperty.call(data,key)?data[key]:fallback,set(key,value){data[key]=value;clearTimeout(timer);timer=setTimeout(flush,150)},flush};
})();

// ---- Applying ----
const listeners=new Set();
const media=typeof matchMedia==='function'?matchMedia('(prefers-color-scheme: dark)'):null;
let custom=(Array.isArray(Prefs.get('themes'))?Prefs.get('themes'):[]).map(storedTheme).filter(Boolean);
let themeId=String(Prefs.get('theme','graphite')),mode=['system','light','dark'].includes(Prefs.get('mode'))?Prefs.get('mode'):'system';
let previewing=null,current=null;
const all=()=>[...BUILT_IN,...custom];
const find=id=>all().find(t=>t.id===id)||null;
const appearance=()=>mode==='system'?(media&&!media.matches?'light':'dark'):mode;
function paint(theme,ap){
  const colors=resolve(theme,ap),root=document.documentElement,style=root.style;
  root.classList.add('no-transitions');
  for(const role of ROLES)style.setProperty(cssVar(role),colors[role]);
  const extra=derived(colors,ap);for(const k in extra)style.setProperty(k,extra[k]);
  style.colorScheme=ap;root.dataset.appearance=ap;
  void root.offsetHeight;
  (window.requestAnimationFrame||setTimeout)(()=>root.classList.remove('no-transitions'));
  current={id:theme.id,appearance:ap,colors};
  // The window frame follows the theme where the system allows it.
  const look={dark:ap==='dark',background:colors.canvas,caption:colors.chrome,text:colors.text};
  if(!previewing){const was=Prefs.get('window');if(!was||JSON.stringify(was)!==JSON.stringify(look))Prefs.set('window',look)}
  if(typeof window.setWindowLook==='function')Promise.resolve().then(()=>window.setWindowLook(look.dark,look.caption,look.text)).catch(()=>{});
  for(const fn of listeners)try{fn(current)}catch(e){}
}
function refresh(){const theme=previewing||find(themeId)||BUILT_IN[0];paint(theme,appearance())}
if(media){const onChange=()=>{if(mode==='system')refresh()};if(media.addEventListener)media.addEventListener('change',onChange);else if(media.addListener)media.addListener(onChange)}
function uniqueId(id){let n=1,out=id;while(find(out)||RESERVED.has(out))out=id.slice(0,44)+'-'+(++n);return out}
window.Prefs=Prefs;
window.Theme={
  ROLES,BUILT_IN,cssVar,parseColor,hex,contrast,rgbToOklch,oklchToRgb,generate,resolve,parseFile,toFile,derived,
  list:all,find,get id(){return themeId},get mode(){return mode},get appearance(){return appearance()},get current(){return current},
  isCustom:id=>custom.some(t=>t.id===id),
  setTheme(id){if(!find(id))return;themeId=id;Prefs.set('theme',id);previewing=null;refresh()},
  setMode(m){if(!['system','light','dark'].includes(m))return;mode=m;Prefs.set('mode',m);refresh()},
  preview(theme){previewing=theme||null;refresh()},
  // Adds or replaces a custom theme and returns it as stored.
  save(theme,replaceId){
    const clean=storedTheme({...theme,id:replaceId||uniqueId(theme.id||idFromName(theme.label||''))});
    if(!clean)throw new Error('That theme could not be saved.');
    const i=custom.findIndex(t=>t.id===clean.id);
    if(i>=0)custom[i]=clean;else custom.push(clean);
    Prefs.set('themes',custom);return clean;
  },
  remove(id){custom=custom.filter(t=>t.id!==id);Prefs.set('themes',custom);if(themeId===id){themeId='graphite';Prefs.set('theme',themeId)}refresh()},
  onChange(fn){listeners.add(fn);return()=>listeners.delete(fn)},
};
if(!find(themeId))themeId='graphite';
refresh();
})();
