(() => {
  // Tauri's internal invoke stays in this closure. Browser pages have no
  // native fallback, and Rust validates this webview's current origin.
  const invoke = window.__TAURI_INTERNALS__?.invoke;
  if (typeof invoke !== 'function') return;
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
