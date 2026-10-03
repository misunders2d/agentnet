// "Typing…" for the people in a conversation: ephemeral, never content. It
// is shared only where the server supports it and the person allows it, at
// most once every three seconds while they type, and cleared when they
// stop, send, leave the field or the tab.
import { useCallback, useEffect, useRef } from "react";
import type { Api, T } from "../api";

const every = 3000;

export function useTypingSignal(api: Api, scope: T.TypingScope | null, view: T.TypingView | null) {
  const allowed = !!scope && !!view && view.supported && view.current && view.preferences.send && view.scope.conv === scope.conv;
  const state = useRef({ active: false, last: 0, idle: 0 as number | ReturnType<typeof setTimeout> });
  const key = scope ? JSON.stringify(scope) : "";

  const stop = useCallback(() => {
    const s = state.current;
    clearTimeout(s.idle);
    if (s.active && scope) api.sendTyping(scope, false).catch(() => {});
    s.active = false;
    s.last = 0;
  }, [api, key]);

  // typed reports one change the person made; empty text or a hidden tab is a stop.
  const typed = useCallback((hasText: boolean) => {
    if (!allowed || !scope || !hasText || document.hidden) { stop(); return; }
    const s = state.current, now = Date.now();
    clearTimeout(s.idle);
    if (!s.active || now - s.last >= every) {
      s.active = true;
      s.last = now;
      api.sendTyping(scope, true).catch(() => {});
    }
    s.idle = setTimeout(stop, every);
  }, [allowed, api, key, stop]);

  useEffect(() => {
    const hidden = () => { if (document.hidden) stop(); };
    document.addEventListener("visibilitychange", hidden);
    window.addEventListener("pagehide", stop);
    return () => {
      document.removeEventListener("visibilitychange", hidden);
      window.removeEventListener("pagehide", stop);
      stop(); // leaving this conversation, or this composer
    };
  }, [stop]);

  return { typed, stop };
}
