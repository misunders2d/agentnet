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
// against the page window; unpacked archive children against one part
// (HistoryArchiveMaxEntries). Parts in flight are bounded by
// HistoryArchiveWindow when packing and hold no page capacity, so a part's
// round trip never stops production.
async function pendingWindow(store,dev,stateOnly){
 const rows=(await store.outboxStates(['queued','waiting','custody','archive_staged'],dev.address)).filter(r=>r.to===dev.address&&(r.recipient_fp||r.fp)===dev.fingerprint);
 if(stateOnly)return Math.max(0,historyWindowSize-rows.filter(r=>backgroundCopy(r)&&stateSubs.has(r.sub)).length);
 const live=rows.filter(r=>r.state!=='archive_staged'&&r.sub!==wire.SubHistoryArchive&&backgroundCopy(r)).length;
 const staged=rows.filter(r=>r.state==='archive_staged'&&!r.archive_chunk).length;
 return Math.max(0,Math.min(historyWindowSize-live,wire.HistoryArchiveMaxEntries-staged));
}
