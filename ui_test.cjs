const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const html = fs.readFileSync('index.html', 'utf8');
const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, {value:'', textContent:'', dataset:{}, setAttribute(){}, classList:{values:new Set(),toggle(name,on){if(on)this.values.add(name);else this.values.delete(name)},add(name){this.values.add(name)},remove(name){this.values.delete(name)}}});
  return elements.get(id);
}
const context = vm.createContext({document:{getElementById:element}, window:{addEventListener(){}}, setTimeout, clearTimeout, console});
vm.runInContext(html.split('<script>')[1].split('</script>')[0], context);
assert.equal(vm.runInContext('target',context),'local','This PC is the default');
vm.runInContext("setTarget('remote')",context);assert.equal(element('connect').classList.values.has('is-local'),false);assert.equal(element('host').required,true);
element('method').value='auto'; element('host').value='buildbox'; element('password').value='should-not-be-sent'; element('path').value='/';
let c=vm.runInContext('connection()',context);
assert.equal(c.serverReport,true);assert.equal(c.serverHelper,false);element('serverHelper').checked=true;assert.equal(vm.runInContext('connection()',context).serverHelper,true);assert.equal(c.method,'auto'); assert.equal(c.host,'buildbox'); assert.equal(c.password,'');
element('method').value='password';element('method').onchange(); c=vm.runInContext('connection()',context);
assert.equal(c.password,'should-not-be-sent'); assert.equal(element('passwordFields').classList.values.has('hidden'),false);assert.equal(element('keyFields').classList.values.has('hidden'),true);
element('method').value='key';element('keyFile').value='C:\\key';element('method').onchange();c=vm.runInContext('connection()',context);
assert.equal(c.password,'');assert.equal(c.keyFile,'C:\\key');assert.equal(element('passwordFields').classList.values.has('hidden'),true);assert.equal(element('keyFields').classList.values.has('hidden'),false);
console.log('UI authentication modes passed');

// The treemap layout must tile the whole area, without overlaps, in proportion to size.
const sizes=[600,600,400,300,200,200,100,57,1,1];
const rects=[];
vm.runInContext('squarify',context)(sizes.map(s=>({s})),10,20,610,420,(k,x0,y0,x1,y1)=>rects.push({s:k.s,x0,y0,x1,y1}));
assert.equal(rects.length,sizes.length);
const total=sizes.reduce((a,b)=>a+b,0),area=600*400;
let covered=0;
for(const r of rects){
  const a=(r.x1-r.x0)*(r.y1-r.y0);covered+=a;
  assert.ok(Math.abs(a-r.s/total*area)<1e-6*area,`area for ${r.s}`);
  assert.ok(r.x0>=10-1e-9&&r.y0>=20-1e-9&&r.x1<=610+1e-9&&r.y1<=420+1e-9,'inside bounds');
}
assert.ok(Math.abs(covered-area)<1e-6*area,'covers the area');
for(let i=0;i<rects.length;i++)for(let j=i+1;j<rects.length;j++){
  const a=rects[i],b=rects[j],w=Math.min(a.x1,b.x1)-Math.max(a.x0,b.x0),h=Math.min(a.y1,b.y1)-Math.max(a.y0,b.y0);
  assert.ok(w<=1e-9||h<=1e-9,'no overlap');
}
const ext=vm.runInContext('ext',context);
assert.equal(ext('archive.TAR.GZ'),'gz');assert.equal(ext('.bashrc'),'');assert.equal(ext('Makefile'),'');
const size=vm.runInContext('size',context);
assert.equal(size(0),'0 B');assert.equal(size(1536),'1.5 KiB');assert.equal(size(42.86*1024**3),'42.9 GiB');
console.log('UI treemap layout passed');

