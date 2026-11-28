// Exact-key cold capacity includes custody, not merely successful POSTs.
// Current state has its own bounded capacity: a stalled archive cannot starve it.
import * as wire from './wire.mjs';
export const historyWindowSize=50;
const stateSubs=new Set([wire.SubRootSync,wire.SubReadSync,wire.SubTopicSync,wire.SubTopicStateSync,wire.SubReminderSync,wire.SubBlockSync,wire.SubDirectTopicSync,wire.SubModelSync,wire.SubInvitationSync]);
const coldSubs=new Set([wire.SubHistoryArchive,'history',wire.SubDeviceHistory]);
export const backgroundCopy=r=>!!r&&!r.fresh_live&&(coldSubs.has(r.sub)||stateSubs.has(r.sub));
export const historyWindow=(store,dev)=>pendingWindow(store,dev,false);
export const stateWindow=(store,dev)=>pendingWindow(store,dev,true);
// As the Go client's syncWindowQuery: copies on the wire one by one count
// against the page window; unpacked archive children against one engine
// part (HistoryArchivePackEntries), counted on the archive_pending index so
// no page re-reads the staged rows. Parts in flight are bounded by
// HistoryArchiveWindow when packing and hold no page capacity, so a part's
// round trip never stops production.
async function pendingWindow(store,dev,stateOnly){
 const rows=(await store.outboxStates(['queued','waiting','custody'],dev.address)).filter(r=>r.to===dev.address&&(r.recipient_fp||r.fp)===dev.fingerprint);
 if(stateOnly)return Math.max(0,historyWindowSize-rows.filter(r=>backgroundCopy(r)&&stateSubs.has(r.sub)).length);
 const live=rows.filter(r=>r.sub!==wire.SubHistoryArchive&&backgroundCopy(r)).length;
 const staged=await store.archivePendingCount(dev.address,dev.fingerprint);
 return Math.max(0,Math.min(historyWindowSize-live,wire.HistoryArchivePackEntries-staged));
}
