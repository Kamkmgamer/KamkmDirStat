const fs = require('node:fs');
const vm = require('node:vm');
const assert = require('node:assert/strict');
const html = fs.readFileSync('index.html', 'utf8');
const elements = new Map();
function element(id) {
  if (!elements.has(id)) elements.set(id, {value:'', textContent:'', classList:{values:new Set(),toggle(name,on){if(on)this.values.add(name);else this.values.delete(name)},add(name){this.values.add(name)},remove(name){this.values.delete(name)}}});
  return elements.get(id);
}
const context = vm.createContext({document:{getElementById:element}, window:{addEventListener(){}}, setTimeout, clearTimeout, console});
vm.runInContext(html.split('<script>')[1].split('</script>')[0], context);
assert.equal(vm.runInContext('target',context),'local','This PC is the default');
vm.runInContext("setTarget('remote')",context);assert.equal(element('connect').classList.values.has('is-local'),false);assert.equal(element('host').required,true);
element('method').value='auto'; element('host').value='buildbox'; element('password').value='should-not-be-sent'; element('path').value='/';
let c=vm.runInContext('connection()',context);
assert.equal(c.serverReport,true);assert.equal(c.method,'auto'); assert.equal(c.host,'buildbox'); assert.equal(c.password,'');
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
 element('wantScan').checked=false;element('wantMonitor').checked=true;vm.runInContext('syncTools()',context);
 assert.equal(element('submitLabel').textContent,'Connect and monitor');assert.equal(element('scanFields').classList.values.has('hidden'),true);assert.equal(element('path').required,false);
 element('wantScan').checked=true;vm.runInContext('syncTools()',context);assert.equal(element('submitLabel').textContent,'Connect and open');
 assert.equal(canEnd(theirs,'admin'),true);assert.equal(canFreeCache('admin'),true);assert.equal(needsSudoPassword(theirs,'admin'),false);
 // This PC: drives that are ticked, or one folder that replaces them.
 vm.runInContext("setTarget('local')",context);
 assert.equal(element('connect').classList.values.has('is-local'),true);assert.equal(element('host').required,false);assert.equal(element('path').required,false);
 vm.runInContext("drives=[{path:'C:\\\\',checked:true},{path:'D:\\\\',checked:false},{path:'E:\\\\',checked:true}]",context);
 element('localFolder').value='';let lc=vm.runInContext('connection()',context);
 assert.equal(lc.local,true);assert.deepEqual([...lc.paths],['C:\\','E:\\']);assert.equal(lc.password,undefined);
 element('localFolder').value='  D:\\Games  ';lc=vm.runInContext('connection()',context);assert.deepEqual([...lc.paths],['D:\\Games']);
 element('wantScan').checked=true;element('wantMonitor').checked=true;vm.runInContext('syncTools()',context);assert.equal(element('submitLabel').textContent,'Scan and monitor');
 for(const [p,name,hint] of [['darwin','This Mac','/Users/you/Downloads'],['linux','This computer','/home/you/Downloads'],['windows','This PC','C:\\Users\\you\\Downloads']]){
  vm.runInContext(`platform='${p}';applyPlatform()`,context);
  assert.equal(element('localTargetName').textContent,name);assert.equal(element('localFolder').placeholder,hint);
 }
 console.log('UI activity monitor rules passed');
}
