// Exact-key cold capacity includes custody, not merely successful POSTs.
// Current state has its own bounded capacity: a stalled archive cannot starve it.
import * as wire from './wire.mjs';
export const historyWindowSize=50;
const stateSubs=new Set([wire.SubRootSync,wire.SubReadSync,wire.SubTopicSync,wire.SubTopicStateSync,wire.SubDirectTopicSync,wire.SubModelSync,wire.SubInvitationSync]);
const coldSubs=new Set([wire.SubHistoryArchive,'history',wire.SubDeviceHistory]);
export const backgroundCopy=r=>!!r&&!r.fresh_live&&(coldSubs.has(r.sub)||stateSubs.has(r.sub));
export const historyWindow=(store,dev)=>pendingWindow(store,dev,false);
export const stateWindow=(store,dev)=>pendingWindow(store,dev,true);
async function pendingWindow(store,dev,stateOnly){
 const outstanding=(await store.outboxStates(['queued','waiting','custody','archive_staged'],dev.address)).filter(r=>r.to===dev.address&&(r.recipient_fp||r.fp)===dev.fingerprint&&(stateOnly?backgroundCopy(r)&&stateSubs.has(r.sub):r.state==='archive_staged'||backgroundCopy(r))).length;
 return Math.max(0,historyWindowSize-outstanding);
}
