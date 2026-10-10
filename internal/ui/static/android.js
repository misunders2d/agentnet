// Android's trusted local page. Native owns SQLite/identity/transport; Comic
// remains the shared UI. Only bounded, origin-scoped OS actions cross this bridge.
(() => {
  'use strict';
  const bridge = window.AgentNetAndroid;
  if (!bridge || typeof bridge.postMessage !== 'function') return;
  const waiting = new Map();
  let next = 0, back = null, exporting = false;
  const request = (type, fields = {}) => new Promise((resolve, reject) => {
    const id = String(++next);
    waiting.set(id, { resolve, reject });
    try { bridge.postMessage(JSON.stringify({ id, type, ...fields })); }
    catch (error) { waiting.delete(id); reject(error); }
  });
  bridge.onmessage = event => {
    let reply;
    try { reply = JSON.parse(event.data); } catch (_) { return; }
    const pending = waiting.get(reply.id);
    if (!pending) return;
    waiting.delete(reply.id);
    if (reply.ok) pending.resolve(reply);
    else pending.reject(new Error(reply.error || 'Android could not complete this action.'));
  };
  window.__agentnetPlatform = 'android';
  window.__agentnetAndroid = Object.freeze({
    connection: () => request('connection:get'),
    setConnection: enabled => request('connection:set', { enabled: !!enabled }),
    requestNotifications: () => request('notifications:request'),
    notificationStatus: () => request('notifications:status'),
    copyText: text => request('clipboard:write', {text}),
    legacyDrafts: () => request('drafts:legacy'),
    acknowledgeLegacyDraft: (key, value) => request('drafts:ack', {key, value}),
  });
  window.__agentnetOnBack = fn => {
    back = fn;
    return () => { if (back === fn) back = null; };
  };
  window.agentnetNativeBack = () => back ? !!back() : false;
  window.__agentnetNativeVisible = true;
  window.agentnetNativeVisibility = visible => {
    window.__agentnetNativeVisible = !!visible;
    document.dispatchEvent(new Event('agentnet-native-visibility'));
  };

  // The shared UI downloads already-decrypted bytes via an owned blob URL.
  // Use the Android document picker, never a raw filesystem path or URL loader.
  document.addEventListener('click', event => {
    const link = event.composedPath().find(node => node instanceof HTMLAnchorElement && node.hasAttribute('download'));
    if (!link || !link.href.startsWith('blob:' + location.origin + '/')) return;
    event.preventDefault();
    if (exporting) return;
    exporting = true;
    let transfer = '';
    (async () => {
      const response = await fetch(link.href);
      if (!response.ok) throw new Error('Could not read this attachment. Try Download again.');
      const blob = await response.blob();
      const started = await request('export:start', { name: link.download || 'attachment', mime: blob.type || 'application/octet-stream', size: blob.size });
      transfer = started.transfer;
      for (let offset = 0; offset < blob.size; offset += 32768) {
        const bytes = new Uint8Array(await blob.slice(offset, offset + 32768).arrayBuffer());
        let raw = '';
        for (const byte of bytes) raw += String.fromCharCode(byte);
        await request('export:chunk', { transfer, data: btoa(raw) });
      }
      await request('export:finish', { transfer });
      transfer = '';
    })().catch(async error => {
      if (transfer) await request('export:cancel', { transfer }).catch(() => {});
      window.alert(error.message || 'Could not save this attachment.');
    }).finally(() => { exporting = false; });
  }, true);
})();
