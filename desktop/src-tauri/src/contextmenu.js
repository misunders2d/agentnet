// The webview's own page menu (Back, Refresh, Save as, Print, More tools)
// is not part of the app: Back left the messenger for a blank starting page
// (Windows, November 27, 2026). Text fields and selected text keep their
// native Cut/Copy/Paste menu; AgentNet's own menus already handle the event.
(() => {
  // Only text entry keeps the native menu: a checkbox, file picker or
  // slider would otherwise bring back the page menu (Back, Reload, Print).
  const textInput = /^(text|search|email|url|tel|password|number)$/;
  const editable = (t) => {
    if (!(t instanceof Element)) return false;
    const f = t.closest('input, textarea');
    if (f) return f instanceof HTMLTextAreaElement || textInput.test(f.type);
    return t instanceof HTMLElement && t.isContentEditable;
  };
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
