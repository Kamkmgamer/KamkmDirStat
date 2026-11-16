const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const html = fs.readFileSync('index.html', 'utf8');
// The mocks below create any element on demand, so check separately that every element the
// script looks up by id exists in the page; a missing one stops the real script while loading.
{
  const script = html.split('<script>')[1].split('</script>')[0];
  const ids = new Set([...html.matchAll(/\bid="([^"]+)"/g)].map(m => m[1]));
  const missing = [...new Set([...script.matchAll(/\$\('([A-Za-z0-9_-]+)'\)/g)].map(m => m[1]))].filter(id => !ids.has(id));
  assert.deepEqual(missing, [], 'elements used by the script but missing from the page');
}
const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, {value:'', textContent:'', dataset:{}, style:{}, setAttribute(){}, replaceChildren(){}, classList:{values:new Set(),toggle(name,on){if(on)this.values.add(name);else this.values.delete(name)},add(name){this.values.add(name)},remove(name){this.values.delete(name)},contains(name){return this.values.has(name)}}});
  return elements.get(id);
}
const context = vm.createContext({document:{getElementById:element,createElement:tag=>({tag,value:'',textContent:'',setAttribute(){}})}, window:{addEventListener(){}}, setTimeout, clearTimeout, setInterval:()=>1, clearInterval(){}, console});
vm.runInContext(html.split('<script>')[1].split('</script>')[0], context);
vm.runInContext('placeholder=()=>{};serverScanPrompt=()=>{};redraw=()=>{}',context);
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
 context.startView=async(t,id)=>{requested={target:t,id}};
 context.viewStatus=async()=>({running:false,view:{id:7,entries:[]}});
 const v=await vm.runInContext('loadView(7)',context);
 assert.deepEqual(requested,{target:'remote',id:7});assert.equal(v.id,7);
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
 const startup=vm.createContext({document:{getElementById:element,createElement:tag=>({tag,value:'',textContent:'',setAttribute(){}})},window:{addEventListener(){}},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,
   startMonitor:async c=>calls.push(c),monitorStatus:async()=>({running:true}),appPlatform:async()=> 'windows'});
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],startup);
 await new Promise(resolve=>setTimeout(resolve,0));
 assert.equal(calls.length,1,'monitor starts on launch');
 assert.equal(calls[0].local,true,'monitor starts on this computer');
 assert.equal(vm.runInContext('tab',startup),'activity','activity is the opening view');
 console.log('UI automatic monitoring passed');

 // Connecting to a server monitors it without scanning; the scan starts from its button.
 const scans=[];
 startup.startScan=async c=>scans.push(c);
 startup.scanStatus=async()=>({running:true,progress:{}});
 vm.runInContext('var prompts=0;serverScanPrompt=()=>{prompts++};placeholder=()=>{};redraw=()=>{};resetFind=()=>{}',startup);
 const server={host:'buildbox',port:'22',user:'root',method:'auto',path:'/srv',fingerprint:'SHA256:x'};
 vm.runInContext(`setTarget('remote')`,startup);
 vm.runInContext('prompts=0',startup);
 await vm.runInContext('begin',startup)(server,'root@buildbox');
 assert.equal(calls.at(-1).host,'buildbox','the server is monitored');assert.equal(scans.length,0,'connecting does not scan the server');
 assert.equal(vm.runInContext('prompts',startup),1,'the storage view offers a scan');assert.equal(vm.runInContext('tab',startup),'activity');
 assert.equal(element('scanServerButton').classList.values.has('hidden'),false,'the scan button is offered');
 assert.equal(element('scanServerLabel').textContent,'Scan files on root@buildbox');
 await vm.runInContext('scanServer()',startup); // other suites share these mock elements and replace their handlers
 assert.equal(scans.length,1);assert.equal(scans[0].host,'buildbox');assert.equal(scans[0].path,'/srv');assert.equal(scans[0].fingerprint,'SHA256:x');
 assert.equal(vm.runInContext('tab',startup),'storage');assert.equal(element('scanServerButton').classList.values.has('hidden'),true,'no second scan while one runs');
 vm.runInContext('finishScan()',startup);assert.equal(element('scanServerButton').classList.values.has('hidden'),false,'the server can be scanned again');
 vm.runInContext(`setTarget('local')`,startup);
 await vm.runInContext('begin',startup)({local:true,paths:['C:\\']},'This PC');
 assert.equal(scans.length,2,'this computer is still scanned on start');assert.equal(element('scanServerButton').classList.values.has('hidden'),true,'the server button goes away');
 console.log('UI server monitoring without a scan passed');
})().catch(err=>{console.error(err);process.exitCode=1});

