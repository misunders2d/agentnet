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
  // Selected text counts only where it was right-clicked: the click must
  // land on the selection's own drawn text, not a blank spot beside it.
  const selectedAt = (e) => {
    const s = window.getSelection();
    if (!s || s.isCollapsed || String(s).trim() === '') return false;
    for (let i = 0; i < s.rangeCount; i++) {
      for (const r of s.getRangeAt(i).getClientRects()) {
        if (r.width && e.clientX >= r.left && e.clientX <= r.right && e.clientY >= r.top && e.clientY <= r.bottom) return true;
      }
    }
    return false;
  };
  window.addEventListener('contextmenu', (e) => {
    if (!editable(e.target) && !selectedAt(e)) e.preventDefault();
  });
})();
