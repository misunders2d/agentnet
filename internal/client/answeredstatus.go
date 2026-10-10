package client

// Old DM replies named the executor's physical request copy. Other recipients
// cannot correlate that ID. Queue one existing signed logical status only for
// an actually answered local job and its exact retained successful output.
// This changes no execution state and never regenerates or edits a reply.
const answeredConversationStatusSchema = `
UPDATE inbox SET status_due=status_due+1
WHERE state='answered' AND replica=0 AND verified_by IS NOT NULL
 AND conv IS NOT NULL AND lid IS NOT NULL AND id!=lid
 AND kind IN ('question','task') AND pid IS NOT NULL AND coalesce(result_id,'')!=''
 AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs r WHERE r.inbox_id=inbox.id)
 AND EXISTS(SELECT 1 FROM outbox o WHERE o.id=inbox.result_id
  AND o.conv=inbox.conv AND o.pid=inbox.pid AND o.reply_to=inbox.id
  AND o.kind=CASE inbox.kind WHEN 'question' THEN 'answer' ELSE 'result' END
  AND o.status IN ('done','proposal') AND o.origin LIKE 'agent:%'
  AND coalesce(o.sub,'')='' AND o.ref_id IS NULL
  AND json_extract(o.envelope,'$.from')=json_extract(inbox.target,'$.address'));
`

// Before v0.8.15 a local conversation job told nobody any state, and the
// step above re-tells only answered ones: siblings still list a failed,
// declined or held request as open with nothing recorded. Queue one signed
// status of each retained conversation job's current recorded state, only
// where that state is terminal or waits for a decision here. A queued,
// accepted or running state may be old news after a stop, so it is never
// re-told without a fresh transition; cancel_requested is such a run. A
// waiting (part_waiting) one is told by the step below.
// Only jobs addressed to this device: a copy held here for another device's
// agent (a not_run proposal duplicate, say) is that device's to speak for.
// tellStatus still decides, at send time, whether and to whom a state is
// told. This changes no execution state and runs nothing.
const retainedConversationStatusSchema = `
UPDATE inbox SET status_due=status_due+1
WHERE state IN ('failed','cancelled','declined','resolved','not_run','not_delivered','needs_human','interrupted','awaiting','held')
 AND replica=0 AND verified_by IS NOT NULL AND conv IS NOT NULL AND lid IS NOT NULL
 AND kind IN ('question','task') AND pid IS NOT NULL
 AND json_extract(target,'$.address')=(SELECT v FROM config WHERE k='address')
 AND (local=0 OR verified_by=json_extract(target,'$.fingerprint'))
 AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs r WHERE r.inbox_id=inbox.id);
`

// No shipped version told a conversation job waiting for this device's
// agent (part_waiting): statusOf had no name for it and its admission noted
// nothing (statusDue), so a request queued here reads as unconfirmed
// elsewhere until it is claimed. Unlike a running state, a waiting one is
// not old news after a stop: it stays as it was and the worker looks at it
// again, and the daemon tells whatever is recorded when its turn comes.
// The same jobs as above; this changes no execution state and runs nothing.
const waitingConversationStatusSchema = `
UPDATE inbox SET status_due=status_due+1
WHERE state='part_waiting'
 AND replica=0 AND verified_by IS NOT NULL AND conv IS NOT NULL AND lid IS NOT NULL
 AND kind IN ('question','task') AND pid IS NOT NULL
 AND json_extract(target,'$.address')=(SELECT v FROM config WHERE k='address')
 AND (local=0 OR verified_by=json_extract(target,'$.fingerprint'))
 AND NOT EXISTS(SELECT 1 FROM reply_receiver_inputs r WHERE r.inbox_id=inbox.id);
`
