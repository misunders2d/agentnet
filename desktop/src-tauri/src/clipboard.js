(() => {
  // Tauri's internal invoke stays in this closure. Browser pages have no
  // native fallback, and Rust validates this webview's current origin.
  const invoke = window.__TAURI_INTERNALS__?.invoke;
  if (typeof invoke !== 'function') return;
  const appReply = (action, body) => invoke('agentnet_app_controls', { action, body: body ?? null });
  Object.defineProperty(window, '__agentnetNativeAppJSON', { value: async (action, body) => {
    const reply = await appReply(action, body);
    if (reply.status < 200 || reply.status >= 300) throw new Error(reply.body.trim());
    return JSON.parse(reply.body);
  }});
  // An independently managed older daemon serves its own older host. Keep
  // that host's existing app-control calls usable, without proxying chats
  // or any other API and without sending an app cookie into its origin.
  const fetchPage = window.fetch.bind(window);
  const actions = { '/api/app/status': 'status', '/api/app/check': 'check', '/api/app/update': 'update', '/api/app/cli': 'cli' };
  window.fetch = async (input, init) => {
    const url = new URL(input instanceof Request ? input.url : String(input), location.href);
    const action = actions[url.pathname], method = String(init?.method || (input instanceof Request ? input.method : 'GET')).toUpperCase();
    if (url.origin !== location.origin || url.search || !action || method !== (action === 'status' || action === 'check' ? 'GET' : 'POST')) return fetchPage(input, init);
    const raw = init?.body ?? (input instanceof Request && method === 'POST' ? await input.clone().text() : undefined);
    if (raw !== undefined && (typeof raw !== 'string' || raw.length > 1024)) throw new Error('Invalid app request.');
    const reply = await appReply(action, raw === undefined ? undefined : JSON.parse(raw));
    return new Response(reply.body, { status: reply.status, headers: { 'Content-Type': 'application/json', 'Cache-Control': 'no-store' } });
  };
  Object.defineProperty(window, '__agentnetNativeSkinFolder', { value: async () => {
    const files = await invoke('agentnet_skin_folder');
    return files?.map(({ name, bytes }) => {
      const file = new File([new Uint8Array(bytes)], name.split('/').at(-1));
      Object.defineProperty(file, 'webkitRelativePath', { value: name });
      return file;
    }) ?? null;
  }});
  Object.defineProperty(window, '__agentnetNativeClipboardImage', { value: async () => {
    const image = await invoke('agentnet_clipboard_image');
    if (!image) return null;
    const canvas = document.createElement('canvas');
    canvas.width = image.width; canvas.height = image.height;
    const context = canvas.getContext('2d');
    if (!context) throw new Error('Could not paste this picture.');
    context.putImageData(new ImageData(new Uint8ClampedArray(image.rgba), image.width, image.height), 0, 0);
    const blob = await new Promise(resolve => canvas.toBlob(resolve, 'image/png'));
    if (!blob) throw new Error('Could not paste this picture.');
    return new File([blob], 'pasted-picture.png', { type: 'image/png' });
  }});
})();
