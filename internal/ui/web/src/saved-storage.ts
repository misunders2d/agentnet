// Drafts, the send journal and send recovery live in this origin's localStorage,
// which has no compare-and-set between windows: one window writes them (the
// loader's one-window lock). A native shell whose web view cannot take that lock
// opens read-only (loader: __agentnetSingleWindow "readonly"): it reads saved
// state and never writes it, so another window's drafts and saved sends stay as
// they are. Reading, Settings and updates are unaffected.
export const savedReadOnly = (): boolean =>
  (globalThis as { __agentnetSingleWindow?: string }).__agentnetSingleWindow === "readonly";

export const readOnlyTitle = "Sending is off in this window";
export const readOnlyDetail = "AgentNet can’t make sure this is its only window here, so it changes no drafts or saved messages. Chats, Settings and updates still work; saved messages can be read and copied.";

/** localStorage for saved drafts and sends: in a read-only window, reads pass and every write is refused. */
export function savedStorage(): Storage {
  if (!savedReadOnly()) return localStorage;
  const refuse = () => { throw new Error(readOnlyTitle + ". " + readOnlyDetail); };
  return {
    get length() { return localStorage.length; },
    key: (i: number) => localStorage.key(i),
    getItem: (k: string) => localStorage.getItem(k),
    setItem: refuse, removeItem: refuse, clear: refuse,
  };
}
