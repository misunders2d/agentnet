// Shared human-composer presentation. The provider owns identity, encryption,
// scope checks and expiry. Reads follow opens/push events; timers never poll.
export function mountTyping({ api, input, line, settings }) {
  let scope = null, key = "", generation = 0, request = 0, closed = false;
  let view = null, pendingPreferences = null, entries = [], expiry, idle, active = false, last = 0;
  let sends = Promise.resolve();
  line.setAttribute("role", "status");
  line.hidden = true;
  const clearLine = () => { clearTimeout(expiry); entries = []; line.textContent = ""; line.hidden = true; };
  const draw = () => {
    clearTimeout(expiry);
    const now = Date.now();
    entries = entries.filter(e => Number.isFinite(Date.parse(e.expires)) && Date.parse(e.expires) > now);
    const names = [...new Set(entries.map(e => e.label || e.address).filter(Boolean))];
    line.replaceChildren();
    if (names.length) {
      const text = document.createElement("span"); text.textContent = names.join(", ") + (names.length === 1 ? " is typing…" : " are typing…");
      const dots = document.createElement("span"); dots.className = "typing-dots"; dots.setAttribute("aria-hidden", "true");
      for (let i = 0; i < 3; i++) dots.append(document.createElement("i"));
      line.append(text, dots);
    }
    line.hidden = !names.length;
    if (entries.length) expiry = setTimeout(draw, Math.min(...entries.map(e => Date.parse(e.expires))) - now + 1);
  };
  const send = (target, on, gen) => {
    sends = sends.then(() => {
      // A queued start from a composer already left is obsolete. Its clear
      // still uses the original scope and captured workspace transport.
      if (on && (closed || gen !== generation || !active)) return;
      return api("/api/typing", { scope: target, active: on });
    }).catch(() => {}); // ephemeral, never block or retry a message
  };
  const stop = () => {
    clearTimeout(idle);
    if (active && scope) send(scope, false, generation);
    active = false;
    last = 0;
  };
  const changed = e => {
    // Restoring a draft or an automated DOM event must not announce a human.
    if (!e.isTrusted) return;
    if (!scope || input.disabled || !input.value || !view?.current || !view?.supported || !view?.preferences?.send || document.hidden) { stop(); return; }
    clearTimeout(idle);
    const now = Date.now();
    if (!active || now - last >= 3000) { active = true; last = now; send(scope, true, generation); }
    idle = setTimeout(stop, 3000);
  };
  const visibility = () => { if (document.hidden) { stop(); clearLine(); } };
  const refresh = async () => {
    const gen = generation, seq = ++request;
    try {
      const suffix = scope ? "?" + new URLSearchParams(scope) : "";
      const next = await api("/api/typing" + suffix);
      if (closed || gen !== generation || seq !== request) return;
      view = pendingPreferences ? { ...next, preferences: pendingPreferences } : next;
      if (!view.current || !view.supported || !view.preferences?.send) stop();
      entries = view.current && view.supported && view.preferences?.show ? view.entries || [] : [];
      draw();
    } catch (_) {
      if (!closed && gen === generation && seq === request) { view = null; stop(); clearLine(); }
    }
  };
  const showSettings = async () => {
    if (!settings) return;
    const gen = generation;
    settings.textContent = "Reading typing preferences…";
    try {
      const data = await api("/api/typing");
      if (closed || gen !== generation) return;
      const title = document.createElement("h3"); title.textContent = "Human typing";
      const hint = document.createElement("p"); hint.className = "hint";
      hint.textContent = "For this device in this workspace. Agent work is shown on the request, separately.";
      const fields = {};
      const labels = [["send", "Share when I’m typing"], ["show", "Show when people are typing"]].map(([name, text]) => {
        const label = document.createElement("label"), checkbox = document.createElement("input");
        label.className = "typing-preference";
        checkbox.type = "checkbox"; checkbox.checked = data.preferences[name]; fields[name] = checkbox;
        label.append(checkbox, document.createTextNode(" " + text));
        return label;
      });
      const error = document.createElement("p"); error.className = "hint"; error.setAttribute("role", "status");
      for (const checkbox of Object.values(fields)) checkbox.addEventListener("change", async () => {
        const wanted = { send: fields.send.checked, show: fields.show.checked };
        Object.values(fields).forEach(c => { c.disabled = true; });
        pendingPreferences = wanted; request++;
        if (view) view = { ...view, preferences: wanted };
        if (!wanted.send) stop();
        if (!wanted.show) clearLine();
        try {
          await api("/api/typing/preferences", wanted);
          pendingPreferences = null; request++;
          if (closed || gen !== generation) return;
          data.preferences = wanted; error.textContent = "Saved on this device."; await refresh();
        } catch (_) {
          pendingPreferences = null; request++;
          if (closed || gen !== generation) return;
          for (const name of Object.keys(fields)) fields[name].checked = data.preferences[name];
          if (view) view = { ...view, preferences: data.preferences };
          error.textContent = "Typing preferences were not saved. Try again.";
        } finally { Object.values(fields).forEach(c => { c.disabled = false; }); }
      });
      settings.replaceChildren(title, hint, ...labels, error);
    } catch (_) { if (!closed && gen === generation) settings.textContent = "Typing preferences are unavailable on this connection."; }
  };
  input.addEventListener("input", changed);
  input.addEventListener("blur", stop);
  document.addEventListener("visibilitychange", visibility);
  window.addEventListener("pagehide", stop);
  return {
    setScope(next) {
      const nextKey = next ? JSON.stringify(next) : "";
      if (key !== nextKey) { stop(); generation++; scope = next; key = nextKey; view = null; clearLine(); }
      return scope ? refresh() : Promise.resolve();
    },
    refresh, showSettings, stop,
    disconnect() { stop(); view = null; request++; clearLine(); },
    destroy() {
      stop(); closed = true; generation++; clearLine();
      input.removeEventListener("input", changed); input.removeEventListener("blur", stop);
      document.removeEventListener("visibilitychange", visibility); window.removeEventListener("pagehide", stop);
    },
  };
}
