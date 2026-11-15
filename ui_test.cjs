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

// Map labels keep the size when they can, and opening a program asks first.
(async()=>{
 const reqs=[],statuses=[];let seq=0,confirmed=0;
 const ctx=vm.createContext({document:{getElementById:element},window:{addEventListener(){}},setTimeout,clearTimeout,setInterval:()=>1,clearInterval(){},console,performance,
   startOpen:async r=>{reqs.push(r);return ++seq},openStatus:async()=>statuses.shift(),
   startSearch:async q=>{reqs.push(q);return 7},searchStatus:async()=>({seq:7,result:{hits:[],total:0,totalSize:0}})});
 vm.runInContext(html.split('<script>')[1].split('</script>')[0],ctx);
 const fit=vm.runInContext('fitLabel',ctx),m={measureText:s=>({width:[...s].length*6})};
 assert.equal(fit(m,'Users/',' (120.8 GiB)',200),'Users/ (120.8 GiB)');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',120),'Adminis… (120.8 GiB)','the name shortens before the size goes');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',60),'Administr…','the size goes when the name would not fit');
 assert.equal(fit(m,'Administrator/',' (120.8 GiB)',12),'','nothing when not even two letters fit');

 ctx.confirmAction=async a=>{confirmed++;assert.match(a.title,/^Run setup\.exe\?/);return {}};
 statuses.push({seq:1,running:false,confirm:'run',name:'setup.exe',path:'C:/x/setup.exe'},{seq:2,running:false,name:'setup.exe'});
 const done=await vm.runInContext("openEntry(4,'setup.exe')",ctx);
 assert.equal(confirmed,1);assert.equal(done.name,'setup.exe');
 assert.deepEqual(reqs.map(r=>({...r})),[{dir:4,name:'setup.exe',action:'open'},{dir:4,name:'setup.exe',action:'open',confirmed:true}]);
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
 assert.deepEqual({...reqs[0],exts:[...reqs[0].exts]},{text:'',kind:'videos',exts:['mkv'],sort:'size',limit:1000,types:true,scope:''});

 // A result of a search of every drive opens by its folder's path, and so does its retry.
 reqs.length=0;ctx.confirmAction=async()=>({});
 statuses.push({seq:5,running:false,confirm:'run',name:'tool.exe'},{seq:6,running:false,name:'tool.exe'});
 await vm.runInContext("openEntry(null,'tool.exe','open','D:\\\\Apps')",ctx);
 assert.deepEqual(reqs.map(r=>({...r})),[{dir:0,name:'tool.exe',action:'open',path:'D:\\Apps'},{dir:0,name:'tool.exe',action:'open',path:'D:\\Apps',confirmed:true}]);

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
 for(const hours of [1,6,24,72]){const b=node('button');b.dataset.hours=String(hours);rangeButtons.push(b)}
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
 rangeButtons[1].onclick();await tick(10);
 assert.equal(vm.runInContext('historyHours',ctx),6);assert.equal(vm.runInContext('historyCustom',ctx),null);
 const sixHourQuery=queries.at(-1);assert.ok(Math.abs((sixHourQuery.to-sixHourQuery.from)-6*3600)<2);
 assert.equal(rangeButtons[1].attrs['aria-pressed'],'true');

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
 assert.equal(vm.runInContext('historyHours',ctx),0);assert.ok(rangeButtons.every(b=>b.attrs['aria-pressed']==='false'));
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
 await get('historyClear').onclick();assert.equal(cleared,1);assert.equal(vm.runInContext('historyHours',ctx),1);assert.equal(vm.runInContext('historyData.points.length',ctx),0);
 assert.equal(vm.runInContext('monitorEvents.length',ctx),0);assert.equal(vm.runInContext('knownEventIDs.size',ctx),0);assert.equal(get('historyEmpty').classList.contains('hidden'),false);
 console.log('UI history failures and clear workflow passed');
})().catch(err=>{console.error(err);process.exitCode=1});
