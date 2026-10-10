// Bound inert catch-up by exact recipient key, including relay custody: a
// successful POST alone does not prove that an offline device accepted history.
import * as wire from './wire.mjs';
export const historyWindowSize=50;
export const backgroundCopy=r=>!!r&&(r.sub==='history'||r.sub===wire.SubDeviceHistory||[wire.SubRootSync,wire.SubReadSync,wire.SubTopicSync,wire.SubTopicStateSync,wire.SubModelSync,wire.SubInvitationSync].includes(r.sub));
export async function historyWindow(store,dev){
 const outstanding=(await store.all('outbox')).filter(r=>r.to===dev.address&&(r.recipient_fp||r.fp)===dev.fingerprint&&backgroundCopy(r)&&['queued','waiting','custody'].includes(r.state)).length;
 return Math.max(0,historyWindowSize-outstanding);
}
