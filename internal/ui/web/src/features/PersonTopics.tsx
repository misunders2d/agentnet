// Flat presentation only. A persisted workspace preference chooses one existing
// Main root; message activity and matching names never change its audience.
import { useEffect, useRef, useState } from "react";
import { Menu } from "@base-ui/react/menu";
import type { T } from "../api";
import { errorText } from "../api";
import { useApp, useWide } from "../context";
import { useStore } from "../store";
import { personRoots, personTopicEntries, type PersonTopicEntry } from "../person-topics.mjs";
import { firstLine, personName, topicOf, type Topic } from "../model";
import { Button } from "../ui/Button";
import { AllTopics } from "./Conversation.alltopics";
import { TopicMenu, topicLabel } from "./Conversation.topics";

export function PersonTopics({ dm, main, onMain }: { dm: T.DMThread; main: string; onMain: () => void }) {
 const store=useApp();
 const wide=useWide();
 const overview=useStore(store,s=>s.overview);
 const draft=useStore(store,s=>s.drafts[dm.id]);
 const roots=personRoots(overview,dm), rootIDs=roots.map(r=>r.id).sort().join("\n");
 const [views,setViews]=useState<Record<string,T.DMThread>>({});
 const [open,setOpen]=useState(false);
 const [loading,setLoading]=useState(true),[failed,setFailed]=useState("");
 const stamps=useRef<Record<string,string>>({});
 useEffect(()=>{
  let alive=true;
  const cached=store.get().views;
  const available:Record<string,T.DMThread>={};
  for(const r of roots){const view=r.id===dm.id?dm:views[r.id]||cached[r.id] as T.DMThread|undefined;if(view)available[r.id]=view;}
  const stamp=(r:T.DMSummary)=>JSON.stringify([r.count,r.last_at,r.unread]);
  const currentRoot=roots.find(r=>r.id===dm.id);if(currentRoot)stamps.current[dm.id]=stamp(currentRoot);
  setViews(available);
  const missing=roots.filter(r=>r.id!==dm.id&&(!available[r.id]||stamps.current[r.id]!==stamp(r)));
  if(!open){setLoading(!!missing.length);setFailed("");return;}
  setLoading(!!missing.length);setFailed("");
  if(missing.length)void Promise.allSettled(missing.map(r=>store.api.dm(r.id))).then(results=>{
   if(!alive)return;
   const loaded:Record<string,T.DMThread>={},errors:string[]=[];
   results.forEach((result,i)=>{if(result.status==="fulfilled"){loaded[missing[i].id]=result.value;stamps.current[missing[i].id]=stamp(missing[i]);}else errors.push(errorText(result.reason));});
   setViews(current=>({...current,...loaded}));setFailed(errors.length?"Some topics could not load: "+errors[0]:"");setLoading(false);
  });
  return()=>{alive=false;};
 },[store,dm,rootIDs,overview?.seq,open]);
 const entries=personTopicEntries(roots,views,main);
 const current=entries.find(e=>e.conv===dm.id&&e.topic===(draft?.topic||""));
 const selectedTopic=current?.native?topicOf({...current.native,conv:current.conv}):null;
 const selectedMain=dm.id===main&&!draft?.topic&&!draft?.newTopic;
 const choose=(e:PersonTopicEntry)=>{
  setOpen(false);
  store.setDraft(e.conv,{...store.draft(e.conv),topic:e.topic||undefined,newTopic:false,replyTo:undefined});
  void store.open({kind:"dm",id:e.conv});
 };
 const flatTopics:Topic[]=entries.map(e=>e.native?{...topicOf({...e.native,conv:e.conv}),id:e.key}:{id:e.key,conv:e.conv,peer:"",title:e.title,last:e.last,lastAt:e.root.last_at||e.root.created,unread:e.unread,needsYou:0,waiting:false,pending:false,state:"active",renamed:false,count:e.count,quietSince:e.root.last_at||e.root.created});
 const change=async(what:"delete"|"done"|"archive",ids:string[],counts:Record<string,number>)=>{
  const groups=new Map<string,{ids:string[];counts:Record<string,number>}>();
  for(const e of entries.filter(e=>e.native&&ids.includes(e.key))){const group=groups.get(e.conv)||{ids:[],counts:{}};group.ids.push(e.topic);group.counts[e.topic]=counts[e.key];groups.set(e.conv,group);}
  const results=await Promise.all([...groups].map(async([conv,group])=>{
   const result=await store.run(api=>api.changeTopic(what,{conv,peer:"",id:"",...group}));
   if(!result)return undefined;
   if(what==="delete"&&group.ids.includes(store.draft(conv).topic||""))store.setDraft(conv,{...store.draft(conv),topic:undefined,newTopic:false,replyTo:undefined});
   try{const loaded=await store.api.dm(conv);setViews(current=>({...current,[conv]:loaded}));}catch(error){setFailed(errorText(error));}
   return result;
  }));
  return results.every(Boolean)?{note:results.map(r=>r!.note).join(" ")}:undefined;
 };
 const knownCount=!loading&&!failed;
 const chip="min-h-11 min-w-0 truncate rounded-full px-3 text-[14px] font-semibold stroke ";
 return <>
  <nav aria-label="Topics" className="flex min-w-0 items-center gap-2 border-b border-hairline bg-canvas px-4 py-2">
   <button type="button" aria-pressed={selectedMain} onClick={onMain} className={chip+(selectedMain?"bg-ink text-canvas":"bg-surface")}>Main</button>
   {selectedTopic ? <TopicMenu topic={selectedTopic} onAll={()=>setOpen(true)} trigger={<Menu.Trigger className={chip+"flex-1 bg-ink text-canvas"} aria-label={topicLabel(selectedTopic)+", open topic, menu"}>{firstLine(selectedTopic.title,30)}</Menu.Trigger>}/> : current&&<button type="button" aria-current="true" onClick={()=>setOpen(true)} className={chip+"flex-1 bg-ink text-canvas"}>{firstLine(current.title,30)}</button>}
   <Button size="sm" variant="outline" onClick={()=>setOpen(true)} aria-label={"All topics"+(knownCount?" ("+entries.length+")":"")}>{wide?"All topics"+(knownCount?" ("+entries.length+")":""):"All"+(knownCount?" "+entries.length:"")}</Button>
   <Button size="sm" variant="ghost" aria-label="New topic" aria-pressed={!!draft?.newTopic} onClick={()=>store.setDraft(dm.id,{...store.draft(dm.id),topic:undefined,newTopic:!draft?.newTopic,replyTo:undefined})}>{wide?"New topic":"+"}</Button>
  </nav>
  <AllTopics open={open} onOpenChange={setOpen} peer="" conv={dm.id} agent={personName(dm.peer)} current={current?.key} flat={{topics:flatTopics,loading,failed,canSelect:id=>!!entries.find(e=>e.key===id)?.native,detail:id=>{const e=entries.find(e=>e.key===id)!;return e.count+" "+(e.count===1?"message":"messages")+(e.guests?" · "+e.guests+(e.guests===1?" guest":" guests"):"");},choose:id=>{const e=entries.find(e=>e.key===id);if(e)choose(e);},change}}/>

 </>;
}
