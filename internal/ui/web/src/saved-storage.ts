// Drafts, the send journal and send recovery live in this origin's localStorage,
// which has no compare-and-set between windows: one window writes them (the
// loader's one-window lock). A native shell whose web view cannot take that lock
// opens read-only (the host's savedReadOnly, set at mount): it reads saved
// state and never writes it, so another window's drafts and saved sends stay as
// they are. Reading, Settings and updates are unaffected.
let readOnly = false;
export const setSavedReadOnly = (on: boolean): void => { readOnly = on; };
export const savedReadOnly = (): boolean => readOnly;

export const readOnlyTitle = "Drafts are read-only in this window";
export const readOnlyDetail = "AgentNet can’t make sure this is its only window here, so this window doesn’t write drafts or saved sends: the message box is off, saved messages can be read and copied, and an unsent message can be stopped but not retried or discarded. Other chat actions and Settings, with updates, work as usual.";

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