(async()=>{
 let requested=null;
 context.startView=async id=>{requested=id};
 context.viewStatus=async()=>({running:false,view:{id:7,entries:[]}});
 const v=await vm.runInContext('loadView(7)',context);
 assert.equal(requested,7);assert.equal(v.id,7);
 context.viewStatus=async()=>({running:false,error:'Remote report connection closed.'});
 await assert.rejects(()=>vm.runInContext('loadView(8)',context),/Remote report connection closed/);
 console.log('UI remote folder loading passed');
})().catch(err=>{console.error(err);process.exitCode=1});

// Activity monitor: who may end which process, when sudo needs a password, and filtering.
{
 const canEnd=vm.runInContext('canEnd',context),needsSudoPassword=vm.runInContext('needsSudoPassword',context),canFreeCache=vm.runInContext('canFreeCache',context);
 const mine={pid:10,own:true},theirs={pid:11,own:false},kthread={pid:12,own:true,kernel:true};
 assert.equal(canEnd(mine,'none'),true);assert.equal(canEnd(theirs,'none'),false);assert.equal(canEnd(kthread,'root'),false);assert.equal(canEnd({pid:13,own:true,protected:true},'root'),false);
 for(const p of ['root','sudo','sudo-password'])assert.equal(canEnd(theirs,p),true);
 assert.equal(needsSudoPassword(theirs,'sudo-password'),true);assert.equal(needsSudoPassword(mine,'sudo-password'),false);
 assert.equal(needsSudoPassword(theirs,'sudo'),false);assert.equal(needsSudoPassword(null,'sudo-password'),true);
 assert.equal(canFreeCache('none'),false);assert.equal(canFreeCache('root'),true);assert.equal(canFreeCache('sudo-password'),true);
 const filter=vm.runInContext('filterProcesses',context),procs=[{pid:42,name:'nginx',user:'www-data',cmd:'nginx: worker'},{pid:7,name:'python3',user:'alice',cmd:'python3 train.py'}];
 assert.equal(filter(procs,'').length,2);assert.deepEqual(filter(procs,'TRAIN').map(p=>p.pid),[7]);assert.deepEqual(filter(procs,'42').map(p=>p.pid),[42]);assert.deepEqual(filter(procs,'www').map(p=>p.pid),[42]);
 const duration=vm.runInContext('duration',context);
 assert.equal(duration(59),'0 min');assert.equal(duration(3*3600+5*60),'3 h 5 min');assert.equal(duration(2*86400+3600),'2 d 1 h');
 vm.runInContext('syncTools()',context);assert.equal(element('submitLabel').textContent,'Connect');assert.equal(element('path').required,true);
 assert.equal(canEnd(theirs,'admin'),true);assert.equal(canFreeCache('admin'),true);assert.equal(needsSudoPassword(theirs,'admin'),false);
 // This PC: drives that are ticked, or one folder that replaces them.
 vm.runInContext("setTarget('local')",context);
 assert.equal(element('connect').classList.values.has('is-local'),true);assert.equal(element('host').required,false);assert.equal(element('path').required,false);
 vm.runInContext("drives=[{path:'C:\\\\',checked:true},{path:'D:\\\\',checked:false},{path:'E:\\\\',checked:true}]",context);
 element('localFolder').value='';let lc=vm.runInContext('connection()',context);
 assert.equal(lc.local,true);assert.deepEqual([...lc.paths],['C:\\','E:\\']);assert.equal(lc.password,undefined);
 element('localFolder').value='  D:\\Games  ';lc=vm.runInContext('connection()',context);assert.deepEqual([...lc.paths],['D:\\Games']);
 vm.runInContext('syncTools()',context);assert.equal(element('submitLabel').textContent,'Scan');
 for(const [p,name,hint] of [['darwin','This Mac','/Users/you/Downloads'],['linux','This computer','/home/you/Downloads'],['windows','This PC','C:\\Users\\you\\Downloads']]){
  vm.runInContext(`platform='${p}';applyPlatform()`,context);
  assert.equal(element('localTargetName').textContent,name);assert.equal(element('localFolder').placeholder,hint);
 }
 console.log('UI activity monitor rules passed');
}

