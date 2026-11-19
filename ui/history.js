// ---- Local resource history and monitoring events ----
let historySeconds=3600,historyCustom=null,historyData=null,historyCursor=null,historyHoverTime=null,historySelectedEvent=null;
let historyRequest=0,historyProcessRequest=0,historyProcessTimer=null,historyDrag=null,historyUpdateTimer=null;
let monitorEvents=[],eventBaseline=false,knownEventIDs=new Set(),eventRequest=0;
// historyMachine is the computer whose history and events are shown: "local", or a server's
// user@host:port key. It follows the monitor when it starts on another computer.
let historyMachine='local',historyMachineList=[],monitorMachine=null;
const historyCanvas=$('historyChart'),historyTip=$('historyTip');
const machineName=k=>!k||k==='local'?'this computer':k;
function renderMachinePickers(){
  const keys=[...new Set(['local',historyMachine,...(monitorMachine?[monitorMachine]:[]),...historyMachineList.map(m=>m.key)])];
  for(const id of ['historyMachine','eventsMachine']){
    const s=$(id);if(!s)continue;
    if(typeof document.createElement==='function'){
      clearElement(s);
      for(const k of keys){const o=document.createElement('option');o.value=k;o.textContent=(k==='local'?'This computer':k)+(k===monitorMachine?' · monitoring':'');s.append(o)}
    }
    s.value=historyMachine;
  }
  $('historySub').textContent='Resource use on '+machineName(historyMachine)+', kept for up to 72 hours.';
  $('eventsSub').textContent='Process activity and alerts from monitoring '+machineName(historyMachine)+'.';
}
async function loadHistoryMachineList(){
  if(typeof historyMachines==='function'){try{const list=await historyMachines();historyMachineList=Array.isArray(list)?list:[]}catch(e){}}
  renderMachinePickers();
}
function selectHistoryMachine(key){
  key=key||'local';if(key===historyMachine){renderMachinePickers();return}
  historyMachine=key;historyRequest++;historyProcessRequest++;eventRequest++;clearTimeout(historyProcessTimer);
  historyData=null;historyCursor=null;historyHoverTime=null;historySelectedEvent=null;historyCustom=null;if(!historySeconds)historySeconds=3600;
  // Events already recorded for the newly shown computer set its baseline instead of popping up.
  monitorEvents=[];knownEventIDs=new Set();eventBaseline=false;
  resetHistoryProcesses();syncHistoryRanges();renderMachinePickers();renderMonitorEvents();
  loadMonitorEvents(true);if(tab==='history')loadResourceHistory();
}
for(const id of ['historyMachine','eventsMachine'])if($(id))$(id).onchange=()=>selectHistoryMachine($(id).value);
const historyNow=()=>Date.now()/1000;
const historyAlert=id=>!prefs||prefs.get(id,true)!==false;
function clearElement(e){if(typeof e.replaceChildren==='function')e.replaceChildren();else e.textContent=''}
function historyRange(){
  if(historyCustom)return {...historyCustom};
  const to=historyNow();return {from:to-historySeconds,to};
}
// The smallest span the chart shows, so a single fresh sample is not stretched into a vertical sliver.
const HISTORY_MIN_SPAN=30;
// Live ranges start at the first recorded sample (or event) in the window instead of at the window's edge,
// so a recording that began minutes ago fills the chart rather than hugging its right side.
function fitHistoryDomain(data){
  if(historyCustom)return data;
  const starts=[];
  if(data.points.length)starts.push(data.points[0].time);
  for(const e of data.events)if(e.time>=data.from&&e.time<=data.to)starts.push(e.time);
  if(!starts.length)return data;
  const to=data.to,from=Math.max(data.from,Math.min(Math.min(...starts),to-HISTORY_MIN_SPAN));
  return {...data,from,to};
}
function historySpanLabel(seconds){
  if(seconds<3600){const m=Math.round(seconds/60);return m+(m===1?' minute':' minutes')}
  const h=Math.round(seconds/360)/10;return h+(h===1?' hour':' hours');
}
function syncHistoryRanges(){
  const box=$('historyRanges');if(!box||typeof box.querySelectorAll!=='function')return;
  for(const b of box.querySelectorAll('button[data-seconds]')){
    const active=!historyCustom&&Number(b.dataset.seconds)===historySeconds;
    b.setAttribute('aria-pressed',String(active));
  }
}
function setHistoryEmpty(title,detail,show=true){
  const box=$('historyEmpty');box.classList.toggle('hidden',!show);if(!show)return;
  if(typeof box.querySelector==='function'){
    const h=box.querySelector('h2'),p=box.querySelector('p');if(h)h.textContent=title;if(p)p.textContent=detail;
  }else box.textContent=title+(detail?' — '+detail:'');
}
function setEventEmpty(title,detail){
  const box=$('eventList');clearElement(box);
  if(typeof document.createElement!=='function'){box.textContent=title+(detail?' — '+detail:'');return}
  const empty=el('div','empty event-empty');empty.append(el('h2','',title),el('p','',detail));box.append(empty);
}
function historyStorageWarning(text){
  $('historyStorageText').textContent=text||'';$('historyStorage').classList.toggle('hidden',!text);
}
function eventsStorageWarning(text){
  $('eventsStorageText').textContent=text||'';$('eventsStorage').classList.toggle('hidden',!text);
}
function historyCoverage(data){
  if(!data.points.length)return data.oldest?'No samples in this range':'No samples loaded';
  const requested=historyCustom?'Custom range':'Last '+historySpanLabel(historySeconds);
  const shown=data.to-data.from,fitted=!historyCustom&&shown<historySeconds-60?' (recorded '+historySpanLabel(shown)+')':'';
  return requested+fitted+' · '+count(data.points.length)+' samples · '+new Date(data.points[0].time*1000).toLocaleTimeString()+'–'+new Date(data.points[data.points.length-1].time*1000).toLocaleTimeString();
}
async function loadResourceHistory(background=false){
  if(typeof monitorHistory!=='function'){
    $('historyCoverage').textContent='History is unavailable';setHistoryEmpty('History is unavailable','The local monitoring history service could not be started.');return null;
  }
  const seq=++historyRequest,range=historyRange();
  if(!background||!historyData){$('historyCoverage').textContent='Loading history…';if(!historyData)setHistoryEmpty('Loading local history…','Resource samples will appear here.')}
  try{
    const data=await monitorHistory({...range,machine:historyMachine,points:Math.max(200,Math.min(1600,historyCanvas.clientWidth||900)),eventType:'alerts',eventLimit:1000});
    if(seq!==historyRequest)return null;
    historyData=fitHistoryDomain({...data,points:Array.isArray(data.points)?data.points:[],events:Array.isArray(data.events)?data.events:[]});
    if(historyCursor!==null&&(historyCursor<historyData.from||historyCursor>historyData.to))historyCursor=null;
    if(historySelectedEvent&&(historySelectedEvent.time<historyData.from||historySelectedEvent.time>historyData.to))historySelectedEvent=null;
    $('historyCoverage').textContent=historyCoverage(historyData);$('historyCoverage').title=$('historyCoverage').textContent;
    setHistoryEmpty('No samples in this range',historyData.oldest?'Recording has gaps. Choose another range or inspect a different time.':'Keep monitoring '+machineName(historyMachine)+' to record resource use. History only advances while the app is monitoring it.',!historyData.points.length);
    if(!historyData.points.length)resetHistoryProcesses();
    historyStorageWarning(data.storageWarning||'');
    drawResourceHistory();
    if(historyCursor!==null){updateHistoryInspection(historyCursor);queueHistoryProcesses(historyCursor)}
    return historyData;
  }catch(e){
    if(seq!==historyRequest)return null;
    const message=String(e);$('historyCoverage').textContent='Could not load history';
    if(!historyData||!historyData.points.length)setHistoryEmpty('Could not load history',message);
    historyStorageWarning(message);if(!background||tab==='history')toast(message,{error:true});return null;
  }
}
function nearestHistoryPoint(time){
  const a=historyData&&historyData.points||[];if(!a.length)return null;
  let lo=0,hi=a.length-1;
  while(lo<hi){const mid=Math.floor((lo+hi)/2);if(a[mid].time<time)lo=mid+1;else hi=mid}
  if(lo>0&&Math.abs(a[lo-1].time-time)<=Math.abs(a[lo].time-time))return a[lo-1];
  return a[lo];
}
function historyGap(){
  const a=historyData&&historyData.points||[];if(a.length<3)return 15;
  const deltas=[];for(let i=1;i<a.length;i++){const d=a[i].time-a[i-1].time;if(d>0)deltas.push(d)}
  deltas.sort((x,y)=>x-y);const median=deltas.length?deltas[Math.floor(deltas.length/2)]:1;
  return Math.max(15,median*3);
}
function historyEventsNear(time){
  if(!historyData)return [];
  const tolerance=Math.max(1,(historyData.to-historyData.from)/150);
  return historyData.events.filter(e=>Math.abs(e.time-time)<=tolerance).slice(0,3);
}
function historyReadoutText(time){
  const p=nearestHistoryPoint(time),when=new Date(time*1000).toLocaleString();
  if(!p)return when+' · No resource sample';
  const gpu=p.gpu===null||p.gpu===undefined?'no GPU data':pct0(p.gpu);
  const events=historyEventsNear(time),suffix=events.length?' · '+events.map(e=>e.title).join(' · '):'';
  return when+' · CPU '+pct0(p.cpu)+' · Memory '+pct0(p.memory)+' · GPU '+gpu+' · In '+rate(p.rx)+' · Out '+rate(p.tx)+suffix;
}
function updateHistoryInspection(time){
  $('historyReadout').textContent=historyReadoutText(time);
}
function inspectHistoryTime(time,tip=false,x=0){
  if(!historyData)return;
  time=Math.max(historyData.from,Math.min(historyData.to,time));
  historyCursor=time;historySelectedEvent=null;updateHistoryInspection(time);queueHistoryProcesses(time);
  if(tip)showHistoryTip(time,x);drawResourceHistory();
}
function showHistoryTip(time,x){
  historyTip.textContent=historyReadoutText(time);
  const rect=historyCanvas.getBoundingClientRect(),width=rect.width||historyCanvas.clientWidth||600;
  historyTip.style.left=Math.max(90,Math.min(width-90,x))+'px';historyTip.style.top='34px';historyTip.classList.remove('hidden');
}
function queueHistoryProcesses(time){
  historyProcessRequest++;
  clearTimeout(historyProcessTimer);
  historyProcessTimer=setTimeout(()=>loadHistoryProcesses(time),180);
}
async function loadHistoryProcesses(time){
  if(typeof monitorHistory!=='function'||!historyData)return;
  const seq=++historyProcessRequest,range=historyRange();
  $('historyProcessTime').textContent='Finding the nearest process snapshot…';
  try{
    const data=await monitorHistory({...range,machine:historyMachine,points:1,processTime:time,eventType:'',eventLimit:1});
    if(seq!==historyProcessRequest||time!==historyCursor)return;
    if(!data.processTime){renderHistoryProcesses([],null);return}
    renderHistoryProcesses(Array.isArray(data.processes)?data.processes:[],data.processTime);
  }catch(e){
    if(seq!==historyProcessRequest)return;
    $('historyProcessTime').textContent='Could not load the process snapshot.';clearElement($('historyProcesses'));
  }
}
function resetHistoryProcesses(){
  historyProcessRequest++;clearTimeout(historyProcessTimer);clearElement($('historyProcesses'));
  $('historyProcessTime').textContent='Hover over the timeline to see the busiest processes at that time. Snapshots are taken every 10 seconds.';
}
function renderHistoryProcesses(processes,time){
  const list=$('historyProcesses');clearElement(list);
  if(!time){
    $('historyProcessTime').textContent='No process snapshot within 30 seconds of this time.';
    if(typeof document.createElement==='function')list.append(el('p','hint','Hover over a recorded sample to find its nearest snapshot.'));
    return;
  }
  $('historyProcessTime').textContent='Nearest snapshot: '+new Date(time*1000).toLocaleString();
  const sorted=[...processes].sort((a,b)=>(b.cpu||0)-(a.cpu||0)||(b.rss||0)-(a.rss||0)||String(a.name).localeCompare(String(b.name)));
  if(!sorted.length){list.append(el('p','hint','The snapshot contains no process details.'));return}
  for(const p of sorted){
    const row=el('div','history-process'),name=el('span','pname',p.name||'Unknown process'),cpu=el('span','pcpu',(Number(p.cpu)||0).toFixed(1)+'%');
    name.title=p.cmd||p.name||'';row.append(name,cpu);
    const meta=['PID '+p.pid,p.user||'',size(p.rss),p.mem===undefined?'':pct0(p.mem)+' of memory',p.publisher||'',p.signed===false?'Unverified signature':''].filter(Boolean);
    row.append(el('div','pmeta',meta.join(' · ')));
    if(p.cmd)row.append(el('div','ppath',p.cmd));
    list.append(row);
  }
}
function historyChartLayout(W,H){
  const left=66,right=Math.max(left+1,W-14),top=20,bottom=Math.max(top+4,H-27),gap=9,laneH=(bottom-top-gap*3)/4;
  return {left,right,top,bottom,gap,laneH,lanes:[0,1,2,3].map(i=>({y:top+i*(laneH+gap),h:laneH}))};
}
function historyNiceMax(value){
  if(!isFinite(value)||value<=0)return 1;
  const power=10**Math.floor(Math.log10(value)),scaled=value/power;
  return (scaled<=1?1:scaled<=2?2:scaled<=5?5:10)*power;
}
function drawHistorySeries(ctx,points,lane,get,color,x,y,gap){
  // Segments break at gaps; a segment with a single sample is drawn as a dot so it stays visible.
  let segment=0,previous=null,lone=null;
  const dot=p=>{ctx.fillStyle=color;ctx.beginPath();ctx.arc(p[0],p[1],2.2,0,Math.PI*2);ctx.fill()};
  const end=()=>{if(segment>1)ctx.stroke();else if(segment===1)dot(lone);segment=0;ctx.beginPath()};
  ctx.strokeStyle=color;ctx.lineWidth=1.45;ctx.lineJoin='round';ctx.lineCap='round';ctx.beginPath();
  for(const p of points){
    const value=get(p),missing=value===null||value===undefined||!isFinite(value);
    if(missing||previous!==null&&p.time-previous>gap)end();
    previous=p.time;if(missing)continue;
    const px=x(p.time),py=y(value);
    if(!segment){ctx.moveTo(px,py);lone=[px,py]}else ctx.lineTo(px,py);
    segment++;
  }
  end();
}
// Tick labels adapt to both the visible span and the chart width.
function historyTicks(from,to,width){
  const span=to-from,count=Math.max(2,Math.min(8,Math.floor(width/120)+1)),steps=[5,10,15,30,60,120,300,600,900,1800,3600,7200,10800,21600,43200,86400];
  const step=steps.find(s=>span/s<=count-1)||Math.ceil(span/(count-1)/86400)*86400;
  const offset=new Date().getTimezoneOffset()*60,ticks=[];
  for(let t=Math.ceil((from-offset)/step)*step+offset;t<=to;t+=step)ticks.push(t);
  const fmt=t=>{
    const d=new Date(t*1000);
    if(span<=15*60)return d.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit',second:'2-digit'});
    if(span<=24*3600)return d.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
    return d.toLocaleDateString([],{weekday:'short'})+' '+d.toLocaleTimeString([],{hour:'2-digit',minute:'2-digit'});
  };
  return ticks.map(t=>({time:t,label:fmt(t)}));
}
function drawResourceHistory(){
  const c=historyCanvas;if(!c||typeof c.getContext!=='function'||!historyData)return;
  const W=c.clientWidth,H=c.clientHeight;if(W<120||H<150)return;
  const dpr=Math.min(2,window.devicePixelRatio||1),st=getComputedStyle(document.documentElement),cv=name=>st.getPropertyValue(name).trim();
  if(c.width!==Math.round(W*dpr)||c.height!==Math.round(H*dpr)){c.width=Math.round(W*dpr);c.height=Math.round(H*dpr)}
  const ctx=c.getContext('2d');ctx.setTransform(dpr,0,0,dpr,0,0);ctx.clearRect(0,0,W,H);
  const L=historyChartLayout(W,H),points=historyData.points,span=Math.max(.001,historyData.to-historyData.from),x=t=>L.left+(t-historyData.from)/span*(L.right-L.left),gap=historyGap();
  const font=getComputedStyle(c).fontFamily||'sans-serif',colors={text:cv('--text'),muted:cv('--text-muted'),faint:cv('--text-faint'),border:cv('--border'),line:cv('--chart-1'),second:cv('--chart-2'),accent:cv('--accent'),warning:cv('--warning-foreground'),error:cv('--error-foreground')};
  ctx.font='500 10.5px '+font;ctx.textBaseline='alphabetic';
  for(let i=1;i<points.length;i++){
    const p=points[i];if(p.time-points[i-1].time>gap){
      ctx.strokeStyle=colors.border;ctx.setLineDash([2,4]);ctx.beginPath();ctx.moveTo(x(p.time),L.top);ctx.lineTo(x(p.time),L.bottom);ctx.stroke();ctx.setLineDash([]);
    }
  }
  const netMax=historyNiceMax(Math.max(1,...points.map(p=>Math.max(p.rx||0,p.tx||0))));
  const configs=[
    {label:'CPU',max:100,get:p=>p.cpu,color:colors.line,unit:'100%'},
    {label:'Memory',max:100,get:p=>p.memory,color:colors.line,unit:'100%'},
    {label:'GPU',max:100,get:p=>p.gpu,color:colors.line,unit:'100%'},
    {label:'Network',max:netMax,get:p=>p.rx,color:colors.line,second:p=>p.tx,unit:rate(netMax)}
  ];
  configs.forEach((cfg,i)=>{
    const lane=L.lanes[i],top=lane.y+18,bottom=lane.y+lane.h,h=Math.max(1,bottom-top),y=v=>bottom-Math.max(0,Math.min(1,v/cfg.max))*h;
    ctx.strokeStyle=colors.border;ctx.lineWidth=1;ctx.setLineDash([2,4]);ctx.beginPath();ctx.moveTo(L.left,Math.round((top+bottom)/2)+.5);ctx.lineTo(L.right,Math.round((top+bottom)/2)+.5);ctx.stroke();ctx.setLineDash([]);
    ctx.fillStyle=colors.text;ctx.font='600 11px '+font;ctx.textAlign='left';ctx.fillText(cfg.label,9,lane.y+12);
    // The scale sits under the label, so long labels such as Memory and Network never overlap it.
    ctx.fillStyle=colors.faint;ctx.font='500 10px '+font;ctx.fillText(cfg.unit,9,lane.y+25);
    drawHistorySeries(ctx,points,lane,cfg.get,cfg.color,x,y,gap);
    if(cfg.second)drawHistorySeries(ctx,points,lane,cfg.second,colors.second,x,y,gap);
  });
  // Events stay visible even when their resource series is flat. The selected event gets a full-height rule.
  for(const e of historyData.events){
    if(e.time<historyData.from||e.time>historyData.to)continue;
    const ex=x(e.time),color=e.severity==='warning'?colors.warning:e.severity==='notice'?colors.accent:colors.faint;
    ctx.fillStyle=color;ctx.beginPath();ctx.moveTo(ex,5);ctx.lineTo(ex+3.5,9);ctx.lineTo(ex,13);ctx.lineTo(ex-3.5,9);ctx.closePath();ctx.fill();
  }
  const selected=historySelectedEvent;
  if(selected){ctx.strokeStyle=colors.warning;ctx.lineWidth=1.5;ctx.beginPath();ctx.moveTo(x(selected.time)+.5,L.top);ctx.lineTo(x(selected.time)+.5,L.bottom);ctx.stroke()}
  const cursor=historyHoverTime??historyCursor;
  if(cursor!==null&&cursor>=historyData.from&&cursor<=historyData.to){
    const cx=x(cursor);ctx.strokeStyle=colors.faint;ctx.lineWidth=1;ctx.beginPath();ctx.moveTo(cx+.5,L.top);ctx.lineTo(cx+.5,L.bottom);ctx.stroke();
  }
  if(historyDrag&&historyDrag.moved){
    const a=x(Math.min(historyDrag.startTime,historyDrag.currentTime)),b=x(Math.max(historyDrag.startTime,historyDrag.currentTime));
    ctx.fillStyle=withAlpha(colors.accent,.14);ctx.fillRect(a,L.top,Math.max(1,b-a),L.bottom-L.top);ctx.strokeStyle=colors.accent;ctx.strokeRect(a+.5,L.top+.5,Math.max(1,b-a),L.bottom-L.top-1);
  }
  ctx.font='500 10px '+font;ctx.fillStyle=colors.faint;ctx.textBaseline='alphabetic';
  let lastRight=-Infinity;
  for(const t of historyTicks(historyData.from,historyData.to,L.right-L.left)){
    const tx=x(t.time),w=ctx.measureText(t.label).width,left=Math.max(L.left,Math.min(L.right-w,tx-w/2));
    if(left<lastRight+12)continue;
    ctx.strokeStyle=colors.border;ctx.lineWidth=1;ctx.beginPath();ctx.moveTo(Math.round(tx)+.5,L.bottom);ctx.lineTo(Math.round(tx)+.5,L.bottom+4);ctx.stroke();
    ctx.textAlign='left';ctx.fillText(t.label,left,H-8);lastRight=left+w;
  }
  c._historyLayout=L;
}
function historyPointerTime(e){
  const r=historyCanvas.getBoundingClientRect(),L=historyCanvas._historyLayout||historyChartLayout(r.width,r.height);
  return {time:historyData.from+Math.max(0,Math.min(1,(e.clientX-r.left-L.left)/(L.right-L.left)))*(historyData.to-historyData.from),x:e.clientX-r.left};
}
if(historyCanvas){
  historyCanvas.onpointerdown=e=>{
    if(!historyData||!historyData.points.length||e.button!==0)return;
    const p=historyPointerTime(e);historyDrag={startTime:p.time,currentTime:p.time,startX:p.x,moved:false,pointerId:e.pointerId};
    historyHoverTime=p.time;historyCanvas.focus();if(historyCanvas.setPointerCapture)historyCanvas.setPointerCapture(e.pointerId);e.preventDefault();
  };
  historyCanvas.onpointermove=e=>{
    if(!historyData||!historyData.points.length)return;
    const p=historyPointerTime(e);historyHoverTime=p.time;
    if(historyDrag){
      historyDrag.currentTime=p.time;historyDrag.moved=historyDrag.moved||Math.abs(p.x-historyDrag.startX)>5;
      if(historyDrag.moved){const a=new Date(Math.min(historyDrag.startTime,p.time)*1000).toLocaleTimeString(),b=new Date(Math.max(historyDrag.startTime,p.time)*1000).toLocaleTimeString();$('historyReadout').textContent='Zoom from '+a+' to '+b;historyTip.classList.add('hidden');drawResourceHistory();return}
    }
    inspectHistoryTime(p.time,true,p.x);
  };
  historyCanvas.onpointerup=e=>{
    if(!historyDrag)return;
    const drag=historyDrag,p=historyPointerTime(e);historyDrag=null;historyHoverTime=null;historyTip.classList.add('hidden');
    if(drag.moved&&Math.abs(drag.startTime-p.time)>=1){
      historyCustom={from:Math.min(drag.startTime,p.time),to:Math.max(drag.startTime,p.time)};historySeconds=0;historyCursor=null;historySelectedEvent=null;resetHistoryProcesses();syncHistoryRanges();loadResourceHistory();
    }else inspectHistoryTime(p.time);
    if(historyCanvas.releasePointerCapture&&historyCanvas.hasPointerCapture&&historyCanvas.hasPointerCapture(e.pointerId))historyCanvas.releasePointerCapture(e.pointerId);
  };
  historyCanvas.onpointercancel=()=>{historyDrag=null;historyHoverTime=null;historyTip.classList.add('hidden');drawResourceHistory()};
  historyCanvas.onpointerleave=()=>{if(!historyDrag){historyHoverTime=null;historyTip.classList.add('hidden');drawResourceHistory()}};
  historyCanvas.onkeydown=e=>{
    const points=historyData&&historyData.points||[];if(!points.length||!['ArrowLeft','ArrowRight','Home','End'].includes(e.key))return;
    e.preventDefault();const base=historyCursor??historySelectedEvent?.time??points[points.length-1].time,current=nearestHistoryPoint(base);
    let index=points.indexOf(current);if(e.key==='Home')index=0;else if(e.key==='End')index=points.length-1;else index=Math.max(0,Math.min(points.length-1,index+(e.key==='ArrowRight'?1:-1)));
    historyHoverTime=null;inspectHistoryTime(points[index].time);
  };
}
if($('historyRanges')&&typeof $('historyRanges').querySelectorAll==='function')for(const b of $('historyRanges').querySelectorAll('button[data-seconds]'))b.onclick=()=>{
  historySeconds=Number(b.dataset.seconds);historyCustom=null;historyCursor=null;historySelectedEvent=null;historyHoverTime=null;resetHistoryProcesses();syncHistoryRanges();loadResourceHistory();
};
$('historyRefresh').onclick=()=>loadResourceHistory();
$('historyClear').onclick=async()=>{
  if(typeof clearMonitorHistory!=='function'){toast('History is unavailable.',{error:true});return}
  const machine=historyMachine,name=machineName(machine);
  const ok=await confirmAction({title:'Clear the monitoring history of '+name+'?',text:'All resource samples, process snapshots and events recorded for '+name+' are permanently deleted. Other computers keep their history. Monitoring continues recording new activity.',label:'Clear history'});if(!ok)return;
  $('historyClear').disabled=true;
  try{
    await clearMonitorHistory(machine);if(machine!==historyMachine)return;
    historyRequest++;historyProcessRequest++;eventRequest++;clearTimeout(historyProcessTimer);
    historyData=null;historyCursor=null;historyHoverTime=null;historySelectedEvent=null;historyCustom=null;historySeconds=3600;monitorEvents=[];knownEventIDs=new Set();eventBaseline=false;
    syncHistoryRanges();clearElement($('historyProcesses'));$('historyProcessTime').textContent='Hover over the timeline to see the busiest processes at that time. Snapshots are taken every 10 seconds.';renderMonitorEvents();
    toast('Monitoring history of '+name+' cleared.');await Promise.all([loadResourceHistory(),loadMonitorEvents()]);
  }catch(e){const message=String(e);historyStorageWarning(message);toast(message,{error:true})}
  finally{$('historyClear').disabled=false}
};
function eventGroup(kind){return kind==='process_new'||kind==='process_unsigned'||String(kind).startsWith('resource_')?'alerts':String(kind).startsWith('process_')?'processes':'other'}
function filteredMonitorEvents(){
  const query=$('eventSearch').value.trim().toLowerCase(),type=$('eventType').value,severity=$('eventSeverity').value;
  return monitorEvents.filter(e=>{
    if(type&&e.type!==type&&eventGroup(e.type)!==type)return false;
    if(severity&&e.severity!==severity)return false;
    if(!query)return true;
    const p=e.process||{},haystack=[e.title,e.message,e.type,p.name,p.cmd,p.user,p.publisher,p.pid].filter(v=>v!==undefined&&v!==null).join(' ').toLowerCase();
    return haystack.includes(query);
  });
}
function eventTypeLabel(e){
  return {process_start:'Process start',process_exit:'Process exit',process_new:'First run',process_unsigned:'Signature alert',resource_cpu:'CPU alert',resource_memory:'Memory alert',resource_gpu:'GPU alert'}[e.type]||e.type;
}
// The list shows EVENT_PAGE rows at first and more on request; thousands of rows at once make
// every update slow.
const EVENT_PAGE=200;
let eventRowLimit=EVENT_PAGE;
function renderMonitorEvents(){
  eventsDirty=false;
  const list=$('eventList'),events=filteredMonitorEvents();clearElement(list);
  $('eventCount').textContent=events.length===monitorEvents.length?count(events.length)+' events':count(events.length)+' of '+count(monitorEvents.length)+' events';
  if(!events.length){setEventEmpty(monitorEvents.length?'No matching events':'No events yet',monitorEvents.length?'Change the search or filters to see more events.':'Process starts, exits and alert events are recorded while monitoring runs.');return}
  if(typeof document.createElement!=='function')return;
  const today=new Date().toDateString();
  for(const e of events.slice(0,eventRowLimit)){
    const severity=e.severity||'info',at=new Date(e.time*1000);
    const row=el('button','event-row sev-'+severity);row.type='button';row.setAttribute('aria-label',e.title+', '+at.toLocaleString());row.onclick=()=>inspectMonitorEvent(e);
    // Today's events show the time alone; older ones the date too, with the full time on hover.
    const when=el('div','event-when',at.toDateString()===today?at.toLocaleTimeString():at.toLocaleString(undefined,{month:'short',day:'numeric',hour:'numeric',minute:'2-digit'}));
    when.title=at.toLocaleString();when.append(el('span','',eventTypeLabel(e)));
    // Routine events carry no severity label; their kind is in the left column.
    const body=el('div','event-body'),title=el('div','event-title',e.title);if(severity!=='info')title.append(el('span','severity sev-'+severity,severity));
    body.append(title);
    // A process start or exit message only repeats the title and PID, so it is left out.
    if(e.message&&e.type!=='process_start'&&e.type!=='process_exit')body.append(el('div','event-message',e.message));
    const p=e.process||{},details=[p.pid!==undefined?'PID '+p.pid:'',p.user||'',p.publisher||'',e.metric?e.metric+' '+pct0(e.value)+' (threshold '+pct0(e.threshold)+')':''].filter(Boolean);
    if(details.length||p.cmd){
      const meta=el('div','event-meta');
      if(details.length)meta.append(el('span','event-details',details.join(' · ')));
      if(p.cmd){const path=el('span','event-path',p.cmd);path.title=p.cmd;meta.append(path)}
      body.append(meta);
    }
    row.append(when,body);list.append(row);
  }
  if(events.length>eventRowLimit){
    const more=el('button','btn sm ghost event-more','Show '+count(Math.min(EVENT_PAGE,events.length-eventRowLimit))+' more · '+count(events.length-eventRowLimit)+' not shown');
    more.type='button';more.id='eventMore';more.onclick=()=>{eventRowLimit+=EVENT_PAGE;renderMonitorEvents()};list.append(more);
  }
}
function alertAllowed(e){
  if(e.type==='process_new')return historyAlert('alertFirstRun');
  if(e.type==='process_unsigned')return historyAlert('alertUnsigned');
  if(String(e.type).startsWith('resource_'))return historyAlert('alertResource');
  return false;
}
function announceMonitorEvents(events){
  const alerts=events.filter(e=>eventGroup(e.type)==='alerts'&&alertAllowed(e));if(!alerts.length)return;
  const latest=alerts[0],more=alerts.length-1;
  toast(latest.title,{sub:latest.message+(more?' · '+(more+1)+' new alerts are in the Events tab.':'')});
}
// Busy computers record several process events a second, so background updates only fetch
// events since the newest one already loaded, and the list is rebuilt only while it is visible.
const MAX_MONITOR_EVENTS=5000;
let eventsDirty=false;
async function loadMonitorEvents(background=false){
  if(typeof monitorHistory!=='function'){
    $('eventCount').textContent='Events unavailable';setEventEmpty('Events are unavailable','The local monitoring history service could not be started.');return null;
  }
  const seq=++eventRequest,to=historyNow(),incremental=background&&eventBaseline&&monitorEvents.length>0;
  // The newest event's own second is fetched again; events already known are skipped by ID.
  const from=incremental?monitorEvents[0].time:to-72*3600;
  if(!monitorEvents.length&&!background){$('eventCount').textContent='Loading events…';setEventEmpty('Loading events…','Process activity and alerts will appear here.')}
  try{
    const data=await monitorHistory({from,to,machine:historyMachine,points:1,eventType:'',eventLimit:MAX_MONITOR_EVENTS});
    if(seq!==eventRequest)return null;
    const events=Array.isArray(data.events)?data.events:[],fresh=eventBaseline?events.filter(e=>!knownEventIDs.has(e.id)):[];
    eventsStorageWarning(data.storageWarning||'');
    if(incremental){
      if(!fresh.length)return monitorEvents;
      const cutoff=to-72*3600;
      monitorEvents=fresh.concat(monitorEvents).filter(e=>e.time>=cutoff).slice(0,MAX_MONITOR_EVENTS);
    }else monitorEvents=events;
    knownEventIDs=new Set(monitorEvents.map(e=>e.id));const announce=eventBaseline;eventBaseline=true;
    if(tab==='events'||!background)renderMonitorEvents();else eventsDirty=true;
    if(announce)announceMonitorEvents(fresh);return monitorEvents;
  }catch(e){
    if(seq!==eventRequest)return null;
    const message=String(e);eventsStorageWarning(message);$('eventCount').textContent='Could not load events';
    if(!monitorEvents.length)setEventEmpty('Could not load events',message);if(!background||tab==='events')toast(message,{error:true});return null;
  }
}
function inspectMonitorEvent(e){
  historySelectedEvent=e;historyCursor=e.time;historyHoverTime=null;historySeconds=0;historyCustom={from:e.time-1800,to:e.time+1800};
  clearTimeout(historyProcessTimer);syncHistoryRanges();setTab('history');
}
for(const [id] of [['alertFirstRun'],['alertUnsigned'],['alertResource']]){
  const box=$(id);box.checked=historyAlert(id);box.onchange=()=>{if(prefs)prefs.set(id,box.checked)};
}
// Changing the filters starts again from the first page of rows.
const refilterEvents=()=>{eventRowLimit=EVENT_PAGE;renderMonitorEvents()};
$('eventSearch').oninput=refilterEvents;$('eventType').onchange=refilterEvents;$('eventSeverity').onchange=refilterEvents;
$('eventsRefresh').onclick=()=>loadMonitorEvents();
function startHistoryUpdates(){
  if(historyUpdateTimer!==null||typeof setInterval!=='function'||typeof monitorHistory!=='function')return;
  loadHistoryMachineList();loadMonitorEvents(true);
  historyUpdateTimer=setInterval(()=>{loadMonitorEvents(true);if(tab==='history')loadResourceHistory(true)},5000);
}
syncHistoryRanges();
window.addEventListener('resize',()=>{clearTimeout(resizeTimer);resizeTimer=setTimeout(()=>{redraw();drawSparks();drawResourceHistory()},150)});