// Computer tabs own their monitor and scan sessions. Background work keeps its target and
// switching tabs never stops a monitor or discards the other computer's scan.
(async()=>{
 const script=html.split('<script>')[1].split('</script>')[0];
 const settle=(ms=10)=>new Promise(resolve=>setTimeout(resolve,ms));
 function sessionContext(extra={}){
  let nextTimer=0;const timers=new Map();
  const ctx=vm.createContext({document:{getElementById:element,createElement:tag=>({tag,value:'',textContent:'',dataset:{},style:{},setAttribute(){},replaceChildren(){}})},window:{addEventListener(){}},setTimeout,clearTimeout,performance,
   setInterval:(fn,ms)=>{const id=++nextTimer;timers.set(id,{fn,ms});return id},clearInterval:id=>{timers.delete(id)},console,...extra});
  vm.runInContext(script,ctx);
  vm.runInContext('placeholder=()=>{};serverScanPrompt=()=>{};redraw=()=>{};render=()=>{};renderActivity=()=>{};drawSparks=()=>{}',ctx);
  return {ctx,timers};
 }
 const stopped=[],cleared=[],samples={
  local:{seq:1,cpu:{total:11,cores:[10]},memory:{total:100,used:25},network:[]},
  remote:{seq:1,cpu:{total:72,cores:[70]},memory:{total:200,used:100},network:[]}
 };
 const monitors=sessionContext({
  monitorStatus:async t=>({running:true,seq:samples[t].seq,sample:samples[t],results:[]}),
  stopMonitor:async t=>stopped.push(t),clearResults:async t=>cleared.push(t)
 });
 const mc=monitors.ctx;
 await vm.runInContext('beginMonitor',mc)('This PC',true,true);await settle();
 await vm.runInContext('beginMonitor',mc)('root@buildbox',false,true);await settle();
 assert.equal(vm.runInContext('monitorSessions.local.hist.cpu.length',mc),1);
 assert.equal(vm.runInContext('monitorSessions.remote.hist.cpu.length',mc),1,'the hidden SSH monitor records samples too');
 assert.ok(monitors.timers.has(vm.runInContext('monitorSessions.local.poll',mc)));
 assert.ok(monitors.timers.has(vm.runInContext('monitorSessions.remote.poll',mc)),'both monitor timers stay active');
 vm.runInContext("setTarget('remote')",mc);
 assert.equal(vm.runInContext('sample.cpu.total',mc),72);
 assert.equal(vm.runInContext('hist===monitorSessions.remote.hist',mc),true);
 samples.local={seq:2,cpu:{total:33,cores:[30]},memory:{total:100,used:50},network:[]};
 await vm.runInContext("pollMonitor('local')",mc);
 assert.equal(vm.runInContext('monitorSessions.local.hist.cpu.length',mc),2,'the hidden local monitor keeps its history');
 assert.equal(vm.runInContext('sample.cpu.total',mc),72,'a hidden sample cannot overwrite the SSH view');
 vm.runInContext("setTarget('local');setTarget('remote');setTarget('local')",mc);
 assert.equal(vm.runInContext('hist.cpu.length',mc),2);
 assert.deepEqual(Array.from(vm.runInContext('hist.cpu',mc)),[11,33]);
 assert.deepEqual(stopped,[],'tab switches do not stop either monitor');
 assert.deepEqual(cleared,[],'tab switches do not clear either scan');
 console.log('UI independent monitor sessions passed');

 const scans=[],statuses={local:{running:true,progress:{mode:'local'}},remote:{running:true,progress:{mode:'remote'}}};
 const views={local:{id:101,entries:[]},remote:{id:202,entries:[]}};
 const storage=sessionContext({
  startScan:async c=>scans.push(c),scanStatus:async t=>statuses[t],
  startView:async()=>{},viewStatus:async t=>({running:false,view:views[t]}),
  stopMonitor:async t=>stopped.push(t),clearResults:async t=>cleared.push(t)
 });
 const sc=storage.ctx;
 await vm.runInContext('beginScan',sc)({local:true,paths:['C:\\']},'This PC',true);
 await vm.runInContext('beginScan',sc)({host:'buildbox',path:'/srv'},'root@buildbox',false);
 await settle();
 assert.equal(scans.length,2);
 assert.ok(storage.timers.has(vm.runInContext('storageSessions.local.poll',sc)),'the local scan timer keeps running');
 assert.ok(storage.timers.has(vm.runInContext('storageSessions.remote.poll',sc)),'the SSH scan timer runs at the same time');
 vm.runInContext('storageSessions.local.lastView={id:101,entries:[]};lastView=storageSessions.local.lastView',sc);
 statuses.remote={running:false,files:1,size:20,mode:'remote'};
 await vm.runInContext("pollScanFor('remote')",sc);
 assert.equal(vm.runInContext('storageSessions.remote.needsFinal',sc),true);
 assert.equal(vm.runInContext('storageSessions.remote.poll',sc),null);
 assert.equal(vm.runInContext('storageSessions.local.lastView.id',sc),101,'an SSH scan completion leaves local results intact');
 assert.notEqual(vm.runInContext('storageSessions.local.poll',sc),null,'an SSH scan completion leaves the local scan running');
 vm.runInContext("setTarget('remote');restoreStorageTarget('remote')",sc);
 await settle(120);
 assert.equal(vm.runInContext('storageSessions.remote.needsFinal',sc),false,'restoring during a poll still applies the final scan');
 assert.equal(vm.runInContext('storageSessions.remote.lastView.id',sc),202);
 assert.equal(vm.runInContext('storageSessions.local.lastView.id',sc),101,'the local scan result remains retained after browsing SSH');
 assert.deepEqual(stopped,[]);assert.deepEqual(cleared,[]);
 console.log('UI independent scan sessions passed');

 // A delayed search response belongs to the computer that started it, even after a switch.
 let releaseSearch;const searchGate=new Promise(resolve=>{releaseSearch=resolve}),searchCalls=[];
 const searching=sessionContext({
  startSearch:async(t,q)=>{searchCalls.push({target:t,...q});return 9},
  searchStatus:async()=>{await searchGate;return {seq:9,result:{hits:[],total:0,totalSize:0}}}
 });
 const qc=searching.ctx;
 vm.runInContext("setTarget('remote');filesMode=true;scanReady=true;renderHits=()=>{throw new Error('cross-target search render')}",qc);
 const searchDone=vm.runInContext("runSearch('remote')",qc);await settle();
 vm.runInContext("setTarget('local')",qc);releaseSearch();await searchDone;
 assert.equal(searchCalls[0].target,'remote');assert.equal(searchCalls[0].text,'');
 console.log('UI target-safe delayed search passed');
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

// Map labels keep the size when they can, and opening a program asks first.
(async()=>{
 const reqs=[],statuses=[];let seq=0,confirmed=0;
 const ctx=vm.createContext({document:{getElementById:element},window:{addEventListener(){}},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,performance,
   startOpen:async r=>{reqs.push(r);return ++seq},openStatus:async()=>statuses.shift(),
   startSearch:async(t,q)=>{reqs.push({target:t,...q});return 7},searchStatus:async()=>({seq:7,result:{hits:[],total:0,totalSize:0}})});
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],ctx);
 vm.runInContext('placeholder=()=>{};serverScanPrompt=()=>{};redraw=()=>{};fillTypes=()=>{}',ctx);
 const fit=vm.runInContext('fitLabel',ctx),m={measureText:s=>({width:[...s].length*6})};
 assert.equal(fit(m,'Users/',' (120.8 GiB)',200),'Users/ (120.8 GiB)');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',120),'Adminis… (120.8 GiB)','the name shortens before the size goes');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',60),'Administr…','the size goes when the name would not fit');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',12),'','nothing when not even two letters fit');

 ctx.confirmAction=async a=>{confirmed++;assert.match(a.title,/^Run setup\.exe\?/);vm.runInContext("setTarget('remote')",ctx);return {}};
 statuses.push({seq:1,running:false,confirm:'run',name:'setup.exe',path:'C:/x/setup.exe'},{seq:2,running:false,name:'setup.exe'});
 const done=await vm.runInContext("openEntry(4,'setup.exe')",ctx);
 assert.equal(confirmed,1);assert.equal(done.name,'setup.exe');
 assert.deepEqual(reqs.map(r=>({...r})),[{target:'local',dir:4,name:'setup.exe',action:'open'},{target:'local',dir:4,name:'setup.exe',action:'open',confirmed:true}]);
 vm.runInContext("setTarget('local')",ctx);
 ctx.confirmAction=async()=>null;reqs.length=0;
 statuses.push({seq:3,running:false,confirm:'download',name:'clip.mp4',size:5});
 assert.equal(await vm.runInContext("openEntry(2,'clip.mp4')",ctx),null,'declining stops');
 assert.equal(reqs.length,1);
 statuses.push({seq:4,running:false,error:'gone'});
 assert.equal(await vm.runInContext("openEntry(2,'x.txt')",ctx),null);
 assert.equal(element('toastMsg').textContent,'gone','errors are shown');

 // Searching with nothing typed lists the largest files; the type and extension are sent.
 reqs.length=0;element('search').value='  ';element('extFilter').value='mkv';
 vm.runInContext("filesMode=true;findKind='videos';renderHits=()=>{};runSearch()",ctx);
 await new Promise(r=>setTimeout(r,30));
 assert.deepEqual({...reqs[0],exts:[...reqs[0].exts]},{target:'local',text:'',kind:'videos',exts:['mkv'],sort:'size',limit:1000,types:true,scope:''});

 // A result of a search of every drive opens by its folder's path, and so does its retry.
 reqs.length=0;ctx.confirmAction=async()=>({});
 statuses.push({seq:5,running:false,confirm:'run',name:'tool.exe'},{seq:6,running:false,name:'tool.exe'});
 await vm.runInContext("openEntry(null,'tool.exe','open','D:\\\\Apps')",ctx);
 assert.deepEqual(reqs.map(r=>({...r})),[{target:'local',dir:0,name:'tool.exe',action:'open',path:'D:\\Apps'},{target:'local',dir:0,name:'tool.exe',action:'open',path:'D:\\Apps',confirmed:true}]);

 // The drive bar offers what would make the index current, and hides what does not apply.
 const made=[];
 ctx.document.createElement=tag=>{const e={tag,children:[],textContent:'',dataset:{},setAttribute(){},append(...c){this.children.push(...c)},classList:{values:new Set(),add(n){this.values.add(n)},toggle(n,on){if(on)this.values.add(n);else this.values.delete(n)}}};made.push(e);return e};
 let chips=[];element('driveChips').replaceChildren=(...c)=>{chips=c};
 const hidden=id=>element(id).classList.values.has('hidden');
 const text=e=>typeof e==='string'?e:(e.textContent||'')+e.children.map(text).join('');
 vm.runInContext("renderDriveBar",ctx)({source:'app',admin:false,service:'',drives:[{root:'C:\\',mode:'snapshot',state:'ready',entries:5,built:Date.now()-7200e3},{root:'D:\\',state:'building',read:1200,entries:10}]});
 assert.deepEqual(chips.map(text),['C:read 2 h ago','D:indexing · 1,200']);
 assert.match(element('driveHint').textContent,/administrator rights or the background indexer/);
 assert.deepEqual(['driveRefresh','driveAdmin','driveService','driveRemove'].map(hidden),[false,false,false,true]);
 assert.equal(element('driveService').textContent,'Set up background indexer');
 vm.runInContext("renderDriveBar",ctx)({source:'service',admin:false,service:'running',drives:[{root:'C:\\',mode:'live',state:'ready',entries:5}]});
 assert.deepEqual(chips.map(text),['C:live']);
 assert.deepEqual(['driveRefresh','driveAdmin','driveService','driveRemove'].map(hidden),[true,true,true,false]);
 vm.runInContext("renderDriveBar",ctx)({source:'service',admin:false,service:'running',outdated:true,drives:[]});
 assert.equal(hidden('driveService'),false);assert.equal(element('driveService').textContent,'Update background indexer');
 console.log('UI map labels, opening, search and drive index passed');
})().catch(err=>{console.error(err);process.exitCode=1});