// Opening the app starts local monitoring without requiring a disk scan.
(async()=>{
 const calls=[];
 const startup=vm.createContext({document:{getElementById:element},window:{addEventListener(){}},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,
   startMonitor:async c=>calls.push(c),monitorStatus:async()=>({running:true}),appPlatform:async()=> 'windows'});
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],startup);
 await new Promise(resolve=>setTimeout(resolve,0));
 assert.equal(calls.length,1,'monitor starts on launch');
 assert.equal(calls[0].local,true,'monitor starts on this computer');
 assert.equal(vm.runInContext('tab',startup),'activity','activity is the opening view');
 console.log('UI automatic monitoring passed');
})().catch(err=>{console.error(err);process.exitCode=1});

// Themes: every built-in theme keeps text readable, the CSS defaults match what the engine
// makes, settings carry the theme, and theme files round-trip and import from T3 Code.
{
 const source=html.match(/<script id="theme">([\s\S]*?)<\/script>/)[1];
 const props=new Map(),saved=[],looks=[];
 const root={style:{setProperty(k,v){props.set(k,v)}},classList:{add(){},remove(){}},dataset:{},offsetHeight:0};
 const win={KAMKM_SETTINGS:{mode:'dark',theme:'ocean'},saveSettings:s=>{saved.push(s)},setWindowLook:(...a)=>{looks.push(a)}};
 const themes=vm.createContext({window:win,document:{documentElement:root},setTimeout,clearTimeout,console,matchMedia:()=>({matches:true,addEventListener(){}})});
 vm.runInContext(source,themes);
 const plain=o=>JSON.parse(JSON.stringify(o));
 const T=win.Theme,ratio=(a,b)=>T.contrast(T.parseColor(a),T.parseColor(b));

 for(const t of T.BUILT_IN)for(const ap of ['dark','light']){
  const c=T.resolve(t,ap),at=`${t.id} ${ap}`;
  for(const bg of ['canvas','chrome','surface','surfaceRaised']){
   assert.ok(ratio(c.text,c[bg])>=7,`${at}: text on ${bg}`);
   assert.ok(ratio(c.textMuted,c[bg])>=4.5,`${at}: muted text on ${bg}`);
   assert.ok(ratio(c.accentText,c[bg])>=4.5,`${at}: accent text on ${bg}`);
   assert.ok(ratio(c.textFaint,c[bg])>=3,`${at}: faint text on ${bg}`);
   assert.ok(ratio(c.focus,c[bg])>=3,`${at}: focus outline on ${bg}`);
  }
  assert.ok(ratio(c.accentForeground,c.accent)>=4.5,`${at}: button text on the accent`);
  assert.ok(ratio(c.warningForeground,c.warningSurface)>=4.5,`${at}: warning text`);
  assert.ok(ratio(c.errorForeground,c.errorSurface)>=4.5,`${at}: error text`);
  assert.ok(ratio('#ffffff',T.derived(c,ap)['--error-solid'])>=4.5,`${at}: text on solid error buttons`);
  for(const role of T.ROLES)assert.match(c[role],/^#[0-9a-f]{6}$/,`${at}: ${role} is a colour`);
 }

 // The stylesheet's defaults, used before the script runs, are Graphite's colours.
 for(const [ap,css] of [['dark',html.match(/\n:root\{(--canvas:[^}]*)\}/)[1]],['light',html.match(/prefers-color-scheme:light\)\{:root\{([^}]*)\}/)[1]]]){
  const want=T.resolve(T.find('graphite'),ap),extra=T.derived(want,ap);
  const have=Object.fromEntries(css.split(';').filter(d=>d.startsWith('--')).map(d=>[d.slice(0,d.indexOf(':')),d.slice(d.indexOf(':')+1)]));
  for(const role of T.ROLES)assert.equal(have[T.cssVar(role)],want[role],`${ap} default for ${role}`);
  for(const k in extra)assert.equal(have[k],extra[k],`${ap} default for ${k}`);
 }

 // Saved settings choose the theme, and the window frame follows it.
 assert.equal(T.id,'ocean');assert.equal(T.mode,'dark');
 const ocean=T.resolve(T.find('ocean'),'dark');
 assert.equal(props.get('--canvas'),ocean.canvas);assert.equal(props.get('--chart-1'),ocean.chart1);
 assert.equal(root.style.colorScheme,'dark');
 const tick=()=>new Promise(resolve=>setTimeout(resolve,0));
 (async()=>{
  await tick();
  assert.deepEqual(looks.at(-1),[true,ocean.chrome,ocean.text]);
  T.setMode('light');win.Prefs.flush();await tick();
  const stored=JSON.parse(saved.at(-1));
  assert.equal(stored.mode,'light');assert.equal(stored.theme,'ocean');
  assert.deepEqual(stored.window,{dark:false,background:T.resolve(T.find('ocean'),'light').canvas,caption:T.resolve(T.find('ocean'),'light').chrome,text:T.resolve(T.find('ocean'),'light').text});

  // Custom themes are saved, exported and read back unchanged.
  const mine=T.save({label:'Harbor Night',light:{canvas:'#ffffff',accent:'#0a7'},dark:{accent:'oklch(0.7 0.15 250)',border:'rgba(255,255,255,0.12)'}});
  assert.equal(mine.id,'harbor-night');assert.equal(mine.light.accent,'#00aa77');
  const again=T.parseFile(T.toFile(mine));
  assert.deepEqual(plain(again),plain(mine));
  assert.equal(T.save({label:'Harbor Night',dark:{accent:'#f00'},light:{}}).id,'harbor-night-2','names do not collide');
  T.setTheme('harbor-night');win.Prefs.flush();await tick();
  assert.deepEqual(JSON.parse(saved.at(-1)).themes.map(t=>t.id),['harbor-night','harbor-night-2']);
  T.remove('harbor-night');assert.equal(T.id,'graphite','removing the current theme returns to Graphite');

  // A T3 Code theme file imports; its roles with other names map across and unknown ones are skipped.
  const t3=T.parseFile(JSON.stringify({version:1,id:'dusk',name:'Dusk',appearance:'dark',
   colors:{canvas:'#1b1d24',accent:'oklch(0.7 0.15 250)',sidebar:'#16181e',placeholder:'#7a7f8c',diffAdded:'#00ff00'},
   variants:{light:{canvas:'#fafafa',accent:'#2b59c3'}}}));
  assert.equal(t3.id,'dusk');assert.equal(t3.label,'Dusk');
  assert.deepEqual(plain(t3.dark),{canvas:'#1b1d24',accent:T.hex(T.parseColor('oklch(0.7 0.15 250)')),chrome:'#16181e',textFaint:'#7a7f8c'});
  assert.deepEqual(plain(t3.light),{canvas:'#fafafa',accent:'#2b59c3'});
  assert.equal(T.parseFile({version:1,id:'graphite',name:'Graphite copy',appearance:'light',colors:{accent:'#123456'}}).id,'custom-graphite');

  for(const [file,message] of [
   ['{oops',/not valid JSON/],['[]',/JSON object/],[{version:2,name:'x',appearance:'dark',colors:{accent:'#fff'}},/unsupported version/],
   [{version:1,name:'',appearance:'dark',colors:{accent:'#fff'}},/name/],[{version:1,name:'x',appearance:'dim',colors:{accent:'#fff'}},/appearance/],
   [{version:1,name:'x',appearance:'dark',colors:{accent:'blue-ish'}},/not a CSS colour/],[{version:1,name:'x',appearance:'dark',colors:{diffAdded:'#0f0'}},/no colours/],
   [{version:1,name:'x',appearance:'dark',colors:{accent:'#fff'},variants:{dim:{}}},/variants/],
  ])assert.throws(()=>T.parseFile(typeof file==='string'?file:JSON.stringify(file)),message);
  console.log('UI themes passed');
 })().catch(err=>{console.error(err);process.exitCode=1});
}
