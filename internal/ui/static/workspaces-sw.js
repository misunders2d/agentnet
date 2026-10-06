// Scope is chosen locally at registration. Foreign push payloads never supply
// workspace identity, destination URL, labels or message contents.
"use strict";
const workspace=/^\/workspace-push\/([a-f0-9]{32})\/$/.exec(new URL(self.registration.scope).pathname)?.[1];
const channel=v=>typeof v==="string"&&/^[A-Za-z0-9_-]{22}$/.test(v)?v:"";
self.addEventListener("push",event=>{
 if(!workspace)return;
 let chan="",security=false;
 try{const p=event.data?.json();if(p?.v===1){chan=channel(p.chan);security=p.notice==="device_admin";}}catch(_){}
 event.waitUntil(self.registration.showNotification("AgentNet",{body:security?"Company settings access changed. Open AgentNet to read the notice.":"New activity",tag:workspace+":"+(security?"device-admin":chan||"summary"),renotify:true,icon:"/assets/icon-192.png",data:{chan}}));
});
self.addEventListener("notificationclick",event=>{
 event.notification.close();if(!workspace)return;
 const chan=channel(event.notification.data?.chan);
 event.waitUntil((async()=>{
  const wins=(await self.clients.matchAll({type:"window",includeUncontrolled:true})).filter(w=>new URL(w.url).origin===self.location.origin);
  if(wins.length){for(const w of wins)w.postMessage({type:"agentnet-workspace-open",chan});await wins[0].focus();return;}
  await self.clients.openWindow("/#agentnet-workspace-open:"+workspace+":"+chan);
 })());
});