// Local History and Events: chart interaction, snapshots, filtering, alert baselines and failures.
(async()=>{
 const els=new Map(),rangeButtons=[],toasts=[],queries=[];
 function classes(){const values=new Set();return {values,add(...n){n.forEach(x=>values.add(x))},remove(...n){n.forEach(x=>values.delete(x))},toggle(n,on){if(on===undefined)on=!values.has(n);if(on)values.add(n);else values.delete(n);return on},contains(n){return values.has(n)}}}
 function node(tag='div'){
  const n={tag,children:[],dataset:{},style:{setProperty(k,v){this[k]=v}},classList:classes(),attrs:{},value:'',checked:false,textContent:'',disabled:false,
   setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return this.attrs[k]},append(...c){this.children.push(...c)},replaceChildren(...c){this.children=c;this.textContent=''},
   remove(){this.removed=true},addEventListener(){},removeEventListener(){},contains(){return false},focus(){this.focused=true},select(){},click(){if(this.onclick)return this.onclick()},querySelectorAll(){return []},querySelector(){return null}};
  return n;
 }
 function get(id){if(!els.has(id))els.set(id,node());return els.get(id)}
 for(const seconds of [300,900,3600,10800,21600,86400,259200]){const b=node('button');b.dataset.seconds=String(seconds);rangeButtons.push(b)}
 get('historyRanges').querySelectorAll=()=>rangeButtons;
 const emptyTitle=node('h2'),emptyDetail=node('p');get('historyEmpty').querySelector=s=>s==='h2'?emptyTitle:emptyDetail;
 const calls={strokes:0,fills:0,lines:0,text:0,rects:0};
 const draw=new Proxy({measureText:s=>({width:String(s).length*6}),beginPath(){this.path=0},moveTo(){this.path=(this.path||0)+1},lineTo(){this.path=(this.path||0)+1;calls.lines++},stroke(){calls.strokes++},fill(){calls.fills++},fillText(){calls.text++},fillRect(){calls.rects++},strokeRect(){calls.rects++},arc(){},closePath(){},setTransform(){},clearRect(){},setLineDash(){},save(){},restore(){}},{get:(o,k)=>o[k]});
 const canvas=get('historyChart');canvas.clientWidth=800;canvas.clientHeight=410;canvas.width=800;canvas.height=410;canvas.getContext=()=>draw;canvas.getBoundingClientRect=()=>({left:0,top:0,width:800,height:410});canvas.setPointerCapture=()=>{};canvas.hasPointerCapture=()=>false;
 const savedPrefs={};
 const prefStore={get(k,f){return Object.prototype.hasOwnProperty.call(savedPrefs,k)?savedPrefs[k]:f},set(k,v){savedPrefs[k]=v},flush(){}};
 const document={getElementById:get,createElement:tag=>node(tag),createElementNS:(ns,tag)=>node(tag),documentElement:node('html'),addEventListener(){},hidden:false};
 const ctx=vm.createContext({document,window:{addEventListener(){},devicePixelRatio:1,Prefs:prefStore},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,performance,
  getComputedStyle:()=>({fontFamily:'Segoe UI',getPropertyValue:name=>({'--text':'#eeeeee','--text-muted':'#aaaaaa','--text-faint':'#777777','--border':'#333333','--chart-1':'#2277ee','--chart-2':'#ee7722','--accent':'#2277ee','--warning-foreground':'#ffaa00','--error-foreground':'#ff4444'}[name]||'#000000')})});
 const now=Math.floor(Date.now()/1000),points=[50,40,30,20,10,0].map((ago,i)=>({time:now-ago,cpu:10+i*10,memory:30+i,gpu:i===2?null:20+i,rx:1000*(i+1),tx:500*(i+1)}));
 const process=(pid,name,cpu=1)=>({pid,start:100,name,user:'tester',cmd:'C:\\Apps\\'+name,cpu,rss:pid*1024,mem:2,publisher:'Example Publisher',signed:true});
 let allEvents=[
  {id:'old-alert',time:now-25,type:'process_new',severity:'notice',title:'old.exe ran for the first time',message:'old.exe is new',process:process(10,'old.exe')},
  {id:'old-start',time:now-20,type:'process_start',severity:'info',title:'old.exe started',message:'old.exe started',process:process(10,'old.exe')},
  {id:'old-cpu',time:now-15,type:'resource_cpu',severity:'warning',title:'CPU use is high',message:'CPU stayed high',metric:'cpu',value:96,threshold:90}
 ];
 let processList=[process(2,'low.exe',2),process(1,'busy.exe',88)];
 let historyImpl=async q=>{
  queries.push(q);
  if(q.processTime)return {from:q.from,to:q.to,points:[],events:[],processTime:q.processTime,processes:processList};
  if(q.points===1)return {from:q.from,to:q.to,points:[],events:allEvents};
  return {from:q.from,to:q.to,oldest:points[0].time,newest:points.at(-1).time,points,events:allEvents.filter(e=>e.time>=q.from&&e.time<=q.to)};
 };
 ctx.monitorHistory=q=>historyImpl(q);
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],ctx);
 const tick=(ms=0)=>new Promise(r=>setTimeout(r,ms));
 await tick(10);
 assert.equal(vm.runInContext('eventBaseline',ctx),true,'existing events establish the alert baseline');
 assert.equal(vm.runInContext('knownEventIDs.size',ctx),3);
 ctx.toast=(text,opts={})=>toasts.push({text,opts});
 assert.equal(toasts.length,0,'existing alerts never pop up');
 const history=await vm.runInContext('loadResourceHistory()',ctx);
 assert.equal(history.points.length,6);assert.equal(get('historyEmpty').classList.contains('hidden'),true);
 assert.match(get('historyCoverage').textContent,/6 samples/);assert.ok(calls.strokes>0&&calls.fills>0&&calls.text>0,'canvas chart, markers and labels drawn');
 console.log('UI history loading and alert baseline passed');

 // Range buttons use live relative ranges and clear any custom zoom.
 rangeButtons[4].onclick();await tick(10);
 assert.equal(vm.runInContext('historySeconds',ctx),21600);assert.equal(vm.runInContext('historyCustom',ctx),null);
 const sixHourQuery=queries.at(-1);assert.ok(Math.abs((sixHourQuery.to-sixHourQuery.from)-6*3600)<2);
 assert.equal(rangeButtons[4].attrs['aria-pressed'],'true');
 // A recording shorter than the range starts the chart at its first sample instead of leaving empty space.
 assert.equal(vm.runInContext('historyData.from',ctx),points[0].time);
 assert.ok(Math.abs(vm.runInContext('historyData.to',ctx)-sixHourQuery.to)<1);
 assert.match(get('historyCoverage').textContent,/^Last 6 hours \(recorded 1 minute\)/);
 rangeButtons[0].onclick();await tick(10);
 const fiveMinuteQuery=queries.at(-1);assert.ok(Math.abs((fiveMinuteQuery.to-fiveMinuteQuery.from)-300)<2,'5-minute range requested');
 assert.equal(vm.runInContext('historySeconds',ctx),300);assert.equal(rangeButtons[0].attrs['aria-pressed'],'true');
 assert.equal(vm.runInContext('historyData.from',ctx),points[0].time);
 // One fresh sample still gets a readable minimum span, and is drawn as a dot rather than an invisible line.
 const single=vm.runInContext(`fitHistoryDomain({from:${now-3600},to:${now},points:[{time:${now-2},cpu:5}],events:[]})`,ctx);
 assert.equal(single.to-single.from,30);
 const arcs=[];const dotCtx={beginPath(){},moveTo(){},lineTo(){},stroke(){throw new Error('lone sample must not be stroked')},arc(...a){arcs.push(a)},fill(){}};
 vm.runInContext('drawHistorySeries',ctx)(dotCtx,[{time:1,cpu:5}],null,p=>p.cpu,'#000',t=>t,v=>v,15);assert.equal(arcs.length,1);
 const ticks=vm.runInContext(`historyTicks(${now-300},${now},720)`,ctx);
 assert.ok(ticks.length>=3&&ticks.length<=8&&ticks.every(t=>t.time>=now-300&&t.time<=now),'ticks fit the visible span');
 rangeButtons[4].onclick();await tick(10);

 // Process rows are CPU-sorted DOM nodes, and their nearest-snapshot queries are debounced.
 vm.runInContext(`inspectHistoryTime(${now-10})`,ctx);await tick(230);
 assert.ok(queries.some(q=>q.processTime===now-10),'nearest process snapshot requested');
 assert.deepEqual(get('historyProcesses').children.map(r=>r.children[0].textContent),['busy.exe','low.exe']);
 vm.runInContext("renderHistoryProcesses([{pid:3,name:'<img src=x onerror=alert(1)>',cpu:99,rss:1,cmd:'<script>bad()</script>'}],100)",ctx);
 assert.equal(get('historyProcesses').children[0].children[0].textContent,'<img src=x onerror=alert(1)>','process text is not interpreted as HTML');
 assert.equal(get('historyProcesses').children[0].children.at(-1).textContent,'<script>bad()</script>');
 console.log('UI history process snapshots passed');

 // Keyboard inspection follows samples, while a drag creates a custom zoom range.
 const key=key=>({key,preventDefault(){this.prevented=true}});
 canvas.onkeydown(key('Home'));assert.equal(vm.runInContext('historyCursor',ctx),points[0].time);
 canvas.onkeydown(key('ArrowRight'));assert.equal(vm.runInContext('historyCursor',ctx),points[1].time);
 canvas.onkeydown(key('End'));assert.equal(vm.runInContext('historyCursor',ctx),points.at(-1).time);
 await tick(220);
 const L=canvas._historyLayout,start=L.left+100,end=L.left+260,beforeSpan=vm.runInContext('historyData.to-historyData.from',ctx);
 canvas.onpointerdown({button:0,pointerId:1,clientX:start,preventDefault(){}});
 canvas.onpointermove({pointerId:1,clientX:end});canvas.onpointerup({pointerId:1,clientX:end});await tick(20);
 const custom=vm.runInContext('historyCustom',ctx);assert.ok(custom&&custom.to>custom.from,'drag zoom selected a range');
 assert.ok(Math.abs((custom.to-custom.from)-160/(L.right-L.left)*beforeSpan)<2);
 assert.equal(vm.runInContext('historySeconds',ctx),0);assert.ok(rangeButtons.every(b=>b.attrs['aria-pressed']==='false'));
 console.log('UI history keyboard and drag zoom passed');

 // Exact types, alert/process groups, severity and text all compose locally.
 vm.runInContext("$('eventSearch').value='';$('eventType').value='alerts';$('eventSeverity').value=''",ctx);
 assert.deepEqual(Array.from(vm.runInContext('filteredMonitorEvents().map(e=>e.id)',ctx)),['old-alert','old-cpu']);
 vm.runInContext("$('eventType').value='process_new'",ctx);assert.deepEqual(Array.from(vm.runInContext('filteredMonitorEvents().map(e=>e.id)',ctx)),['old-alert']);
 vm.runInContext("$('eventType').value='';$('eventSeverity').value='warning'",ctx);assert.deepEqual(Array.from(vm.runInContext('filteredMonitorEvents().map(e=>e.id)',ctx)),['old-cpu']);
 vm.runInContext("$('eventSeverity').value='';$('eventSearch').value='EXAMPLE PUBLISHER'",ctx);assert.equal(vm.runInContext('filteredMonitorEvents().length',ctx),2);
 vm.runInContext("$('eventSearch').value='10'",ctx);assert.deepEqual(Array.from(vm.runInContext('filteredMonitorEvents().map(e=>e.id)',ctx)),['old-alert','old-start']);
 vm.runInContext("$('eventSearch').value=''",ctx);
 console.log('UI event filtering passed');

 // Preferences suppress only their own alert category; starts and exits never pop up.
 get('alertFirstRun').checked=false;get('alertFirstRun').onchange();assert.equal(savedPrefs.alertFirstRun,false);
 allEvents.push({id:'suppressed',time:now-5,type:'process_new',severity:'notice',title:'suppressed.exe is new',message:'suppressed',process:process(20,'suppressed.exe')});
 await vm.runInContext('loadMonitorEvents()',ctx);assert.equal(toasts.length,0);
 get('alertFirstRun').checked=true;get('alertFirstRun').onchange();
 allEvents.push({id:'fresh-alert',time:now-4,type:'process_new',severity:'notice',title:'fresh.exe is new',message:'fresh alert',process:process(21,'fresh.exe')});
 await vm.runInContext('loadMonitorEvents()',ctx);assert.equal(toasts.length,1);assert.equal(toasts[0].text,'fresh.exe is new');
 allEvents.push({id:'fresh-start',time:now-3,type:'process_start',severity:'info',title:'fresh.exe started',message:'start',process:process(21,'fresh.exe')});
 await vm.runInContext('loadMonitorEvents()',ctx);assert.equal(toasts.length,1,'process activity is not an alert pop-up');
 console.log('UI alert preferences and new-event pop-ups passed');

 // Event inspection opens a one-hour History range and preserves its selected marker.
 const selected=allEvents.find(e=>e.id==='fresh-alert');vm.runInContext('inspectMonitorEvent',ctx)(selected);await tick(230);
 assert.equal(vm.runInContext('tab',ctx),'history');assert.equal(vm.runInContext('historySelectedEvent.id',ctx),'fresh-alert');
 assert.equal(vm.runInContext('historyCustom.to-historyCustom.from',ctx),3600);assert.equal(vm.runInContext('historyCursor',ctx),selected.time);
 assert.equal(vm.runInContext('nearestHistoryPoint(historyCursor).time',ctx),points.at(-1).time);
 console.log('UI event-to-history inspection passed');

 // Only the newest history and process responses may update their views.
 const deferred=[];historyImpl=q=>{queries.push(q);return new Promise((resolve,reject)=>deferred.push({q,resolve,reject}))};
 const oldRequest=vm.runInContext('loadResourceHistory()',ctx),newRequest=vm.runInContext('loadResourceHistory()',ctx);
 deferred[1].resolve({from:1,to:2,points:[{time:2,cpu:22,memory:33,rx:2,tx:1}],events:[]});await newRequest;
 deferred[0].resolve({from:1,to:2,points:[{time:1,cpu:99,memory:99,rx:0,tx:0}],events:[]});await oldRequest;
 assert.equal(vm.runInContext('historyData.points[0].cpu',ctx),22,'stale history response rejected');
 historyImpl=async q=>({from:q.from,to:q.to,points:[],events:[],processTime:q.processTime,processes:[process(q.processTime===1?1:2,q.processTime===1?'old.exe':'new.exe')]});
 vm.runInContext('historyCursor=2',ctx);
 const p1=vm.runInContext('loadHistoryProcesses(1)',ctx),p2=vm.runInContext('loadHistoryProcesses(2)',ctx);await Promise.all([p1,p2]);
 assert.equal(get('historyProcesses').children[0].children[0].textContent,'new.exe','stale process response rejected');
 console.log('UI stale-response handling passed');

 // Query and clear failures are visible; a successful clear resets all view and alert state.
 historyImpl=async()=>{throw new Error('history read failed')};
 await vm.runInContext('loadResourceHistory()',ctx);assert.equal(get('historyCoverage').textContent,'Could not load history');assert.equal(get('historyStorage').classList.contains('hidden'),false);
 await vm.runInContext('loadMonitorEvents()',ctx);assert.equal(get('eventCount').textContent,'Could not load events');assert.equal(get('eventsStorage').classList.contains('hidden'),false);
 ctx.confirmAction=async()=>({});ctx.clearMonitorHistory=async()=>{throw new Error('clear failed')};
 await get('historyClear').onclick();assert.equal(toasts.at(-1).text,'Error: clear failed');
 let cleared=0;ctx.clearMonitorHistory=async()=>{cleared++};allEvents=[];processList=[];
 historyImpl=async q=>({from:q.from,to:q.to,points:[],events:[],...(q.processTime?{processTime:0,processes:[]}:{})});
 vm.runInContext("$('eventType').value='';$('eventSeverity').value='';$('eventSearch').value=''",ctx);
 await get('historyClear').onclick();assert.equal(cleared,1);assert.equal(vm.runInContext('historySeconds',ctx),3600);assert.equal(vm.runInContext('historyData.points.length',ctx),0);
 assert.equal(vm.runInContext('monitorEvents.length',ctx),0);assert.equal(vm.runInContext('knownEventIDs.size',ctx),0);assert.equal(get('historyEmpty').classList.contains('hidden'),false);
 console.log('UI history failures and clear workflow passed');

 // History follows the monitored computer; each query and clear names it, and switching
 // machines makes the new machine's existing events a baseline rather than pop-ups.
 const server='root@host-01:22';
 ctx.historyMachines=async()=>[{key:'local',local:true,newest:now},{key:server,local:false,newest:now}];
 const serverEvent={id:'srv-cpu',time:now-5,type:'resource_cpu',severity:'warning',title:'Server CPU use is high',message:'high'};
 historyImpl=async q=>{queries.push(q);return q.machine===server?{from:q.from,to:q.to,machine:server,points:[{time:now-5,cpu:77,memory:10,rx:0,tx:0}],events:[serverEvent]}:{from:q.from,to:q.to,points:[],events:[]}};
 const toastsBefore=toasts.length;
 vm.runInContext("setTab('history')",ctx);vm.runInContext(`selectHistoryMachine('${server}')`,ctx);await tick(20);
 assert.equal(vm.runInContext('historyMachine',ctx),server);
 assert.ok(queries.slice(-3).every(q=>q.machine===server),'history and event queries name the server');
 assert.equal(vm.runInContext('historyData.points[0].cpu',ctx),77);
 assert.equal(toasts.length,toastsBefore,'existing server alerts do not pop up');
 await vm.runInContext('loadHistoryMachineList()',ctx);
 assert.deepEqual(get('historyMachine').children.map(o=>o.value),['local',server]);assert.equal(get('eventsMachine').value,server);
 assert.match(get('historySub').textContent,/root@host-01:22/);
 let clearedMachine=null;ctx.clearMonitorHistory=async m=>{clearedMachine=m};
 await get('historyClear').onclick();assert.equal(clearedMachine,server,'clear targets the shown computer');
 get('eventsMachine').value='local';get('eventsMachine').onchange();await tick(20);
 assert.equal(vm.runInContext('historyMachine',ctx),'local');assert.equal(queries.at(-1).machine,'local');
 console.log('UI per-computer history passed');

 // Busy computers: background updates fetch only new events, a hidden list is not rebuilt,
 // the visible list is paged, and the chart asks only for alerts.
 let stored=Array.from({length:300},(_,i)=>({id:'e'+i,time:now-400+i,type:'process_start',severity:'info',title:'p'+i+' started',message:''})).reverse();
 historyImpl=async q=>{queries.push(q);return {from:q.from,to:q.to,points:[],events:stored.filter(e=>e.time>=q.from&&e.time<=q.to&&(!q.eventType||q.eventType!=='alerts'||e.type!=='process_start')).slice(0,q.eventLimit)}};
 vm.runInContext("setTab('activity');selectHistoryMachine('local');eventBaseline=false;monitorEvents=[];loadMonitorEvents(true)",ctx);await tick(20);
 assert.equal(vm.runInContext('monitorEvents.length',ctx),300);
 get('eventList').children=[];
 stored=[{id:'new1',time:now-50,type:'process_exit',severity:'info',title:'p exited',message:''},...stored];
 await vm.runInContext('loadMonitorEvents(true)',ctx);
 assert.equal(queries.at(-1).from,now-101,'background update asks only for events since the newest one');
 assert.equal(vm.runInContext('monitorEvents.length',ctx),301);assert.equal(vm.runInContext('monitorEvents[0].id',ctx),'new1');
 assert.equal(get('eventList').children.length,0,'hidden list is not rebuilt');assert.equal(vm.runInContext('eventsDirty',ctx),true);
 const before=queries.length;await vm.runInContext('loadMonitorEvents(true)',ctx);
 assert.equal(queries.length,before+1);assert.equal(vm.runInContext('monitorEvents.length',ctx),301,'no duplicate events');
 vm.runInContext("tab='events';renderMonitorEvents()",ctx);
 const rows=get('eventList').children;assert.equal(rows.length,201,'first page of rows plus a show-more button');assert.equal(rows.at(-1).id,'eventMore');
 rows.at(-1).onclick();assert.equal(get('eventList').children.length,301,'show more adds the next rows');
 vm.runInContext("$('eventSearch').value='p1';$('eventSearch').oninput();$('eventSearch').value='';$('eventSearch').oninput()",ctx);
 assert.equal(get('eventList').children.length,201,'changing the filter starts again from the first page');
 vm.runInContext("tab='history'",ctx);await vm.runInContext('loadResourceHistory()',ctx);
 assert.equal(queries.at(-1).eventType,'alerts','the chart only fetches alerts');
 console.log('UI busy event streams passed');
})().catch(err=>{console.error(err);process.exitCode=1});

