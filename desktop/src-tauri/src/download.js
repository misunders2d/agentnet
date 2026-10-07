(() => {
  const invoke = window.__TAURI_INTERNALS__?.invoke;
  if (typeof invoke !== 'function') return;
  // Existing host downloads click an owned anchor. WebKitGTK's navigation
  // callback lacks download metadata, so preserve the explicit basename
  // here and use its native download operation without any navigation.
  document.addEventListener('click', event => {
    const link = event.composedPath().find(node => node instanceof HTMLAnchorElement && node.hasAttribute('download'));
    if (!link || !link.href.startsWith('blob:')) return;
    event.preventDefault();
    void invoke('agentnet_download_blob', { url: link.href, name: link.download })
      .catch(() => window.alert('AgentNet could not start this download. Try Download again.'));
  }, true);
})();
