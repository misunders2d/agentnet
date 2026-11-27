// The webview's own page menu (Back, Refresh, Save as, Print, More tools)
// is not part of the app: Back left the messenger for a blank starting page
// (Windows, November 27, 2026). Text fields and selected text keep their
// native Cut/Copy/Paste menu; AgentNet's own menus already handle the event.
(() => {
  const editable = (t) => t instanceof Element && !!t.closest('input, textarea, [contenteditable=""], [contenteditable="true"], [contenteditable="plaintext-only"]');
  // Selected text counts only where it was right-clicked: a selection
  // elsewhere must not let the page menu through on a blank area.
  const point = (x, y) => {
    if (document.caretPositionFromPoint) { const p = document.caretPositionFromPoint(x, y); return p && [p.offsetNode, p.offset]; }
    if (document.caretRangeFromPoint) { const r = document.caretRangeFromPoint(x, y); return r && [r.startContainer, r.startOffset]; }
    return null;
  };
  const selectedAt = (e) => {
    const s = window.getSelection();
    if (!s || s.isCollapsed || String(s).trim() === '') return false;
    const at = point(e.clientX, e.clientY);
    if (!at || !at[0]) return false;
    for (let i = 0; i < s.rangeCount; i++) { try { if (s.getRangeAt(i).isPointInRange(at[0], at[1])) return true; } catch (_) {} }
    return false;
  };
  window.addEventListener('contextmenu', (e) => {
    if (!editable(e.target) && !selectedAt(e)) e.preventDefault();
  });
})();
