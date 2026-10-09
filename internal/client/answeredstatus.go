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
