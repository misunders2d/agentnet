// Presentation only. Signed messages and their execution input stay untouched.
// Like Telegram Web A's edit/deletion flow (f67a408), show local intent before
// waiting for I/O. A newer authoritative revision always wins over that intent.
import type { T } from "./api";

export type Message = T.DMMessage | T.Message;
export interface MessageChange {
  action: "edit" | "delete";
  control: T.ControlAction;
  view: string;
  original: string;
  status: "pending" | "saved" | "failed";
  error?: string;
}
export const messageChangeKey = (c: T.ControlAction) => JSON.stringify([c.conv || "", c.id, c.dir]);
export const messageVersion = (m: Message) => JSON.stringify([m.id, m.dir, m.from, m.body, m.revision, m.edited, m.text, m.deleted]);
export const messageMatches = (m: Message, c: T.ControlAction) => m.id === c.id && m.dir === c.dir;
export const changeConfirmed = (m: Message, p: MessageChange) => p.action === "delete" ? !!m.deleted : !!m.edited && m.text === p.control.text;

export function presentMessage(m: Message, p: MessageChange): Message & { _change?: MessageChange } {
  if (changeConfirmed(m, p)) return m;
  if (p.status === "failed") return { ...m, _change: p };
  if (messageVersion(m) !== p.original) return m;
  return { ...m, ...(p.action === "delete" ? { deleted: true } : { edited: true, text: p.control.text }), _change: p };
}
