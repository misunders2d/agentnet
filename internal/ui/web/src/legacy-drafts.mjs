// Only the local pre-Comic Android preview wrote these records. Import is
// explicit and preserves its complete frozen send; never mint a replacement ID.
export function legacyDraft(record) {
  const match = /^(dm|thread):([0-9a-f]{32}|[0-9a-f]{64})$/.exec(record?.key || '');
  if (!match || typeof record.value !== 'string') throw new Error('Unrecognized saved draft. Its original remains on this device.');
  const data = JSON.parse(record.value);
  if (!data || typeof data.body !== 'string') throw new Error('This saved draft cannot be read. Its original remains on this device.');
  const entry = { ...record, kind: match[1], conversation: match[2], body: data.body };
  if (!Object.hasOwn(data, 'request')) return entry;
  const r = data.request, dm = entry.kind === 'dm';
  const allowed = dm ? ['id', 'body', 'conv'] : ['id','body','to','kind','reply_to','agent_id'];
  if (!r || !/^[0-9a-f]{32}$/.test(r.id) || typeof r.body !== 'string' || r.body !== data.body ||
      Object.keys(r).some(k => !allowed.includes(k)) ||
      (dm ? r.conv !== entry.conversation : r.reply_to !== entry.conversation ||
        !/^[^/\s]+\/[^/\s]+$/.test(r.to || '') || !['message','question','task'].includes(r.kind) ||
        (r.agent_id !== undefined && (typeof r.agent_id !== 'string' || !r.agent_id)))) {
    throw new Error('This saved send cannot be restored safely. Its original remains on this device.');
  }
  return {...entry, endpoint: dm ? '/api/dm/send' : '/api/send', request: r};
}