// Storage: Clean up lists what the scan of this computer holds that can be cleared, in place of
// the folder list, and the context menu and the Delete key delete items after asking.
(async()=>{
 const els=new Map();
 function classes(){const values=new Set();return {values,add(...n){n.forEach(x=>values.add(x))},remove(...n){n.forEach(x=>values.delete(x))},toggle(n,on){if(on===undefined)on=!values.has(n);if(on)values.add(n);else values.delete(n);return on},contains(n){return values.has(n)}}}
 function node(tag='div'){
  return {tag,children:[],dataset:{},style:{setProperty(k,v){this[k]=v}},classList:classes(),attrs:{},value:'',checked:false,textContent:'',disabled:false,clientWidth:0,clientHeight:0,
   setAttribute(k,v){this.attrs[k]=String(v)},getAttribute(k){return this.attrs[k]},append(...c){this.children.push(...c)},replaceChildren(...c){this.children=c;this.textContent=''},
   remove(){},addEventListener(){},removeEventListener(){},contains(){return false},focus(){},select(){},click(){if(this.onclick)return this.onclick()},querySelectorAll(){return []},querySelector(){return {focus(){}}},getBoundingClientRect(){return {left:0,top:0,width:100,height:100}}};
 }
 function get(id){if(!els.has(id))els.set(id,node());return els.get(id)}
 const document={getElementById:get,createElement:tag=>node(tag),createElementNS:(ns,tag)=>node(tag),documentElement:node('html'),body:node('body'),addEventListener(){},hidden:false};
 const ctx=vm.createContext({document,window:{addEventListener(){},devicePixelRatio:1},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,performance,innerWidth:1200,innerHeight:800,
  getComputedStyle:()=>({fontFamily:'Segoe UI',getPropertyValue:()=>''})});
 let status={seq:1,finding:true,cleaning:false,items:[]},found=0,cleared=null,deletes=[],asked=[];
 ctx.cleanupStatus=async()=>JSON.parse(JSON.stringify(status));
 ctx.startCleanupFind=async()=>{found++};
 ctx.startCleanup=async ids=>{cleared=ids;status={...status,cleaning:false,cleared:true,freed:300,items:status.items.slice(1)}};
 ctx.startDelete=async req=>{deletes.push(req);return deletes.length};
 ctx.deleteStatus=async()=>({seq:deletes.length,running:false,name:deletes.at(-1).name,freed:5});
 ctx.isElevated=async()=>false;let restarted=0;ctx.restartElevated=async()=>{restarted++};
 ctx.startView=async()=>{};ctx.viewStatus=async()=>({running:false,view:{id:3,trail:[{id:0,name:'C:'},{id:3,name:'me'}],entries:[],size:0}});
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],ctx);
 ctx.confirmAction=async o=>{asked.push(o);return {password:''}};
 const tick=(ms=0)=>new Promise(r=>setTimeout(r,ms));
 await tick(5);
 assert.equal(get('adminTip').classList.contains('hidden'),false,'a window without administrator rights offers to restart with them');
 await get('adminRestart').onclick();assert.equal(restarted,1);assert.match(asked.at(-1).title,/Restart as administrator/);
 vm.runInContext("target='local';scanReady=true;platform='windows';syncFind()",ctx);
 assert.equal(get('cleanButton').classList.contains('hidden'),false,'Clean up is offered for a finished scan of this computer');
 vm.runInContext("target='remote';syncFind()",ctx);
 assert.equal(get('cleanButton').classList.contains('hidden'),true,'and not for a server');
 vm.runInContext("target='local';syncFind()",ctx);
 await get('cleanButton').onclick();await tick(5);
 assert.equal(found,1);assert.equal(get('cleanBar').classList.contains('hidden'),false);
 assert.equal(vm.runInContext('cleanOn()',ctx),true);
 status={seq:1,finding:false,found:true,homeIn:true,items:[
  {id:'projects:node_modules folders',group:'projects',name:'node_modules folders',detail:'2 folders',size:300,count:2,paths:[{path:'C:/Users/me/a/node_modules',size:300}]},
  {id:'models:Ollama models',group:'models',name:'Ollama models',detail:'Models',note:'Pulled again',size:250,count:1,paths:[]},
  {id:'system:Docker build cache',group:'system',name:'Docker build cache',detail:'Layers',size:50,count:1,paths:[{path:'docker builder prune -af',size:50}]}]};
 await vm.runInContext('Clean.status()',ctx);
 const list=()=>get('listing').children[0].children,rows=()=>list().filter(r=>r.dataset&&r.dataset.id);
 assert.equal(list().length,6,'a heading for each group and a row for each item');
 assert.equal(get('cleanGo').disabled,true,'nothing is chosen at first');
 get('cleanAll').onclick();
 assert.equal(get('cleanGoLabel').textContent,'Clear 350 B','Recommended leaves out items with a warning');
 get('cleanNone').onclick();rows()[0].onclick();
 assert.equal(get('cleanGoLabel').textContent,'Clear 300 B');
 const more=rows()[2].children.find(c=>c.attrs&&c.attrs['aria-expanded']==='false');more.onclick({stopPropagation(){}});
 const paths=rows()[2].children.find(c=>c.className==='clean-paths');
 assert.equal(paths.children[0].children.length,2,'no Show button for a docker command');
 await get('cleanGo').onclick();await tick(5);
 assert.deepEqual(Array.from(cleared),['projects:node_modules folders']);assert.match(asked.at(-1).text,/do not go to the Recycle Bin/);
 // A search, or the close button, brings the folder list back.
 get('closeClean').onclick();
 assert.equal(get('cleanBar').classList.contains('hidden'),true);assert.equal(vm.runInContext('cleanOn()',ctx),false);
 // The context menu offers Open with and both ways to delete; deleting asks first.
 vm.runInContext("mode='local'",ctx);
 vm.runInContext("itemMenu({preventDefault(){},stopPropagation(){},clientX:5,clientY:5},{parent:3,name:'big.iso',dir:false,size:5})",ctx);
 const labels=get('menu').children.filter(b=>b.tag==='button').map(b=>b.children.at(-1));
 assert.deepEqual(Array.from(labels),['Open','Open with…','Show in Explorer','Copy path','Move to Recycle Bin','Delete permanently']);
 const perm=get('menu').children.filter(b=>b.tag==='button').at(-1);perm.onclick();await tick(20);
 assert.deepEqual({...deletes[0]},{target:'local',dir:3,name:'big.iso',permanent:true});
 assert.match(asked.at(-1).title,/Delete big.iso for good/);
 // The Delete key moves to the Recycle Bin; Shift+Delete deletes for good.
 let prevented=false;
 assert.equal(vm.runInContext('deleteKey',ctx)({key:'Delete',shiftKey:false,preventDefault(){prevented=true}},{parent:3,name:'old',dir:true,size:9}),true);await tick(20);
 assert.equal(prevented,true);assert.deepEqual({...deletes[1]},{target:'local',dir:3,name:'old',permanent:false});
 assert.match(asked.at(-1).title,/Move old to the Recycle Bin/);
 // Declining deletes nothing.
 ctx.confirmAction=async()=>null;await vm.runInContext("deleteEntry({parent:3,name:'x',size:1},true)",ctx);
 assert.equal(deletes.length,2);
 // Servers get neither.
 vm.runInContext("target='remote';mode='remote'",ctx);
 vm.runInContext("itemMenu({preventDefault(){},stopPropagation(){},clientX:5,clientY:5},{parent:3,name:'big.iso',dir:false,size:5})",ctx);
 assert.equal(get('menu').children.some(b=>b.tag==='button'&&/Delete|Recycle/.test(b.children.at(-1))),false);
 console.log('UI storage clean up and delete passed');
 // Back, Forward and Up beside the map, Alt+Left and Alt+Right, Alt-clicks and the mouse's
 // back and forward buttons.
 vm.runInContext("target='local';tab='storage';storageSessions.local.back=[];storageSessions.local.fwd=[];viewId=1;lastView={id:1,trail:[{id:0,name:'C:'},{id:1,name:'Users'}],entries:[],size:0};refresh=()=>{};syncNav()",ctx);
 assert.equal(get('mapBack').disabled,true);assert.equal(get('mapForward').disabled,true);assert.equal(get('mapUp').disabled,false,'Up is offered below the top folder');
 vm.runInContext('go(5);go(7)',ctx);
 assert.deepEqual(Array.from(vm.runInContext('storageSessions.local.back',ctx)),[1,5]);assert.equal(get('mapBack').disabled,false);
 get('mapBack').onclick();assert.equal(vm.runInContext('viewId',ctx),5);assert.equal(get('mapForward').disabled,false);
 for(const id of ['trust','confirm','appearance','menu'])get(id).classList.add('hidden');
 let stopped=0;const key=(k,extra={})=>vm.runInContext('document.onkeydown',ctx)({key:k,altKey:true,ctrlKey:false,shiftKey:false,metaKey:false,target:{tagName:'INPUT'},preventDefault(){stopped++},...extra});
 key('ArrowLeft');assert.equal(vm.runInContext('viewId',ctx),1,'Alt+Left goes back, even from the search box');
 key('ArrowRight');assert.equal(vm.runInContext('viewId',ctx),5,'Alt+Right goes forward');
 assert.equal(stopped,2,'the browser does not go back a page');
 const click=(button,altKey=false)=>vm.runInContext('storageNavClick',ctx)({button,altKey,preventDefault(){},stopPropagation(){}});
 assert.equal(click(3),'back');assert.equal(click(4),'forward');assert.equal(click(0,true),'back');assert.equal(click(2,true),'forward');
 assert.equal(click(0),false,'a plain click opens folders as before');assert.equal(click(2),false,'a plain right-click opens the menu as before');
 // Opening a new folder after going back drops the forward list.
 vm.runInContext('goBack();go(9)',ctx);
 assert.equal(vm.runInContext('storageSessions.local.fwd.length',ctx),0);assert.equal(get('mapForward').disabled,true);
 // Up goes to the folder above.
 vm.runInContext("lastView={id:9,trail:[{id:0,name:'C:'},{id:4,name:'Users'},{id:9,name:'me'}],entries:[],size:0};viewId=9",ctx);
 get('mapUp').onclick();assert.equal(vm.runInContext('viewId',ctx),4);
 console.log('UI map navigation passed');
})().catch(err=>{console.error(err);process.exitCode=1});
