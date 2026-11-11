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
