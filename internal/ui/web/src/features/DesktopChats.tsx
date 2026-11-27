// Desktop chats (MEL-829, MEL-830): connect an open Claude chat or a recent
// Codex Desktop chat on this computer as a named agent, the same as
// `agentnet claude-chat|codex-chat connect`. A connected chat is reached only
// from the person's own devices and answers with that chat's own settings and
// approvals; connecting grants nobody else anything.
import { useState } from "react";
import { errorText, type T } from "../api";
import { useApp } from "../context";
import { Button } from "../ui/Button";
import { Tag } from "../ui/Tag";
import { NAME_MAX } from "./AssistantSetup.model";
import { Confirm } from "./Message.actions";
import { Card, Failed, GroupLabel, Hint, Skeleton, input, useLoad } from "./Settings.parts";

type RowState = { busy?: boolean; error?: string; label?: string; note?: string };

const kindName = (c: T.DesktopChat) => (c.kind === "codex" ? "Codex Desktop chat" : "Claude chat");
const folderName = (dir: string) => dir.replace(/[\\/]+$/, "").split(/[\\/]/).pop() || dir;
const key = (c: T.DesktopChat) => c.kind + ":" + c.id;

export function DesktopChats() {
  const store = useApp();
  const r = useLoad(() => store.api.desktopChats(), []);
  const [rows, setRows] = useState<Record<string, RowState>>({});
  const [asking, setAsking] = useState<T.DesktopChat | null>(null);
  const [name, setName] = useState("");
  const set = (k: string, s: RowState) => setRows((all) => ({ ...all, [k]: s }));

  const connect = (c: T.DesktopChat, label: string) => {
    const k = key(c);
    if (!label.trim()) { set(k, { error: "Give it a name to connect it." }); return; }
    if (rows[k]?.busy) return;
    set(k, { busy: true });
    store.api.connectDesktopChat({ kind: c.kind, id: c.id, label: label.trim() }).then(
      (res) => { set(k, { label: res.agent?.record.label || label.trim(), note: res.published ? undefined : res.note }); store.toast(res.note || "Connected.", res.published ? "ok" : "error"); r.reload(); },
      (e) => { set(k, { error: errorText(e) }); r.reload(); },
    );
  };

  let body;
  if (r.error && !r.data) body = <Failed text={r.error} retry={r.reload} />;
  else if (!r.data) body = <Skeleton lines={2} />;
  else if (!r.data.supported) body = <Hint>{r.data.note}</Hint>;
  else {
    const chats = r.data.chats || [];
    body = <>
      {r.data.note && <Hint className="mb-2">{r.data.note}</Hint>}
      {!chats.length ? <Hint>No open Claude chats or recent Codex Desktop chats on this computer.</Hint> :
        <Card className="divide-y divide-hairline">
          {chats.map((c) => {
            const k = key(c), s = rows[k] || {};
            const connectedAs = s.label || c.agent_label;
            return (
              <div key={k} className="flex flex-wrap items-center gap-3 p-3" data-desktop-chat={k}>
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[15px] font-bold">{c.name || folderName(c.dir)}</p>
                  <p className="truncate text-[13px] text-muted">{kindName(c)} · {c.dir} · {c.id.slice(0, 8)}</p>
                  {s.note && <Hint className="mt-1">{s.note}</Hint>}
                  {s.error && <p role="alert" className="mt-1 text-[13px] font-semibold text-danger">{s.error}</p>}
                  {!connectedAs && !c.ready && <Hint className="mt-1">This chat can’t receive requests yet. Open it in Claude and try again.</Hint>}
                </div>
                {connectedAs ? <Tag tone="ok">Connected as {connectedAs}</Tag>
                  : <Button size="sm" disabled={s.busy || !c.ready} onClick={() => { setName(c.name || folderName(c.dir)); setAsking(c); }}>
                      {s.busy ? "Connecting…" : s.error ? "Try again" : "Connect"}
                    </Button>}
              </div>
            );
          })}
        </Card>}
    </>;
  }

  return (
    <section aria-labelledby="desktop-chats">
      <GroupLabel id="desktop-chats">Desktop chats</GroupLabel>
      <Hint className="mb-2 px-1">Connect a chat that’s open on this computer as an agent. Only your own devices can send it requests.</Hint>
      {body}
      <Confirm open={!!asking} onOpenChange={(o) => { if (!o) setAsking(null); }} danger={false} cancel="Cancel"
        title={"Connect this " + (asking ? kindName(asking) : "chat") + "?"} ok="Connect"
        onOk={() => { const c = asking; setAsking(null); if (c) connect(c, name); }}>
        {asking && <p className="font-semibold text-ink">{(asking.name || folderName(asking.dir)) + " · " + asking.dir + " · " + asking.id.slice(0, 8)}</p>}
        <p>Requests from your own devices that name this agent go into this chat. It answers with its own settings and approvals. Other people can’t reach it.</p>
        <label htmlFor="desktop-chat-name" className="mb-1.5 mt-3 block text-[14px] font-bold text-ink">Name in AgentNet</label>
        <input id="desktop-chat-name" className={input} value={name} maxLength={NAME_MAX} autoComplete="off" onChange={(e) => setName(e.target.value)} />
      </Confirm>
    </section>
  );
}
