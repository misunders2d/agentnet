// Ephemeral presentation for the three paged choice lists: topics, search,
// and Review. No persistence, polling, provider cache or execution actions.
import { useEffect, useRef, useState } from "react";
import type { FocusEvent } from "react";
import type { Topic } from "../model";

export const topicIdentity = (t: Topic) => [t.conv || "",t.peer,t.id].join("/");
export const unreadTopicOrder = (a: Topic,b: Topic) => {
 const ai=(a as Topic&{orderID?:string}).orderID??a.id,bi=(b as Topic&{orderID?:string}).orderID??b.id;
 return Number(b.unread>0)-Number(a.unread>0) || Date.parse(b.lastAt)-Date.parse(a.lastAt) || (ai<bi?1:ai>bi?-1:a.peer<b.peer?-1:a.peer>b.peer?1:0);
};

export function steadyChoices<R>(previous: R[], latest: R[], key: (row:R)=>string, held: boolean): {row:R; present:boolean}[] {
 const by=new Map(latest.map(r=>[key(r),r]));
 if(!held)return latest.map(row=>({row,present:true}));
 const seen=new Set(previous.map(key));
 return [...previous.map(row=>({row:by.get(key(row))||row,present:by.has(key(row))})),...latest.filter(r=>!seen.has(key(r))).map(row=>({row,present:true}))];
}
export function useSteadyChoices<R>(rows:R[],key:(row:R)=>string,scope:string,selected=false){
 const [,changed]=useState(0),interaction=useRef({pointer:false,focus:false});
 const previous=useRef<{scope:string;rows:R[]}>({scope,rows:[]});
 const held=interaction.current.pointer||interaction.current.focus||selected;
 const choices=steadyChoices(previous.current.scope===scope?previous.current.rows:[],rows,key,held);
 previous.current={scope,rows:choices.map(c=>c.row)};
 // Event refs fence an external Store render before React commits a queued
 // continuous pointer update. State alone can lose the prior displayed IDs.
 const hold=(kind:"pointer"|"focus",value:boolean)=>{if(interaction.current[kind]!==value){interaction.current[kind]=value;changed(n=>n+1);}};
 const release=()=>{hold("pointer",false);hold("focus",false);};
 return {choices,release,bind:{onPointerEnter:()=>hold("pointer",true),onPointerOver:()=>hold("pointer",true),onPointerMove:()=>hold("pointer",true),onPointerLeave:()=>hold("pointer",false),onFocusCapture:()=>hold("focus",true),onBlurCapture:(e:FocusEvent<HTMLElement>)=>{if(!e.currentTarget.contains(e.relatedTarget as Node|null))hold("focus",false);}}};
}

interface Page<R>{rows:R[];total:number;next?:string;content?:number;seq?:number}
export function usePagedChoices<R>(enabled:boolean,scope:string,revision:number,pageSize:number,key:(row:R)=>string,load:(after:string,limit:number)=>Promise<Page<R>>,initialTarget=pageSize){
 const [state,setState]=useState<{scope:string;rows:R[];total:number;next:string;loading:boolean;error:string}>({scope,rows:[],total:0,next:"",loading:enabled,error:""});
 const current=useRef({enabled,scope,revision,key,load});current.current={enabled,scope,revision,key,load};
 const live=useRef(true),epoch=useRef(0),running=useRef(false),pending=useRef<{epoch:number;scope:string;target:number;more:boolean}|null>(null),saved=useRef(state);saved.current=state;
 const pump=async()=>{
  if(running.current)return;
  running.current=true;
  try{while(pending.current&&live.current){
   const request=pending.current;pending.current=null;
   const config=current.current,valid=()=>live.current&&current.current.enabled&&request.epoch===epoch.current&&request.scope===current.current.scope;
   if(!valid())continue;
   setState(s=>({...s,loading:true,error:""}));
   try{
    const prior=saved.current.scope===request.scope?saved.current:null;
    const rows=request.more?[...(prior?.rows||[])]:[],seen=new Set(rows.map(config.key)),cursors=new Set<string>();
    let after=request.more?prior?.next||"":"",total=prior?.total||0,next="",content:number|undefined;
    const bound=Math.ceil(request.target/Math.min(pageSize,200))+1;
    for(let n=0;n<bound;n++){
     const page=await config.load(after,Math.min(200,Math.max(pageSize,request.target-rows.length)));
     if(!valid())break;
     const stamp=page.content??page.seq;
     if(content!==undefined&&stamp!==content)throw Error("The list changed while loading. Try again.");
     content=stamp;total=page.total;next=page.next||"";
     for(const row of page.rows)if(!seen.has(config.key(row))){seen.add(config.key(row));rows.push(row);}
     if(!next||rows.length>=request.target)break;
     if(cursors.has(next)||next===after)throw Error("The list changed while loading. Try again.");
     cursors.add(next);after=next;
     if(n===bound-1)throw Error("The list changed while loading. Try again.");
    }
    if(valid())setState({scope:request.scope,rows,total,next,loading:false,error:""});
   }catch(error){if(valid())setState(s=>({...s,loading:false,error:error instanceof Error?error.message:String(error)}));}
  }}finally{running.current=false;}
 };
 const request=(more=false)=>{
  if(!current.current.enabled)return;
  const prior=saved.current.scope===scope?saved.current:null;
  pending.current={epoch:++epoch.current,scope,target:more?(prior?.rows.length||0)+pageSize:Math.max(prior?.rows.length||0,initialTarget,pageSize),more};
  void pump();
 };
 useEffect(()=>{live.current=true;return()=>{live.current=false;epoch.current++;pending.current=null;};},[]);
 useEffect(()=>{
  if(!enabled){epoch.current++;pending.current=null;setState({scope,rows:[],total:0,next:"",loading:false,error:""});return;}
  if(saved.current.scope!==scope)setState({scope,rows:[],total:0,next:"",loading:true,error:""});
  request();
  return()=>{epoch.current++;pending.current=null;};
 },[enabled,scope,revision]);
 const shown=state.scope===scope?state:{scope,rows:[] as R[],total:0,next:"",loading:enabled,error:""};
 return {...shown,retry:()=>request(),more:()=>{if(!running.current&&shown.next)request(true);}};
}
