// The webview's own page menu (Back, Refresh, Save as, Print, More tools)
// is not part of the app: Back left the messenger for a blank starting page
// (Windows, November 27, 2026). Text fields and selected text keep their
// native Cut/Copy/Paste menu; AgentNet's own menus already handle the event.
(() => {
  const editable = (t) => t instanceof Element && !!t.closest('input, textarea, [contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]');
  const selected = () => { const s = window.getSelection(); return !!s && !s.isCollapsed && String(s).trim() !== ''; };
  window.addEventListener('contextmenu', (e) => {
    if (!editable(e.target) && !selected()) e.preventDefault();
  });
})();
