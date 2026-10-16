// Ephemeral presentation shared only by Review and topics. No host/storage/read work.
export const LIST_CHANGED = 'The list changed while loading. Try again.';

export function holdChoices(root, {key, onChange}) {
  let dead=false, pointer=false, focus=false, selected=false, order=[], known=new Map();
  const hold=(kind,value)=>{
    const old=kind==='pointer'?pointer:kind==='focus'?focus:selected;
    if(dead||old===value)return;
    if(kind==='pointer')pointer=value;else if(kind==='focus')focus=value;else selected=value;
    onChange();
  };
  const events={pointerenter:()=>hold('pointer',true),pointerover:()=>hold('pointer',true),pointermove:()=>hold('pointer',true),pointerdown:()=>hold('pointer',true),pointerleave:()=>hold('pointer',false),pointercancel:()=>hold('pointer',false),focusin:()=>hold('focus',true),focusout:e=>{if(!root.contains(e.relatedTarget))hold('focus',false);}};
  for(const [name,fn] of Object.entries(events))root.addEventListener(name,fn);
  return {
    update(rows){
      const latest=new Map(rows.map(row=>[key(row),row]));
      if(!(pointer||focus||selected))order=[...latest.keys()];
      else {const had=new Set(order);for(const id of latest.keys())if(!had.has(id))order.push(id);}
      const choices=order.map(id=>({row:latest.get(id)||known.get(id),present:latest.has(id)})).filter(c=>c.row);
      known=new Map(choices.map(c=>[key(c.row),c.row]));
      return choices;
    },
    select:value=>hold('selected',!!value),
    release(){if(dead)return;const held=pointer||focus||selected;pointer=focus=selected=false;if(held)onChange();},
    reset(){pointer=focus=selected=false;order=[];known.clear();},
    stop(){dead=true;for(const [name,fn] of Object.entries(events))root.removeEventListener(name,fn);order=[];known.clear();}
  };
}

// Refill the displayed prefix from the top on content changes. One request at a
// time, one coalesced pending refill, opaque cursors, at most200 rows/request.
export function pagedChoices({load,key,onChange,pageSize=50}) {
  let dead=false,enabled=false,scope='',revision,epoch=0,running=false,queued=false,pending=null;
  let state={rows:[],total:0,next:'',loading:false,error:''};
  const publish=next=>{state=next;onChange(state);};
  const request=(more=false)=>{
    if(dead||!enabled)return;
    pending={epoch:++epoch,scope,target:more?state.rows.length+pageSize:Math.max(state.rows.length,pageSize),more};
    if(!queued){queued=true;queueMicrotask(()=>{queued=false;void pump();});}
  };
  const pump=async()=>{
    if(running||dead)return;running=true;
    try {while(pending&&!dead){
      const ask=pending;pending=null;
      const valid=()=>!dead&&enabled&&ask.epoch===epoch&&ask.scope===scope;
      if(!valid())continue;
      const prior=state;publish({...state,loading:true,error:''});
      try {
        const rows=ask.more?[...prior.rows]:[],seen=new Set(rows.map(key));
        let after=ask.more?prior.next:'',next='',total=prior.total,stamp,stamped=false;
        const cursors=new Set(after?[after]:[]),bound=Math.ceil(ask.target/Math.min(pageSize,200))+1;
        for(let n=0;n<bound;n++){
          const page=await load(after,Math.min(200,Math.max(pageSize,ask.target-rows.length)),ask.scope);
          if(!valid())break;
          const current=page.content??page.seq;
          if(stamped&&current!==stamp)throw Error(LIST_CHANGED);
          stamped=true;stamp=current;total=page.total;next=page.next||'';
          for(const row of page.rows||[])if(!seen.has(key(row))){seen.add(key(row));rows.push(row);}
          if(next&&(next===after||cursors.has(next)))throw Error(LIST_CHANGED);
          if(!next||rows.length>=ask.target)break;
          cursors.add(next);after=next;
          if(n===bound-1)throw Error(LIST_CHANGED);
        }
        if(valid())publish({rows,total,next,loading:false,error:''});
      } catch(error){if(valid())publish({...state,loading:false,error:error?.message===LIST_CHANGED?LIST_CHANGED:'This list couldn’t load. Try again.'});}
    }} finally {running=false;}
  };
  return {
    get state(){return state;},
    update(nextScope,nextRevision,on=true){
      if(dead||enabled===on&&scope===nextScope&&revision===nextRevision)return;
      const changed=scope!==nextScope;enabled=on;scope=nextScope;revision=nextRevision;++epoch;pending=null;
      if(!enabled||changed)publish({rows:[],total:0,next:'',loading:false,error:''});
      if(enabled)request();
    },
    more(){if(!running&&!pending&&state.next)request(true);},
    retry(){request();},
    stop(){dead=true;enabled=false;++epoch;pending=null;}
  };
}

// Preserve keyed DOM nodes/focus when only scalar text/counts change.
export function placeChoices(root,nodes) {
  const keep=new Set(nodes);
  for(const child of [...root.children])if(!keep.has(child))child.remove();
  nodes.forEach((node,i)=>{if(root.children[i]!==node)root.insertBefore(node,root.children[i]||null);});
}
