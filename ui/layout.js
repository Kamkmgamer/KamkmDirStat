// ---- Activity layout ----
// The activity sections sit in a grid of 12 columns and rows of DASH_ROW pixels. In edit mode
// each section can be dragged to a new place, resized from its bottom-right corner to cover any
// number of columns and rows, or hidden. The layout is saved with the other settings. Dragging
// and resizing move transforms only, and the grid is laid out again only when a section changes
// place or size, so it stays smooth on slow machines.
const CARD_INFO={'tile-cpu':'CPU usage','tile-mem':'Memory usage','tile-gpu':'GPU usage','tile-net':'Network traffic',cpu:'Processor',mem:'Memory',gpu:'Graphics',net:'Network interfaces',procs:'Processes'};
const DASH_ROW=44,DASH_GAP=12,DASH_MAX_ROWS=24;
const Dash=(function(){
  const dash=$('dash'),off={apply(){},spans(){},available(){},escape:()=>false,editing:()=>false};
  if(!dash||typeof dash.querySelectorAll!=='function')return off;
  const scroller=$('activity'),cards=new Map([...dash.querySelectorAll('[data-card]')].map(c=>[c.dataset.card,c]));
  const defaultOrder=[...cards.keys()];
  const kind=id=>id.startsWith('tile-')?'tile':id==='procs'?'procs':'panel';
  const MIN={tile:{w:2,h:2},panel:{w:3,h:3},procs:{w:4,h:4}};
  const noGpu=()=>$('actBody').classList.contains('no-gpu');
  // Default sizes fill each row, with or without a GPU.
  function defaultSize(id){
    const g=noGpu();
    return {'tile-cpu':{w:g?4:3,h:3},'tile-mem':{w:g?4:3,h:3},'tile-gpu':{w:3,h:3},'tile-net':{w:g?4:3,h:3},
      cpu:{w:6,h:6},mem:{w:6,h:6},gpu:{w:6,h:5},net:{w:g?12:6,h:5},procs:{w:12,h:9}}[id];
  }
  let state=load(),editing=false,pending=null,drag=null,resize=null,rects=[],narrow=false;
  const size=id=>state.sizes[id]||defaultSize(id);
  // On a narrow view each section takes twice its columns, so two quarter-width cards share a row.
  const cols=id=>narrow?Math.min(12,size(id).w*2):size(id).w;

  function load(){
    const saved=prefs&&prefs.get('activityLayout'),order=[],hidden=new Set(),sizes={};
    if(saved&&typeof saved==='object'){
      if(Array.isArray(saved.order))for(const id of saved.order)if(cards.has(id)&&!order.includes(id))order.push(id);
      if(Array.isArray(saved.hidden))for(const id of saved.hidden)if(cards.has(id))hidden.add(id);
      // Version 1 stored named widths, which are not kept.
      if(saved.v===2&&saved.sizes&&typeof saved.sizes==='object')for(const id in saved.sizes){
        const s=saved.sizes[id],m=cards.has(id)&&MIN[kind(id)];
        if(m&&s&&Number.isInteger(s.w)&&Number.isInteger(s.h))sizes[id]={w:Math.max(m.w,Math.min(12,s.w)),h:Math.max(m.h,Math.min(DASH_MAX_ROWS,s.h))};
      }
    }
    // Sections added in a later version go near where they are by default.
    defaultOrder.forEach((id,i)=>{if(!order.includes(id))order.splice(Math.min(i,order.length),0,id)});
    return {order,hidden,sizes};
  }
  function save(){if(prefs)prefs.set('activityLayout',{v:2,order:state.order,hidden:[...state.hidden],sizes:state.sizes})}
  const shown=c=>!c.classList.contains('user-hidden')&&!(c.classList.contains('gpu-only')&&noGpu());

  // ---- Working out the layout ----
  // The grid puts each section, in order, as far up and then as far left as it fits. layout() does
  // the same with a 12-bit mask per row, so the code below can try a change and keep it only if it
  // works, without laying out the page. The sections are few, so this costs next to nothing.
  const visible=()=>state.order.filter(id=>shown(cards.get(id)));
  const sizesOf=ids=>Object.fromEntries(ids.map(id=>[id,{...size(id)}]));
  const copy=S=>Object.fromEntries(Object.keys(S).map(id=>[id,{...S[id]}]));
  const minOf=id=>MIN[kind(id)];
  function layout(ids,S){
    const occ=[],pos={};let r=0,c=0;
    for(const id of ids){
      const w=Math.min(12,S[id].w),h=S[id].h;let m=0;
      for(;;){
        if(c+w>12){r++;c=0;continue}
        m=((1<<w)-1)<<c;let free=true;
        for(let y=r;y<r+h;y++)if((occ[y]||0)&m){free=false;break}
        if(free)break;c++;
      }
      for(let y=r;y<r+h;y++)occ[y]=(occ[y]||0)|m;
      pos[id]={x:c,y:r,w,h};c+=w;
    }
    let gaps=0;
    for(let y=0;y<occ.length;y++){let m=occ[y]||0;gaps+=12;while(m){m&=m-1;gaps--}}
    return {pos,gaps};
  }
  const sameRows=(ids,a,b)=>ids.every(id=>a.pos[id].y===b.pos[id].y);
  // Only sizes that differ from the defaults are kept.
  function commit(S){
    for(const id in S){const s=S[id],d=defaultSize(id);if(s.w===d.w&&s.h===d.h)delete state.sizes[id];else state.sizes[id]={w:s.w,h:s.h}}
  }
  // Close gaps by widening sections, or else making them taller, a step at a time and taking turns.
  // A step is kept only if it closes some of a gap without moving any section to another row.
  function fill(ids,S,fixed,prefer){
    if(narrow)return S;
    const cand=[...new Set([...prefer,...ids.slice().reverse()])].filter(id=>!fixed.has(id));
    let L=layout(ids,S),turn=0,guard=0;
    while(L.gaps&&cand.length&&guard++<400){
      let next=null;
      for(const k of ['w','h']){
        for(let j=0;j<cand.length&&!next;j++){
          const id=cand[(turn+j)%cand.length],s=S[id];
          if(s[k]>=(k==='w'?12:DASH_MAX_ROWS))continue;
          s[k]++;
          const T=layout(ids,S);
          if(T.gaps<L.gaps&&sameRows(ids,T,L)){next=T;turn=(turn+j+1)%cand.length}else s[k]--;
        }
        if(next)break;
      }
      if(!next)break;
      L=next;
    }
    return S;
  }
  // Nearest in order first, and after before before when equally near.
  const nearest=(ids,i)=>(a,b)=>{const da=ids.indexOf(a)-i,db=ids.indexOf(b)-i;return Math.abs(da)-Math.abs(db)||db-da};
  // Put the shown sections in the order given, leaving hidden ones where they are.
  function setOrder(ids){const q=ids.slice();state.order=state.order.map(id=>shown(cards.get(id))?q.shift():id)}
  // Section A takes the size next, from the sizes in base, and the others make room. Those in the
  // row it shares with ref (A itself when resizing, or the section it was dropped beside) shrink a
  // column at a time, nearest first. If they cannot all fit even at their smallest, the last ones
  // move on to the next row, and share a row of their own if they do not fit there either.
  // Heights are left alone unless follow is set: a section can stand taller than the ones beside
  // it, and the sections after them stack up in the room underneath. With follow, sections beside
  // A that line up with its bottom edge keep lining up. Then any gaps are closed.
  function adjust(ids,base,A,next,ref,follow){
    const S=copy(base);S[A]={...next};
    if(narrow||ids.length<2)return S;
    const before=layout(ref===A?ids:ids.filter(x=>x!==A),base),home=before.pos[ref];
    if(!home)return fill(ids,S,new Set([A]),[]);
    // A section moved into a row takes that row's height.
    if(ref!==A)S[A].h=Math.max(minOf(A).h,Math.min(DASH_MAX_ROWS,home.h));
    const ai=ids.indexOf(A),top=home.y,bottom=home.y+home.h,at=id=>ids.indexOf(id);
    const mates=ids.filter(id=>{const p=before.pos[id];return id!==A&&p&&p.y>=top&&p.y+p.h<=bottom}).sort(nearest(ids,ai));
    const dh=next.h-base[A].h;
    if(follow&&ref===A&&dh)for(const id of mates){
      const p=before.pos[id];
      if(p.y+p.h===bottom)S[id].h=Math.max(minOf(id).h,Math.min(DASH_MAX_ROWS,S[id].h+dh));
    }
    const holds=(L,keep)=>L.pos[A].y===top&&keep.every(id=>L.pos[id].y===before.pos[id].y);
    if(!holds(layout(ids,S),mates)){
      const later=mates.filter(id=>at(id)>ai).sort((a,b)=>at(a)-at(b));
      let fitted=null,out=[];
      for(let drop=0;drop<=later.length&&!fitted;drop++){
        out=later.slice(later.length-drop);
        const keep=mates.filter(id=>!out.includes(id)),T=copy(S);
        for(let more=true;more&&!fitted;){
          more=false;
          for(const id of keep){
            if(holds(layout(ids,T),keep)){fitted=T;break}
            if(T[id].w>minOf(id).w){T[id].w--;more=true}
          }
        }
        if(!fitted&&holds(layout(ids,T),keep))fitted=T;
      }
      if(fitted){
        Object.assign(S,fitted);
        if(out.length){
          // The row the pushed sections land in must keep all of its own sections.
          const after=ids.slice(at(out[out.length-1])+1),rest=ids.filter(x=>!out.includes(x));
          const W=layout(rest,S),L=layout(ids,S),nxt=after[0];
          const fits=nxt&&rest.every(id=>W.pos[id].y!==W.pos[nxt].y||L.pos[id].y===L.pos[out[0]].y);
          if(!fits){
            let left=12;const total=out.reduce((t,id)=>t+S[id].w,0);
            out.forEach((id,i)=>{const w=i===out.length-1?Math.max(minOf(id).w,left):Math.max(minOf(id).w,Math.round(12*S[id].w/total));S[id].w=w;left-=w});
          }
        }
      }
    }
    return fill(ids,S,ref===A?new Set([A]):new Set(),mates);
  }
  // Resize A to next, in the order given. A taller or shorter section first stands on its own, with
  // the sections after it stacking up beside it. If that leaves a gap, the sections beside it that
  // line up with its bottom edge follow it instead, and if that still leaves one, the sections
  // below it give up or take the room, as with tiled windows. Returns the order and sizes.
  function resizeTo(order,S,A,next){
    if(narrow){const T=copy(S);T[A]={...next};return {order,S:T}}
    const was=layout(order,S).gaps,tries=[()=>[order,adjust(order,S,A,next,A)]];
    if(next.h!==S[A].h){
      tries.push(()=>[order,adjust(order,S,A,next,A,true)]);
      tries.push(()=>{const T=copy(S),o=moveEdge(order,T,A,'y',next.h-S[A].h,true);return o&&[o,adjust(o,T,A,{w:next.w,h:T[A].h},A)]});
    }
    let best=null;
    for(const t of tries){
      const r=t();if(!r)continue;
      const g=layout(r[0],r[1]).gaps;
      if(!best||g<best.g)best={order:r[0],S:r[1],g};
      if(g<=was)break;
    }
    return best;
  }
  // Take section A out. First it narrows a column at a time and the sections in its row widen
  // into the room, taking turns from the nearest, so the next row does not move up into the gap.
  function vanish(ids,base,A){
    const S=copy(base);
    if(narrow||!S[A]||ids.length<2){delete S[A];return S}
    const before=layout(ids,base),a=before.pos[A];
    const mates=ids.filter(id=>{const p=before.pos[id];return id!==A&&p.y<a.y+a.h&&p.y+p.h>a.y}).sort(nearest(ids,ids.indexOf(A)));
    let turn=0;
    for(let w=S[A].w-1;w>=0&&mates.length;w--){
      if(w)S[A].w=w;else{delete S[A];ids=ids.filter(x=>x!==A)}
      const G=layout(ids,S);
      for(let j=0;j<mates.length;j++){
        const id=mates[(turn+j)%mates.length];if(S[id].w>=12)continue;
        S[id].w++;
        const M=layout(ids,S);
        if(M.gaps<G.gaps&&sameRows(ids,M,G)){turn=(turn+j+1)%mates.length;break}
        S[id].w--;
      }
    }
    delete S[A];ids=ids.filter(x=>x!==A);
    return fill(ids,S,new Set(),mates);
  }
  // Move one of A's edges by k rows or columns; k>0 grows A. axis is 'y' or 'x', and atEnd picks the
  // bottom or right edge rather than the top or left. As with tiled windows, the sections on the
  // far side of the edge give up the room or take it, those on A's side that meet the same line
  // move with it, and nothing else moves. If k cannot be done it tries less. The grid places
  // sections in order and never goes back up, so a section can only start higher than one in its
  // row by coming before it, or lower by coming after it. Returns the order to use, or null if
  // nothing moved.
  function moveEdge(ids,S,A,axis,k,atEnd){
    const v=axis==='y',L=layout(ids,S),a=L.pos[A];
    const lo=p=>v?p.y:p.x,hi=p=>v?p.y+p.h:p.x+p.w,across=p=>v?p.x:p.y,wide=p=>v?p.w:p.h,key=v?'h':'w';
    const line=atEnd?hi(a):lo(a);
    // The edge runs as far across as sections meet at it without one crossing it.
    let from=across(a),to=from+wide(a),ending=[],starting=[];
    for(let grew=true;grew;){
      grew=false;ending=[];starting=[];
      for(const id of ids){
        const p=L.pos[id];
        if(across(p)>=to||across(p)+wide(p)<=from)continue;
        if(hi(p)===line)ending.push(id);
        else if(lo(p)===line)starting.push(id);
        else if(lo(p)<line&&hi(p)>line)return null;
        else continue;
        if(across(p)<from){from=across(p);grew=true}
        if(across(p)+wide(p)>to){to=across(p)+wide(p);grew=true}
      }
    }
    const near=atEnd?ending:starting,far=atEnd?starting:ending;
    if(!far.length)return null;
    // Nearest places in the order first, so the order changes as little as it can.
    const i=ids.indexOf(A),orders=[ids];
    if(v)for(let d=1;d<ids.length;d++)for(const j of [i-d,i+d])if(j>=0&&j<ids.length){const o=ids.filter(x=>x!==A);o.splice(j,0,A);orders.push(o)}
    const still=(M,id)=>far.includes(id)||near.includes(id)||(M.pos[id].x===L.pos[id].x&&M.pos[id].y===L.pos[id].y);
    for(let n=k;n;n-=Math.sign(n)){
      const T=copy(S);let ok=true;
      for(const id of far){T[id][key]-=n;if(T[id][key]<minOf(id)[key])ok=false}
      for(const id of near){T[id][key]+=n;if(T[id][key]<minOf(id)[key]||T[id][key]>(v?DASH_MAX_ROWS:12))ok=false}
      if(!ok)continue;
      for(const o of orders){
        const M=layout(o,T),p=M.pos[A];
        if((atEnd?hi(p)!==line+n||lo(p)!==lo(a):lo(p)!==line-n||hi(p)!==hi(a))||M.gaps>L.gaps)continue;
        // Each section at the edge keeps its other edge where it was.
        if(!ending.every(id=>lo(M.pos[id])===lo(L.pos[id]))||!starting.every(id=>hi(M.pos[id])===hi(L.pos[id])))continue;
        if(!ids.every(id=>still(M,id)))continue;
        Object.assign(S,T);return o;
      }
    }
    return null;
  }

  function spans(){
    narrow=dash.clientWidth>0&&dash.clientWidth<820;dash.classList.toggle('narrow',narrow);
    for(const c of dash.children){
      const id=c.classList.contains('card-slot')?drag&&drag.card.dataset.card:c.dataset.card;if(!id)continue;
      const col='span '+cols(id),row='span '+size(id).h;
      if(c.style.gridColumn!==col)c.style.gridColumn=col;
      if(c.style.gridRow!==row)c.style.gridRow=row;
    }
  }
  function apply(){
    for(const id of state.order){const c=cards.get(id);if(dash.lastElementChild!==c)dash.append(c)}
    for(const [id,c] of cards)c.classList.toggle('user-hidden',state.hidden.has(id));
    spans();
  }
  let settleTimer=0;
  const settle=(ms=240)=>{clearTimeout(settleTimer);settleTimer=setTimeout(()=>{if(typeof drawSparks==='function')drawSparks()},ms)};

  // FLIP: note where everything is, change the layout, then slide each section from its old
  // place to its new one. skip is a section that is placed by hand, such as one being resized.
  function flip(change,skip){
    const fixed=c=>c===(drag&&drag.card)||c===skip;
    const els=[...dash.children].filter(c=>!fixed(c));
    const first=new Map(els.map(c=>[c,c.getBoundingClientRect()]));
    for(const c of els){c.classList.remove('moving');c.style.transform=''}
    change();
    const moved=[],top=scroller.scrollTop;rects=[];
    for(const c of [...dash.children]){
      if(fixed(c))continue;
      const b=c.getBoundingClientRect();if(!b.width)continue;
      rects.push({el:c,l:b.left,r:b.right,t:b.top+top,b:b.bottom+top});
      const a=first.get(c);if(!a||!a.width)continue;
      const dx=a.left-b.left,dy=a.top-b.top;
      if(Math.abs(dx)>.5||Math.abs(dy)>.5){c.style.transform=`translate(${dx}px,${dy}px)`;moved.push(c)}
    }
    if(!moved.length)return;
    void dash.offsetWidth;
    for(const c of moved){c.classList.add('moving');c.style.transform=''}
  }
  function measure(){
    const top=scroller.scrollTop;rects=[];
    for(const c of dash.children){if(c===(drag&&drag.card))continue;const b=c.getBoundingClientRect();if(b.width)rects.push({el:c,l:b.left,r:b.right,t:b.top+top,b:b.bottom+top})}
  }
  // Scroll when the pointer nears the top or bottom of the activity view.
  function autoScroll(y){
    const v=scroller.getBoundingClientRect(),edge=56;
    if(y<v.top+edge){scroller.scrollTop-=Math.ceil(14*(v.top+edge-y)/edge);return true}
    if(y>v.bottom-edge){scroller.scrollTop+=Math.ceil(14*(y-v.bottom+edge)/edge);return true}
    return false;
  }

  // ---- Moving ----
  function begin(e){
    const card=pending.card,r=card.getBoundingClientRect(),slot=el('div','card-slot');
    card.before(slot);
    const ids=visible(),base=sizesOf(ids);
    // The sizes with this section taken out, so its old row has closed up wherever it goes.
    drag={card,slot,home:state.order.slice(),base,closed:{...vanish(ids,base,card.dataset.card),[card.dataset.card]:base[card.dataset.card]},x:e.clientX,y:e.clientY,startX:e.clientX,startY:e.clientY,left:r.left,top:r.top,raf:0};
    card.classList.add('dragging');
    Object.assign(card.style,{left:r.left+'px',top:r.top+'px',width:r.width+'px',height:r.height+'px'});
    document.body.classList.add('dragging-card');
    spans();measure();
    drag.raf=requestAnimationFrame(frame);
  }
  function frame(){
    if(!drag||drag.dropping)return;
    autoScroll(drag.y);
    drag.card.style.transform=`translate(${drag.x-drag.startX}px,${drag.y-drag.startY}px) scale(1.012)`;
    place();
    drag.raf=requestAnimationFrame(frame);
  }
  function place(){
    const x=drag.x,y=drag.y+scroller.scrollTop,q=rects.find(q=>x>=q.l&&x<q.r&&y>=q.t&&y<q.b);
    if(!q||q.el===drag.slot)return;
    const wide=q.r-q.l>dash.clientWidth*.6,before=wide?y<(q.t+q.b)/2:x<(q.l+q.r)/2;
    const items=[...dash.children].filter(c=>c!==drag.card),from=items.indexOf(drag.slot);
    let to=items.indexOf(q.el)+(before?0:1);if(to>from)to--;
    if(to===from)return;
    flip(()=>{
      items.splice(from,1);
      const ref=items[to];
      if(ref)ref.before(drag.slot);else dash.append(drag.slot);
      // The section joins the row of the one it was dropped beside, and the rest make room.
      const A=drag.card.dataset.card;
      commit(adjust(flow(),drag.closed,A,drag.base[A],q.el.dataset.card));
      spans();
    });
  }
  // The shown sections in the order they are on the page, with the drop slot standing in for the
  // section being dragged.
  const flow=()=>[...dash.children].filter(c=>c!==(drag&&drag.card))
    .map(c=>c===(drag&&drag.slot)?drag.card.dataset.card:c.dataset.card).filter(id=>id&&shown(cards.get(id)));
  function finish(cancel){
    const d=drag;if(!d||d.dropping)return;
    cancelAnimationFrame(d.raf);d.dropping=true;
    if(cancel)flip(()=>{
      const after=d.home.slice(d.home.indexOf(d.card.dataset.card)+1).map(id=>cards.get(id)).find(c=>c.parentNode===dash);
      if(after)after.before(d.slot);else dash.append(d.slot);
      commit(d.base);spans();
    });
    const r=d.slot.getBoundingClientRect();
    d.card.classList.add('dropping');
    d.card.style.transform=`translate(${r.left-d.left}px,${r.top-d.top}px)`;
    d.card.style.width=r.width+'px';d.card.style.height=r.height+'px';
    let done=false;
    const land=()=>{
      if(done)return;done=true;
      d.card.classList.remove('dragging','dropping');
      for(const k of ['left','top','width','height','transform'])d.card.style[k]='';
      d.slot.replaceWith(d.card);
      document.body.classList.remove('dragging-card');
      drag=null;
      state.order=[...dash.children].map(c=>c.dataset.card).filter(Boolean);
      spans();if(!cancel)save();settle();
    };
    d.card.addEventListener('transitionend',e=>{if(e.target===d.card&&e.propertyName==='transform')land()});
    setTimeout(land,300);
  }

  // ---- Resizing from an edge or corner ----
  // The outline follows the pointer and the section snaps to whole columns and rows under it. The
  // others adjust as described above adjust() and moveEdge(). With Shift held, dragging the bottom
  // or right edge changes only this section and leaves any gap for the user to fill.
  const CURSOR={n:'ns',s:'ns',e:'ew',w:'ew',nw:'nwse',se:'nwse',ne:'nesw',sw:'nesw'};
  function beginResize(card,handle,e){
    const id=card.dataset.card,edge=handle.dataset.edge,r=card.getBoundingClientRect(),ghost=el('div','resize-ghost'),badge=el('div','size-badge');
    Object.assign(ghost.style,{left:r.left+'px',top:r.top+'px',width:r.width+'px',height:r.height+'px'});
    document.body.append(ghost);card.append(badge);
    const ids=visible(),base=sizesOf(ids),home=layout(ids,base).pos[id],s=size(id);
    resize={card,id,edge,handle,ghost,badge,ids,base,home,order:state.order.slice(),raf:0,px:e.clientX,py:e.clientY,free:e.shiftKey};
    resize.key=narrow?`${s.w},${s.h}`:[home.y,home.y+home.h,home.x,home.x+home.w,resize.free].join();
    handle.classList.add('active');card.classList.add('resizing');
    document.body.classList.add('resizing-card');document.body.style.setProperty('--resize-cursor',CURSOR[edge]+'-resize');
    showSize();
    resize.raf=requestAnimationFrame(resizeFrame);
  }
  function showSize(){
    const z=resize,s=size(z.id);
    z.badge.replaceChildren(`${s.w} of 12 columns × ${s.h} rows`);
    if(/[se]/.test(z.edge)&&!narrow)z.badge.append(el('small','',z.free?'Only this section':'Hold Shift to resize only this one'));
  }
  function resizeFrame(){
    const z=resize;if(!z)return;
    autoScroll(z.py);
    const r=z.card.getBoundingClientRect(),d=dash.getBoundingClientRect(),e=z.edge;
    // Grid lines nearest the pointer. Line j is where column j starts, so a right edge sits a gap
    // before it; rounding from the middle of the gap treats both alike.
    const m=minOf(z.id),colP=(d.width+DASH_GAP)/12,rowP=DASH_ROW+DASH_GAP;
    const lineX=x=>Math.max(0,Math.min(12,Math.round((x-d.left+DASH_GAP/2)/colP)));
    const lineY=y=>Math.max(0,Math.round((y-d.top+DASH_GAP/2)/rowP));
    if(narrow){
      const gr=e.includes('e')?Math.max(z.px,r.left+40):r.right,gb=e.includes('s')?Math.max(z.py,r.top+40):r.bottom;
      Object.assign(z.ghost.style,{left:r.left+'px',top:r.top+'px',width:gr-r.left+'px',height:gb-r.top+'px'});
      // Each section takes twice its columns here, and only its own size changes.
      const s={...size(z.id)};
      if(e.includes('e'))s.w=Math.max(m.w,Math.min(12,Math.ceil((lineX(gr)-lineX(r.left))/2)));
      if(e.includes('s'))s.h=Math.max(m.h,Math.min(DASH_MAX_ROWS,lineY(gb)-lineY(r.top)));
      const key=`${s.w},${s.h}`;
      if(key!==z.key){z.key=key;commit({...copy(z.base),[z.id]:s});flip(spans,z.card);showSize();settle(80)}
    }else{
      const a=z.home,v=e.includes('n')?-1:e.includes('s')?1:0,h=e.includes('w')?-1:e.includes('e')?1:0;
      // Where the section should span on one axis, as grid lines. Dragging an edge towards the
      // opposite one shrinks the section to its smallest; once the pointer crosses that opposite
      // edge, the section grows out past it instead, the way a stretched rectangle flips over.
      const span=(side,lo,hi,p,min,max,end)=>{
        if(side>0)return p<lo?[Math.max(0,p,lo+min-max),lo+min]:[lo,Math.min(end,lo+max,Math.max(lo+min,p))];
        if(side<0)return p>hi?[hi-min,Math.min(end,p,hi-min+max)]:[Math.max(0,hi-max,Math.min(hi-min,p)),hi];
        return [lo,hi];
      };
      const [top,bottom]=span(v,a.y,a.y+a.h,lineY(z.py),m.h,DASH_MAX_ROWS,Infinity);
      const [left,right]=span(h,a.x,a.x+a.w,lineX(z.px),m.w,12,12);
      // The outline, with the same flip: the dragged edge follows the pointer.
      const ghost=(side,lo,hi,p,min,start,pitch,now)=>{
        const L=start+lo*pitch,H=start+hi*pitch-DASH_GAP;
        if(side>0)return p<L?[p,start+(lo+min)*pitch-DASH_GAP]:[L,Math.max(p,L+40)];
        if(side<0)return p>H?[start+(hi-min)*pitch,p]:[Math.min(p,H-40),H];
        return now;
      };
      const [gt,gb]=ghost(v,a.y,a.y+a.h,z.py,m.h,d.top,rowP,[r.top,r.bottom]);
      const [gl,gr]=ghost(h,a.x,a.x+a.w,z.px,m.w,d.left,colP,[r.left,r.right]);
      Object.assign(z.ghost.style,{left:gl+'px',top:gt+'px',width:gr-gl+'px',height:gb-gt+'px'});
      const key=[top,bottom,left,right,z.free].join();
      if(key!==z.key){
        z.key=key;
        let S=copy(z.base),order=z.ids;
        if(top!==a.y)order=moveEdge(order,S,z.id,'y',a.y-top)||order;
        if(left!==a.x)order=moveEdge(order,S,z.id,'x',a.x-left)||order;
        const p=layout(order,S).pos[z.id];
        const next={w:Math.max(m.w,Math.min(12,right-p.x)),h:Math.max(m.h,Math.min(DASH_MAX_ROWS,bottom-p.y))};
        if(z.free)S[z.id]=next;else({order,S}=resizeTo(order,S,z.id,next));
        commit(S);setOrder(order);flip(apply,z.card);showSize();settle(80);
      }
    }
    z.raf=requestAnimationFrame(resizeFrame);
  }
  function endResize(cancel){
    const z=resize;if(!z)return;
    cancelAnimationFrame(z.raf);resize=null;
    if(cancel){state.order=z.order;commit(z.base);flip(apply,z.card)}
    z.ghost.remove();z.badge.remove();z.handle.classList.remove('active');z.card.classList.remove('resizing');
    document.body.classList.remove('resizing-card');
    if(!cancel)save();settle();
  }

  // ---- Pointer handling ----
  dash.addEventListener('pointerdown',e=>{
    if(!editing||drag||resize||e.button!==0||e.target.closest('[data-act]'))return;
    const card=e.target.closest('[data-card]');if(!card||card.parentNode!==dash)return;
    e.preventDefault();pending={card,x:e.clientX,y:e.clientY};
  });
  window.addEventListener('pointermove',e=>{
    if(resize){resize.px=e.clientX;resize.py=e.clientY;resize.free=e.shiftKey;return}
    if(drag){drag.x=e.clientX;drag.y=e.clientY;return}
    if(pending&&Math.hypot(e.clientX-pending.x,e.clientY-pending.y)>4){begin(e);pending=null}
  });
  window.addEventListener('pointerup',()=>{pending=null;if(resize)endResize(false);if(drag)finish(false)});
  const cancelAll=()=>{pending=null;if(resize)endResize(true);if(drag)finish(true)};
  window.addEventListener('pointercancel',cancelAll);
  const shiftKey=e=>{if(resize&&e.key==='Shift')resize.free=e.type==='keydown'};
  window.addEventListener('keydown',shiftKey);window.addEventListener('keyup',shiftKey);
  window.addEventListener('blur',cancelAll);

  // ---- Controls on each section, also usable from the keyboard ----
  for(const [id,c] of cards){
    const label=CARD_INFO[id],tools=el('div','card-tools');
    const grip=el('button','grip');grip.type='button';grip.dataset.act='move';
    grip.title='Drag to move. Arrow keys move it; Shift with the arrow keys resizes it.';grip.setAttribute('aria-label',`Move or resize ${label}`);grip.append(icon('grip'));
    const hide=el('button');hide.type='button';hide.dataset.act='hide';hide.title='Hide';hide.setAttribute('aria-label',`Hide ${label}`);hide.append(icon('eye-off'));
    tools.append(grip,hide);
    // A handle on every edge and corner. Only the bottom-right one is drawn; the others show when
    // the pointer is over them.
    const handles=['n','s','e','w','nw','ne','sw','se'].map(edge=>{
      const h=el('div',edge==='se'?'card-resize':'card-edge');h.dataset.act='resize';h.dataset.edge=edge;
      if(edge==='se'){h.title='Drag to resize';h.append(icon('resize'))}
      h.addEventListener('pointerdown',e=>{if(!editing||drag||resize||e.button!==0)return;e.preventDefault();e.stopPropagation();beginResize(c,h,e)});
      return h;
    });
    c.append(tools,...handles);
    grip.addEventListener('pointerdown',e=>{if(!editing||e.button!==0)return;e.preventDefault();e.stopPropagation();pending={card:c,x:e.clientX,y:e.clientY}});
    grip.addEventListener('keydown',e=>{
      const step={ArrowLeft:-1,ArrowUp:-1,ArrowRight:1,ArrowDown:1}[e.key];if(!step)return;
      e.preventDefault();
      const ids=visible(),base=sizesOf(ids);
      if(e.shiftKey){
        // Shift resizes from the bottom-right corner; with Alt as well, only this section changes.
        const s={...base[id]},m=minOf(id);
        if(e.key==='ArrowLeft'||e.key==='ArrowRight')s.w=Math.max(m.w,Math.min(12,s.w+step));else s.h=Math.max(m.h,Math.min(DASH_MAX_ROWS,s.h+step));
        if(e.altKey)commit({...base,[id]:s});else{const r=resizeTo(ids,base,id,s);commit(r.S);setOrder(r.order)}
        flip(apply);save();settle();grip.focus();
        grip.title=`${s.w} of 12 columns × ${s.h} rows`;return;
      }
      const i=ids.indexOf(id),other=ids[i+step];if(!other)return;
      flip(()=>{
        const a=state.order.indexOf(id);state.order.splice(a,1);state.order.splice(state.order.indexOf(other)+(step>0?1:0),0,id);
        commit(adjust(visible(),{...vanish(ids,base,id),[id]:base[id]},id,base[id],other));apply();
      });
      save();settle();grip.focus();
    });
    hide.addEventListener('click',()=>{
      const was=visible();
      state.hidden.add(id);
      // The sections nearest the hidden one take up its room first.
      commit(vanish(was,sizesOf(was),id));flip(apply);
      save();renderHidden();settle();
      const next=[...cards.values()].find(x=>shown(x));if(next)next.querySelector('[data-act=move]').focus();else $('layoutDone').focus();
    });
  }
  // A section shown again goes back into the row before or after it, whichever changes the
  // layout less: sizes count a little, and sections moved to another row count a lot.
  function reveal(id){
    state.hidden.delete(id);
    flip(()=>{
      apply();
      const ids=visible(),i=ids.indexOf(id),base=sizesOf(ids),was=layout(ids.filter(x=>x!==id),base);
      let best=null,least=Infinity;
      for(const ref of [ids[i-1],ids[i+1]]){
        if(!ref)continue;
        const S=adjust(ids,base,id,base[id],ref),L=layout(ids,S);
        const cost=L.gaps*1000+ids.reduce((t,x)=>t+Math.abs(S[x].w-base[x].w)+Math.abs(S[x].h-base[x].h)+(x!==id&&L.pos[x].y!==was.pos[x].y?10:0),0);
        if(cost<least){least=cost;best=S}
      }
      if(best)commit(best);
      spans();
    });
    save();renderHidden();settle();
  }
  function renderHidden(){
    const list=$('hiddenCards'),ids=state.order.filter(id=>state.hidden.has(id));
    if(!ids.length){list.replaceChildren();return}
    list.replaceChildren(el('span','','Hidden:'),...ids.map(id=>{
      const b=el('button','chip');b.type='button';b.title='Show this section again';b.append(icon('plus'),CARD_INFO[id]);
      b.onclick=()=>reveal(id);
      return b;
    }));
  }
  function setEditing(on){
    cancelAll();
    editing=on;dash.classList.toggle('editing',on);
    $('layoutBar').classList.toggle('hidden',!on);$('layoutButton').classList.toggle('hidden',on||$('actBody').classList.contains('hidden'));
    if(on){renderHidden();$('layoutDone').focus()}else $('layoutButton').focus();
  }
  $('layoutButton').onclick=()=>setEditing(true);
  $('layoutDone').onclick=()=>setEditing(false);
  $('layoutReset').onclick=()=>{state={order:defaultOrder.slice(),hidden:new Set(),sizes:{}};flip(apply);save();renderHidden();settle()};
  if(typeof ResizeObserver==='function')new ResizeObserver(()=>{if((dash.clientWidth>0&&dash.clientWidth<820)!==narrow){spans();settle()}}).observe(dash);
  apply();
  return {
    apply,spans,editing:()=>editing,
    available(on){if(!on&&editing)setEditing(false);$('layoutButton').classList.toggle('hidden',!on||editing)},
    // Escape cancels a drag or resize, or else leaves edit mode.
    escape(){if(drag||resize){cancelAll();return true}if(editing){setEditing(false);return true}return false},
  };
})();

